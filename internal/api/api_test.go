package api

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
	"github.com/nathanamorin/finance-tracker/internal/ingest/benefitscenter"
	"github.com/nathanamorin/finance-tracker/internal/ingest/etrade"
	"github.com/nathanamorin/finance-tracker/internal/ingest/vanguard"
	"github.com/nathanamorin/finance-tracker/internal/keystore"
	"github.com/nathanamorin/finance-tracker/internal/store"
)

// fixture is an anonymised real Vanguard export. It is FAKE DATA; see
// internal/ingest/vanguard/testdata/README.md.
const fixture = "../ingest/vanguard/testdata/vanguard_anonymised_export.csv"

func newTestServer(t *testing.T, devUser string) *echo.Echo {
	t.Helper()
	k := make([]byte, 32)
	rand.Read(k)
	ks, _ := keystore.New(base64.StdEncoding.EncodeToString(k), nil)
	m, err := store.NewManager(t.TempDir(), ks, time.Minute, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.CloseAll)
	return New(Config{
		UserHeader: "X-Auth-Request-User", EmailHeader: "X-Auth-Request-Email",
		DevUser: devUser, MaxUploadBytes: 5 << 20, Location: time.UTC,
	}, m, ingest.NewRegistry(vanguard.CSV{}, benefitscenter.CSV{}, etrade.RestrictedStock{}), nil)
}

