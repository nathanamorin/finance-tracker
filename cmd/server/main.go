// Command server runs fin-tracker: a self-hosted personal finance tracker
// with one encrypted SQLite database per SSO user.
//
// Configuration (environment variables):
//
//	FIN_TRACKER_MASTER_KEY            required; base64 32-byte key-encryption key (openssl rand -base64 32)
//	FIN_TRACKER_PREVIOUS_MASTER_KEYS  optional; comma-separated retired master keys, for rotation
//	FIN_TRACKER_DATA_DIR              default /data
//	FIN_TRACKER_ADDR                  default :8080
//	FIN_TRACKER_USER_HEADER           default X-Auth-Request-User (set by oauth2-proxy via Istio ext_authz)
//	FIN_TRACKER_EMAIL_HEADER          default X-Auth-Request-Email
//	FIN_TRACKER_DEV_USER              local development only: user ID to assume when the header is absent
//	FIN_TRACKER_MAX_UPLOAD_MB         default 25
//	FIN_TRACKER_IDLE_CLOSE            default 15m; close a user's database after this long unused
//	TZ                                time zone used for "today" (e.g. America/Chicago)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the distroless runtime image has no zoneinfo; honor TZ anyway

	"github.com/nathanamorin/finance-tracker/internal/api"
	"github.com/nathanamorin/finance-tracker/internal/ingest"
	"github.com/nathanamorin/finance-tracker/internal/ingest/benefitscenter"
	"github.com/nathanamorin/finance-tracker/internal/ingest/vanguard"
	"github.com/nathanamorin/finance-tracker/internal/keystore"
	"github.com/nathanamorin/finance-tracker/internal/store"
	"github.com/nathanamorin/finance-tracker/web"
)

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	masterKey := os.Getenv("FIN_TRACKER_MASTER_KEY")
	if masterKey == "" {
		return errors.New("FIN_TRACKER_MASTER_KEY is required (generate with: openssl rand -base64 32)")
	}
	var previous []string
	for _, k := range strings.Split(os.Getenv("FIN_TRACKER_PREVIOUS_MASTER_KEYS"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			previous = append(previous, k)
		}
	}
	keys, err := keystore.New(masterKey, previous)
	if err != nil {
		return err
	}
	// Don't leave key material in the process environment longer than needed.
	os.Unsetenv("FIN_TRACKER_MASTER_KEY")
	os.Unsetenv("FIN_TRACKER_PREVIOUS_MASTER_KEYS")

	idle, err := time.ParseDuration(env("FIN_TRACKER_IDLE_CLOSE", "15m"))
	if err != nil {
		return err
	}
	maxMB, err := strconv.ParseInt(env("FIN_TRACKER_MAX_UPLOAD_MB", "25"), 10, 64)
	if err != nil || maxMB <= 0 {
		return errors.New("FIN_TRACKER_MAX_UPLOAD_MB must be a positive integer")
	}

	stores, err := store.NewManager(env("FIN_TRACKER_DATA_DIR", "/data"), keys, idle, time.Local)
	if err != nil {
		return err
	}

	// Data sources, in auto-detection order. Add new parsers here.
	registry := ingest.NewRegistry(
		vanguard.CSV{},
		benefitscenter.CSV{},
	)

	devUser := os.Getenv("FIN_TRACKER_DEV_USER")
	if devUser != "" {
		slog.Warn("FIN_TRACKER_DEV_USER is set: requests without an SSO header are treated as this user. Never use this in a deployment.", "user", devUser)
	}

	e := api.New(api.Config{
		UserHeader:     env("FIN_TRACKER_USER_HEADER", "X-Auth-Request-User"),
		EmailHeader:    env("FIN_TRACKER_EMAIL_HEADER", "X-Auth-Request-Email"),
		DevUser:        devUser,
		MaxUploadBytes: maxMB << 20,
		Location:       time.Local,
	}, stores, registry, web.FS())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go stores.Run(ctx)

	srv := &http.Server{
		Addr:              env("FIN_TRACKER_ADDR", ":8080"),
		Handler:           e,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	stores.CloseAll() // flush & close every open database before exit
	return err
}
