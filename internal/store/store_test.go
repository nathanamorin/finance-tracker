package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
	"github.com/nathanamorin/finance-tracker/internal/ingest/benefitscenter"
	"github.com/nathanamorin/finance-tracker/internal/ingest/etrade"
	"github.com/nathanamorin/finance-tracker/internal/ingest/vanguard"
	"github.com/nathanamorin/finance-tracker/internal/keystore"
)

// fixture is an anonymised real Vanguard export. It is FAKE DATA; see
// internal/ingest/vanguard/testdata/README.md.
const fixture = "../ingest/vanguard/testdata/vanguard_anonymised_export.csv"

func newManager(t *testing.T, dir string, masterKey string) *Manager {
	t.Helper()
	ks, err := keystore.New(masterKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(dir, ks, time.Minute, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.CloseAll)
	return m
}

func randomKey(t *testing.T) string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func importFixture(t *testing.T, s *Store) *Import {
	t.Helper()
	content, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	today := ingest.NewDate(time.Now().UTC())
	res, err := vanguard.CSV{}.Parse(bytes.NewReader(content), ingest.ParseOptions{AsOf: today})
	if err != nil {
		t.Fatal(err)
	}
	im, err := s.SaveImport(context.Background(), "vanguard_csv", "vanguard_anonymised_export.csv", content, res)
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func TestImportIsIdempotent(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, t.TempDir(), randomKey(t))
	s, release, err := m.Acquire(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	first := importFixture(t, s)
	if first.TransactionsInserted != 460 || first.HoldingsParsed != 7 {
		t.Fatalf("first import: %+v", first)
	}
	second := importFixture(t, s)
	if second.TransactionsInserted != 0 || len(second.Warnings) == 0 {
		t.Fatalf("re-import should insert nothing and warn: %+v", second)
	}

	_, total, err := s.ListTransactions(ctx, TransactionFilter{})
	if err != nil || total != 460 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	holdings, _ := s.LatestHoldings(ctx)
	if len(holdings) != 7 {
		t.Fatalf("holdings after re-import = %d, want 7 (snapshot replaced, not duplicated)", len(holdings))
	}
}

func TestReportsAgreeWithSnapshot(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, t.TempDir(), randomKey(t))
	s, release, _ := m.Acquire(ctx, "alice")
	defer release()
	importFixture(t, s)

	sum, err := s.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Sum of the fixture's "Total Value" column.
	if want := 112835.26 + 27007.02 + 9245.85 + 21912.77 + 21062.38 + 7268.94; math.Abs(sum.TotalValue-want) > 0.01 {
		t.Fatalf("total value %v, want %v", sum.TotalValue, want)
	}
	if sum.AccountCount != 2 || sum.NetContributionsAllTime <= 0 {
		t.Fatalf("summary: %+v", sum)
	}

	vh, err := s.ValueHistory(ctx, "month", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(vh.Points) < 12 {
		t.Fatalf("expected a year+ of monthly points, got %d", len(vh.Points))
	}
	// The reconstruction is anchored to the latest snapshot, so today's
	// point must match the snapshot total, to within rounding: Vanguard's
	// "Total Value" uses unrounded prices, while shares × displayed price
	// drifts by cents.
	last := vh.Points[len(vh.Points)-1]
	if math.Abs(last.Value-sum.TotalValue) > 1 {
		t.Fatalf("reconstructed today %v != snapshot %v", last.Value, sum.TotalValue)
	}
	if len(vh.UnpricedSecurities) != 0 {
		t.Fatalf("unpriced: %v", vh.UnpricedSecurities)
	}
	for i := 1; i < len(vh.Points); i++ {
		if vh.Points[i].NetContributions < vh.Points[i-1].NetContributions-5000 {
			// Only 2 withdrawals in the fixture; contributions should mostly rise.
			t.Fatalf("net contributions dropped sharply at %s", vh.Points[i].Date)
		}
	}

	cf, err := s.CashFlow(ctx, "month", 0)
	if err != nil || len(cf) == 0 {
		t.Fatalf("cashflow: %v %v", cf, err)
	}
}

func TestDeleteImportCascades(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, t.TempDir(), randomKey(t))
	s, release, _ := m.Acquire(ctx, "alice")
	defer release()
	im := importFixture(t, s)
	if err := s.DeleteImport(ctx, im.ID); err != nil {
		t.Fatal(err)
	}
	_, total, _ := s.ListTransactions(ctx, TransactionFilter{})
	holdings, _ := s.LatestHoldings(ctx)
	if total != 0 || len(holdings) != 0 {
		t.Fatalf("after delete: %d transactions, %d holdings", total, len(holdings))
	}
}

func TestDatabaseIsEncryptedAndIsolated(t *testing.T) {
	ctx := context.Background()
	dir, key := t.TempDir(), randomKey(t)
	m := newManager(t, dir, key)

	alice, release, err := m.Acquire(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	importFixture(t, alice)
	release()
	m.CloseAll()

	// No plaintext on disk: neither SQLite's header nor fixture content.
	aliceDir := m.UserDir("alice")
	for _, name := range []string{"finance.db", "finance.db-wal"} {
		raw, err := os.ReadFile(filepath.Join(aliceDir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range []string{"SQLite format 3", "VANGUARD", "VFIAX", "10000001", "Dividend"} {
			if bytes.Contains(raw, []byte(needle)) {
				t.Fatalf("%s contains plaintext %q", name, needle)
			}
		}
	}

	// Each user has their own database.
	bob, releaseBob, err := m.Acquire(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, total, _ := bob.ListTransactions(ctx, TransactionFilter{}); total != 0 {
		t.Fatalf("bob sees %d transactions", total)
	}
	releaseBob()

	// Re-opening with the right master key works.
	m2 := newManager(t, dir, key)
	a2, rel, err := m2.Acquire(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, total, _ := a2.ListTransactions(ctx, TransactionFilter{}); total != 460 {
		t.Fatalf("reopened total = %d", total)
	}
	rel()
	m2.CloseAll()

	// A different master key cannot open it.
	m3 := newManager(t, dir, randomKey(t))
	if _, _, err := m3.Acquire(ctx, "alice"); !errors.Is(err, keystore.ErrNoUsableKey) {
		t.Fatalf("wrong master key: want ErrNoUsableKey, got %v", err)
	}

	// The raw database with a wrong DEK is rejected cleanly.
	wrong := make([]byte, keystore.DEKSize)
	if _, err := Open(ctx, filepath.Join(aliceDir, "finance.db"), wrong, time.UTC); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("wrong DEK: want ErrWrongKey, got %v", err)
	}
}

func TestPeriodEnds(t *testing.T) {
	got := periodEnds("2026-01-15", "2026-03-10", "month")
	want := []string{"2026-01-31", "2026-02-28", "2026-03-10"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// benefitsFixture is an anonymised real Benefits Center 401(k) fund summary.
// It is FAKE DATA; see internal/ingest/benefitscenter/testdata/README.md.
const benefitsFixture = "../ingest/benefitscenter/testdata/benefits_center_anonymised_export.csv"

func importBenefits(t *testing.T, s *Store) *Import {
	t.Helper()
	content, err := os.ReadFile(benefitsFixture)
	if err != nil {
		t.Fatal(err)
	}
	res, err := benefitscenter.CSV{}.Parse(bytes.NewReader(content), ingest.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	im, err := s.SaveImport(context.Background(), benefitscenter.CSV{}.ID(), "401k.csv", content, res)
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func TestBenefitsCenterImport(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, t.TempDir(), randomKey(t))
	s, release, _ := m.Acquire(ctx, "alice")
	defer release()

	im := importBenefits(t, s)
	if im.HoldingsAsOf == nil || *im.HoldingsAsOf != "2026-11-25" || im.HoldingsParsed != 1 {
		t.Fatalf("import: %+v", im)
	}
	importBenefits(t, s) // re-import is idempotent

	accts, _ := s.ListAccounts(ctx)
	if len(accts) != 1 || accts[0].AccountType != "401k" || accts[0].Name != "Employer 401(k) Plan - All Account" ||
		accts[0].Balance != 117426.62 {
		t.Fatalf("accounts: %+v", accts)
	}
	holdings, _ := s.LatestHoldings(ctx)
	if len(holdings) != 1 || holdings[0].AssetClass == nil || *holdings[0].AssetClass != "Stocks--US Stocks" {
		t.Fatalf("holdings: %+v", holdings)
	}
	perf, err := s.LatestPerformance(ctx)
	if err != nil || len(perf) != 1 || perf[0].GainLoss != 6034.83 || perf[0].OtherActivity != 13426.39 ||
		perf[0].StartingBalance != 97965.40 {
		t.Fatalf("performance: %+v %v", perf, err)
	}
	sum, _ := s.Summary(ctx)
	if len(sum.AllocationByClass) != 1 || sum.AllocationByClass[0].Key != "Stocks--US Stocks" {
		t.Fatalf("allocation by class: %+v", sum.AllocationByClass)
	}
	// Fund summaries never become ledger entries.
	if _, total, _ := s.ListTransactions(ctx, TransactionFilter{}); total != 0 {
		t.Fatalf("transactions: %d", total)
	}
}

func TestBenefitsCenterValueHistoryUsesBalances(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, t.TempDir(), randomKey(t))
	s, release, _ := m.Acquire(ctx, "alice")
	defer release()
	importBenefits(t, s)

	vh, err := s.ValueHistory(ctx, "month", 0)
	if err != nil {
		t.Fatal(err)
	}
	at := func(date string) float64 {
		for _, p := range vh.Points {
			if p.Date == date {
				return p.Value
			}
		}
		t.Fatalf("no point at %s in %+v", date, vh.Points)
		return 0
	}
	// Starts at the derived period-start balance and carries it forward...
	if vh.Points[0].Date != "2026-01-31" || at("2026-01-31") != 97965.40 || at("2026-10-31") != 97965.40 {
		t.Fatalf("points: %+v", vh.Points)
	}
	// ...then picks up the snapshot balance from its date on.
	if last := vh.Points[len(vh.Points)-1]; last.Value != 117426.62 {
		t.Fatalf("last point %+v", last)
	}

	// Combined with a transaction-based account, values add up and the
	// 401k contributes nothing before its first balance.
	importFixture(t, s)
	vh, _ = s.ValueHistory(ctx, "month", 0)
	sum, _ := s.Summary(ctx)
	if last := vh.Points[len(vh.Points)-1]; math.Abs(last.Value-sum.TotalValue) > 1 {
		t.Fatalf("combined last %v vs total %v", last.Value, sum.TotalValue)
	}
	vanguardOnly := func(date string) float64 {
		v, _ := s.ValueHistory(ctx, "month", 0)
		for _, p := range v.Points {
			if p.Date == date {
				return p.Value
			}
		}
		return -1
	}
	if v := vanguardOnly("2025-12-31"); v <= 0 || v > 190000 {
		t.Fatalf("2025-12-31 should hold only the Vanguard reconstruction, got %v", v)
	}
}

// E*TRADE Restricted Stock fixtures are FAKE DATA; see
// internal/ingest/etrade/testdata/README.md.
const (
	etradeFixture   = "../ingest/etrade/testdata/etrade_restricted_stock_anonymised.xlsx"
	etradeSynthetic = "../ingest/etrade/testdata/synthetic_vested_and_released.xlsx"
)

func importETrade(t *testing.T, s *Store, path string, asOf ingest.Date) *Import {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := etrade.RestrictedStock{}.Parse(bytes.NewReader(content), ingest.ParseOptions{AsOf: asOf})
	if err != nil {
		t.Fatal(err)
	}
	im, err := s.SaveImport(context.Background(), etrade.RestrictedStock{}.ID(), filepath.Base(path), content, res)
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func TestEquityUnvestedIsTrackedButNotNetWorth(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, t.TempDir(), randomKey(t))
	s, release, _ := m.Acquire(ctx, "alice")
	defer release()

	today := ingest.NewDate(time.Now().UTC())
	importETrade(t, s, etradeFixture, today)
	importETrade(t, s, etradeFixture, today) // idempotent

	eq, err := s.Equity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(eq.Grants) != 3 || len(eq.Upcoming) != 3 {
		t.Fatalf("grants=%d upcoming=%d", len(eq.Grants), len(eq.Upcoming))
	}
	if math.Abs(eq.UnvestedShares-41.246) > 1e-9 || math.Abs(eq.UnvestedValue-15488.99) > 1 {
		t.Fatalf("unvested %v shares / $%v", eq.UnvestedShares, eq.UnvestedValue)
	}
	if eq.Upcoming[0].VestDate != "2027-02-16" || eq.Upcoming[2].VestDate != "2029-02-16" {
		t.Fatalf("upcoming order: %+v", eq.Upcoming)
	}
	for _, g := range eq.Grants {
		if len(g.Vests) != 1 {
			t.Fatalf("grant %s has %d tranches after re-import", g.GrantNumber, len(g.Vests))
		}
	}

	sum, _ := s.Summary(ctx)
	if sum.TotalValue != 0 {
		t.Fatalf("unvested RSUs leaked into total value: %v", sum.TotalValue)
	}
	if math.Abs(sum.UnvestedEquityValue-15488.99) > 1 || sum.NextVest == nil || sum.NextVest.VestDate != "2027-02-16" {
		t.Fatalf("summary equity: %+v next=%+v", sum.UnvestedEquityValue, sum.NextVest)
	}
	vh, _ := s.ValueHistory(ctx, "month", 0)
	for _, p := range vh.Points {
		if p.Value != 0 {
			t.Fatalf("unvested RSUs leaked into value history at %s: %v", p.Date, p.Value)
		}
	}
	accts, _ := s.ListAccounts(ctx)
	if len(accts) != 1 || accts[0].AccountType != "stock_plan" || accts[0].Name != "E*TRADE Stock Plan" {
		t.Fatalf("accounts: %+v", accts)
	}

	// An older snapshot must not roll grants back.
	importETrade(t, s, etradeFixture, "2020-01-01")
	eq, _ = s.Equity(ctx)
	if eq.Grants[0].AsOfDate != string(today) {
		t.Fatalf("older import rolled grant back to %s", eq.Grants[0].AsOfDate)
	}
}

func TestEquitySellableSharesAreHoldings(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, t.TempDir(), randomKey(t))
	s, release, _ := m.Acquire(ctx, "alice")
	defer release()

	im := importETrade(t, s, etradeSynthetic, ingest.NewDate(time.Now().UTC()))
	sum, _ := s.Summary(ctx)
	// 35 sellable shares × $200 count; 50 unvested × $200 don't.
	if sum.TotalValue != 7000 || sum.UnvestedEquityValue != 10000 {
		t.Fatalf("total=%v unvested=%v", sum.TotalValue, sum.UnvestedEquityValue)
	}
	eq, _ := s.Equity(ctx)
	var cancelled EquityGrant
	for _, g := range eq.Grants {
		if g.GrantNumber == "SYN-002" {
			cancelled = g
		}
	}
	if len(cancelled.Vests) != 1 || cancelled.Vests[0].RemainingQty != 0 {
		t.Fatalf("cancelled tranche should have nothing remaining: %+v", cancelled.Vests)
	}
	if len(eq.Upcoming) != 1 || eq.Upcoming[0].Qty != 50 || eq.Upcoming[0].EstValue != 10000 {
		t.Fatalf("upcoming: %+v", eq.Upcoming)
	}

	if err := s.DeleteImport(ctx, im.ID); err != nil {
		t.Fatal(err)
	}
	eq, _ = s.Equity(ctx)
	if len(eq.Grants) != 0 {
		t.Fatalf("grants survived import deletion: %d", len(eq.Grants))
	}
}
