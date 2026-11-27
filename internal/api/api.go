// Package api exposes the JSON API and serves the embedded web UI.
package api

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
	"github.com/nathanamorin/finance-tracker/internal/keystore"
	"github.com/nathanamorin/finance-tracker/internal/store"
)

// Config configures the API server.
type Config struct {
	UserHeader     string
	EmailHeader    string
	DevUser        string
	MaxUploadBytes int64
	Location       *time.Location
}

// Server holds the API's dependencies.
type Server struct {
	cfg      Config
	stores   *store.Manager
	registry *ingest.Registry
}

const storeKey = "store"

// New builds the Echo router. webFS is the built frontend (may be nil).
func New(cfg Config, stores *store.Manager, registry *ingest.Registry, webFS fs.FS) *echo.Echo {
	s := &Server{cfg: cfg, stores: stores, registry: registry}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = errorHandler
	e.Use(middleware.Recover())
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogStatus: true, LogURI: true, LogMethod: true, LogLatency: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			if v.URI != "/healthz" {
				slog.Info("request", "method", v.Method, "uri", v.URI, "status", v.Status, "latency", v.Latency)
			}
			return nil
		},
	}))
	e.Use(securityHeaders)

	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "open_databases": stores.OpenCount()})
	})

	g := e.Group("/api", authMiddleware(cfg.UserHeader, cfg.EmailHeader, cfg.DevUser), s.withStore)
	g.GET("/me", s.me)
	g.GET("/meta", s.meta)
	g.GET("/summary", s.summary)
	g.GET("/accounts", s.listAccounts)
	g.PATCH("/accounts/:id", s.updateAccount)
	g.GET("/transactions", s.listTransactions)
	g.GET("/holdings", s.listHoldings)
	g.GET("/performance", s.listPerformance)
	g.GET("/equity", s.equity)
	g.GET("/securities", s.listSecurities)
	g.GET("/imports", s.listImports)
	g.POST("/imports", s.createImport, middleware.BodyLimit(fmt.Sprintf("%dB", cfg.MaxUploadBytes+1<<20)))
	g.GET("/imports/:id/file", s.downloadImportFile)
	g.DELETE("/imports/:id", s.deleteImport)
	g.GET("/reports/value-history", s.valueHistory)
	g.GET("/reports/cash-flow", s.cashFlow)
	g.GET("/reports/income", s.incomeBySecurity)

	if webFS != nil {
		e.GET("/*", spaHandler(webFS))
	}
	return e
}

// withStore opens the caller's own database for the request's duration.
func (s *Server) withStore(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		st, release, err := s.stores.Acquire(c.Request().Context(), identity(c).UserID)
		if err != nil {
			return err
		}
		defer release()
		c.Set(storeKey, st)
		return next(c)
	}
}

func st(c echo.Context) *store.Store { return c.Get(storeKey).(*store.Store) }

func securityHeaders(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		h := c.Response().Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(c.Request().URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		return next(c)
	}
}

func errorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	code, msg := http.StatusInternalServerError, "internal error"
	var he *echo.HTTPError
	switch {
	case errors.As(err, &he):
		code = he.Code
		msg = fmt.Sprint(he.Message)
	case errors.Is(err, sql.ErrNoRows):
		code, msg = http.StatusNotFound, "not found"
	case errors.Is(err, store.ErrInvalid):
		code, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, keystore.ErrNoUsableKey), errors.Is(err, store.ErrWrongKey):
		msg = "your database could not be decrypted; check the server's master key configuration"
		slog.Error("decrypting user database", "err", err)
	default:
		slog.Error("request failed", "uri", c.Request().RequestURI, "err", err)
	}
	_ = c.JSON(code, map[string]string{"error": msg})
}

func (s *Server) me(c echo.Context) error { return c.JSON(http.StatusOK, identity(c)) }

type sourceInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Server) meta(c echo.Context) error {
	sources := []sourceInfo{}
	for _, src := range s.registry.All() {
		sources = append(sources, sourceInfo{src.ID(), src.Name(), src.Description()})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"sources":       sources,
		"account_types": ingest.AccountTypes,
		"max_upload_mb": s.cfg.MaxUploadBytes >> 20,
	})
}

func (s *Server) summary(c echo.Context) error {
	sum, err := st(c).Summary(c.Request().Context(), reportOptions(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, sum)
}

func (s *Server) listAccounts(c echo.Context) error {
	accts, err := st(c).ListAccounts(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, accts)
}

func (s *Server) updateAccount(c echo.Context) error {
	id, err := pathID(c)
	if err != nil {
		return err
	}
	var u store.AccountUpdate
	if err := c.Bind(&u); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON body")
	}
	if err := st(c).UpdateAccount(c.Request().Context(), id, u); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) listTransactions(c echo.Context) error {
	q := c.QueryParams()
	f := store.TransactionFilter{
		AccountID:  queryInt(c, "account_id"),
		SecurityID: queryInt(c, "security_id"),
		From:       q.Get("from"),
		To:         q.Get("to"),
		Query:      q.Get("q"),
		Limit:      int(queryInt(c, "limit")),
		Offset:     int(queryInt(c, "offset")),
	}
	for _, t := range q["type"] {
		for _, part := range strings.Split(t, ",") {
			if part = strings.TrimSpace(part); part != "" {
				f.Types = append(f.Types, part)
			}
		}
	}
	for _, d := range []string{f.From, f.To} {
		if d != "" && !validDate(d) {
			return echo.NewHTTPError(http.StatusBadRequest, "dates must be YYYY-MM-DD")
		}
	}
	txs, total, err := st(c).ListTransactions(c.Request().Context(), f)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"transactions": txs, "total": total})
}