func do(e *echo.Echo, req *http.Request, user string) *httptest.ResponseRecorder {
	if user != "" {
		req.Header.Set("X-Auth-Request-User", user)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestFailsClosedWithoutIdentity(t *testing.T) {
	e := newTestServer(t, "")
	rec := do(e, httptest.NewRequest(http.MethodGet, "/api/summary", nil), "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
	if rec := do(e, httptest.NewRequest(http.MethodGet, "/healthz", nil), ""); rec.Code != http.StatusOK {
		t.Fatalf("healthz %d", rec.Code)
	}
}

func upload(t *testing.T, e *echo.Echo, user string) *httptest.ResponseRecorder {
	t.Helper()
	return uploadFile(t, e, user, fixture)
}

func uploadFile(t *testing.T, e *echo.Echo, user, path string) *httptest.ResponseRecorder {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, _ := w.CreateFormFile("file", "OfxDownload.csv")
	fw.Write(content)
	w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/imports", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return do(e, req, user)
}

func TestUploadAndPerUserIsolation(t *testing.T) {
	e := newTestServer(t, "")

	rec := upload(t, e, "alice")
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var im store.Import
	json.Unmarshal(rec.Body.Bytes(), &im)
	if im.Source != "vanguard_csv" || im.TransactionsInserted != 460 {
		t.Fatalf("import: %+v", im)
	}

	var page struct{ Total int }
	rec = do(e, httptest.NewRequest(http.MethodGet, "/api/transactions?type=dividend", nil), "alice")
	json.Unmarshal(rec.Body.Bytes(), &page)
	if page.Total != 72 {
		t.Fatalf("alice dividends = %d, want 72", page.Total)
	}

	rec = do(e, httptest.NewRequest(http.MethodGet, "/api/transactions", nil), "bob")
	json.Unmarshal(rec.Body.Bytes(), &page)
	if page.Total != 0 {
		t.Fatalf("bob sees %d of alice's transactions", page.Total)
	}

	for _, p := range []string{"/api/summary", "/api/accounts", "/api/holdings", "/api/reports/value-history?interval=week", "/api/reports/cash-flow", "/api/reports/income", "/api/imports", "/api/meta", "/api/performance", "/api/equity"} {
		if rec := do(e, httptest.NewRequest(http.MethodGet, p, nil), "alice"); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
}

func TestRejectsUnknownFile(t *testing.T) {
	e := newTestServer(t, "dev")
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, _ := w.CreateFormFile("file", "bank.csv")
	fw.Write([]byte("Date,Description,Amount\n2026-01-01,Coffee,-4.00\n"))
	w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/imports", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if rec := do(e, req, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
}

// benefitsFixture is an anonymised real Benefits Center export. It is FAKE
// DATA; see internal/ingest/benefitscenter/testdata/README.md.
const benefitsFixture = "../ingest/benefitscenter/testdata/benefits_center_anonymised_export.csv"

func TestUploadBenefitsCenter(t *testing.T) {
	e := newTestServer(t, "")
	rec := uploadFile(t, e, "alice", benefitsFixture)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var im store.Import
	json.Unmarshal(rec.Body.Bytes(), &im)
	if im.Source != "benefits_center_401k_csv" || im.HoldingsParsed != 1 || im.HoldingsAsOf == nil || *im.HoldingsAsOf != "2026-11-25" {
		t.Fatalf("auto-detected import: %+v", im)
	}
	var perf []store.Performance
	rec = do(e, httptest.NewRequest(http.MethodGet, "/api/performance", nil), "alice")
	json.Unmarshal(rec.Body.Bytes(), &perf)
	if len(perf) != 1 || perf[0].OtherActivity != 13426.39 {
		t.Fatalf("performance: %+v", perf)
	}
}

// etradeFixture is an anonymised real E*TRADE Restricted Stock export. It
// is FAKE DATA; see internal/ingest/etrade/testdata/README.md.
const etradeFixture = "../ingest/etrade/testdata/etrade_restricted_stock_anonymised.xlsx"

func TestUploadETrade(t *testing.T) {
	e := newTestServer(t, "")
	rec := uploadFile(t, e, "alice", etradeFixture)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var im store.Import
	json.Unmarshal(rec.Body.Bytes(), &im)
	if im.Source != "etrade_restricted_stock_xlsx" || len(im.Warnings) != 0 {
		t.Fatalf("auto-detected import: %+v", im)
	}
	var eq store.Equity
	rec = do(e, httptest.NewRequest(http.MethodGet, "/api/equity", nil), "alice")
	json.Unmarshal(rec.Body.Bytes(), &eq)
	if len(eq.Grants) != 3 || eq.UnvestedValue < 15480 {
		t.Fatalf("equity: %+v", eq)
	}
}

func TestIncludeUnvestedParam(t *testing.T) {
	e := newTestServer(t, "")
	if rec := uploadFile(t, e, "alice", etradeFixture); rec.Code != http.StatusCreated { // FAKE DATA
		t.Fatalf("upload: %d", rec.Code)
	}
	var holdings []store.Holding
	rec := do(e, httptest.NewRequest(http.MethodGet, "/api/holdings?include_unvested=1", nil), "alice")
	json.Unmarshal(rec.Body.Bytes(), &holdings)
	if len(holdings) != 1 || !holdings[0].Unvested {
		t.Fatalf("holdings with unvested: %+v", holdings)
	}
	rec = do(e, httptest.NewRequest(http.MethodGet, "/api/holdings", nil), "alice")
	holdings = nil
	json.Unmarshal(rec.Body.Bytes(), &holdings)
	if len(holdings) != 0 {
		t.Fatalf("default holdings should exclude unvested: %+v", holdings)
	}
	var sum store.Summary
	rec = do(e, httptest.NewRequest(http.MethodGet, "/api/summary?include_unvested=true", nil), "alice")
	json.Unmarshal(rec.Body.Bytes(), &sum)
	if !sum.IncludesUnvested || sum.TotalValue < 15480 {
		t.Fatalf("summary: %+v", sum)
	}
}

func TestSettingsRoundTripAndIsolation(t *testing.T) {
	e := newTestServer(t, "")
	get := func(user string) string {
		rec := do(e, httptest.NewRequest(http.MethodGet, "/api/settings/projections", nil), user)
		if rec.Code != http.StatusOK {
			t.Fatalf("get: %d %s", rec.Code, rec.Body)
		}
		return strings.TrimSpace(rec.Body.String())
	}
	if got := get("alice"); got != `{"value":null}` {
		t.Fatalf("unset: %s", got)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/settings/projections", strings.NewReader(`{"years":30}`))
	if rec := do(e, req, "alice"); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if got := get("alice"); got != `{"value":{"years":30}}` {
		t.Fatalf("after put: %s", got)
	}
	if got := get("bob"); got != `{"value":null}` {
		t.Fatalf("bob sees alice's settings: %s", got)
	}
	bad := httptest.NewRequest(http.MethodPut, "/api/settings/projections", strings.NewReader(`{not json`))
	if rec := do(e, bad, "alice"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON: %d", rec.Code)
	}
	badKey := httptest.NewRequest(http.MethodGet, "/api/settings/..%2Fetc", nil)
	if rec := do(e, badKey, "alice"); rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
		t.Fatalf("bad key: %d", rec.Code)
	}
}
