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