// listHoldings returns owned positions, followed by unvested equity rows
// (unvested: true) when include_unvested is set.
func (s *Server) listHoldings(c echo.Context) error {
	h, err := st(c).LatestHoldings(c.Request().Context())
	if err != nil {
		return err
	}
	if reportOptions(c).IncludeUnvested {
		u, err := st(c).UnvestedHoldings(c.Request().Context())
		if err != nil {
			return err
		}
		h = append(h, u...)
	}
	return c.JSON(http.StatusOK, h)
}

// reportOptions reads ?include_unvested=1|true.
func reportOptions(c echo.Context) store.ReportOptions {
	v := strings.ToLower(c.QueryParam("include_unvested"))
	return store.ReportOptions{IncludeUnvested: v == "1" || v == "true" || v == "yes"}
}

func (s *Server) listPerformance(c echo.Context) error {
	p, err := st(c).LatestPerformance(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}

func (s *Server) equity(c echo.Context) error {
	eq, err := st(c).Equity(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, eq)
}

func (s *Server) listSecurities(c echo.Context) error {
	secs, err := st(c).ListSecurities(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, secs)
}

func (s *Server) listImports(c echo.Context) error {
	ims, err := st(c).ListImports(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, ims)
}

// createImport accepts a multipart upload: file (required), source
// (optional; auto-detected) and as_of (optional YYYY-MM-DD date for
// position snapshots; defaults to today).
func (s *Server) createImport(c echo.Context) error {
	fh, err := c.FormFile("file")
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "missing file")
	}
	if fh.Size > s.cfg.MaxUploadBytes {
		return echo.NewHTTPError(http.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds %d MB", s.cfg.MaxUploadBytes>>20))
	}
	f, err := fh.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, s.cfg.MaxUploadBytes+1))
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "file is empty")
	}

	var src ingest.Source
	if id := c.FormValue("source"); id != "" {
		var ok bool
		if src, ok = s.registry.Get(id); !ok {
			return echo.NewHTTPError(http.StatusBadRequest, "unknown source "+id)
		}
	} else {
		if src, err = s.registry.Detect(fh.Filename, content); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	}

	asOf := c.FormValue("as_of")
	if asOf == "" {
		asOf = time.Now().In(s.cfg.Location).Format("2006-01-02")
	} else if !validDate(asOf) {
		return echo.NewHTTPError(http.StatusBadRequest, "as_of must be YYYY-MM-DD")
	}

	res, err := src.Parse(bytes.NewReader(content), ingest.ParseOptions{AsOf: ingest.Date(asOf)})
	if err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "could not parse file as "+src.Name()+": "+err.Error())
	}
	im, err := st(c).SaveImport(c.Request().Context(), src.ID(), path.Base(fh.Filename), content, res)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, im)
}

func (s *Server) downloadImportFile(c echo.Context) error {
	id, err := pathID(c)
	if err != nil {
		return err
	}
	name, content, err := st(c).ImportFile(c.Request().Context(), id)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(name)))
	return c.Blob(http.StatusOK, "application/octet-stream", content)
}

func (s *Server) deleteImport(c echo.Context) error {
	id, err := pathID(c)
	if err != nil {
		return err
	}
	if err := st(c).DeleteImport(c.Request().Context(), id); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) valueHistory(c echo.Context) error {
	vh, err := st(c).ValueHistory(c.Request().Context(), c.QueryParam("interval"), queryInt(c, "account_id"), reportOptions(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, vh)
}

func (s *Server) cashFlow(c echo.Context) error {
	rows, err := st(c).CashFlow(c.Request().Context(), c.QueryParam("interval"), queryInt(c, "account_id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, rows)
}

func (s *Server) incomeBySecurity(c echo.Context) error {
	rows, err := st(c).IncomeBySecurity(c.Request().Context(), c.QueryParam("interval"), queryInt(c, "account_id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, rows)
}

func pathID(c echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	return id, nil
}

func queryInt(c echo.Context, name string) int64 {
	v, _ := strconv.ParseInt(c.QueryParam(name), 10, 64)
	if v < 0 {
		return 0
	}
	return v
}

func validDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// spaHandler serves the built frontend, falling back to index.html for
// client-side routes.
func spaHandler(webFS fs.FS) echo.HandlerFunc {
	fileServer := http.FileServer(http.FS(webFS))
	return func(c echo.Context) error {
		p := strings.TrimPrefix(path.Clean(c.Request().URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(webFS, p); err != nil {
			p = "index.html"
		}
		if p == "index.html" {
			c.Response().Header().Set("Cache-Control", "no-cache")
			html, err := fs.ReadFile(webFS, "index.html")
			if err != nil {
				return echo.NewHTTPError(http.StatusNotFound, "frontend not built")
			}
			return c.HTMLBlob(http.StatusOK, html)
		}
		if strings.HasPrefix(p, "assets/") {
			// Vite emits content-hashed filenames under assets/.
			c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(c.Response(), c.Request())
		return nil
	}
}
