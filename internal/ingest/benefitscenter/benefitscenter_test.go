package benefitscenter

import (
	"os"
	"strings"
	"testing"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
)

// All fixtures in testdata/ are FAKE DATA; see testdata/README.md.

func parseFile(t *testing.T, path string) *ingest.Result {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := CSV{}.Parse(f, ingest.ParseOptions{AsOf: "2030-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDetect(t *testing.T) {
	for _, p := range []string{"testdata/benefits_center_anonymised_export.csv", "testdata/synthetic_multi_fund.csv"} {
		head, _ := os.ReadFile(p)
		if !(CSV{}).Detect("x.csv", head) {
			t.Errorf("%s not detected", p)
		}
	}
	// Must not claim Vanguard's format.
	vanguard, _ := os.ReadFile("../vanguard/testdata/vanguard_anonymised_export.csv")
	if (CSV{}).Detect("OfxDownload.csv", vanguard) {
		t.Error("Vanguard export wrongly detected as Benefits Center")
	}
}

// testdata/benefits_center_anonymised_export.csv: anonymised real export (FAKE DATA).
func TestAnonymisedExport(t *testing.T) {
	res := parseFile(t, "testdata/benefits_center_anonymised_export.csv")
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings: %v", res.Warnings)
	}
	if len(res.Accounts) != 1 || res.Accounts[0].ExternalID != "Employer 401(k) Plan - All Account" ||
		res.Accounts[0].AccountType != ingest.Account401k {
		t.Fatalf("accounts: %+v", res.Accounts)
	}
	// Snapshot date comes from the file, not the upload form.
	if res.HoldingsAsOf != "2026-11-25" {
		t.Fatalf("as-of %s, want 2026-11-25", res.HoldingsAsOf)
	}
	if len(res.Holdings) != 1 {
		t.Fatalf("holdings: %d", len(res.Holdings))
	}
	h := res.Holdings[0]
	if h.Security.Name != "U.S Stock Market - Index" || h.Security.AssetClass != "Stocks--US Stocks" ||
		h.Quantity != 2189.071568 || h.Price != 53.642202 || h.MarketValue != 11742662 {
		t.Fatalf("holding: %+v", h)
	}
	if len(res.Transactions) != 0 {
		t.Fatal("a fund summary must not produce transactions")
	}

	if len(res.Performance) != 1 {
		t.Fatalf("performance: %d", len(res.Performance))
	}
	p := res.Performance[0]
	if p.PeriodStart != "2026-01-01" || p.PeriodEnd != "2026-11-25" || p.GainLoss != 603483 ||
		p.OtherActivity != 1342639 || p.ReturnPct == nil || *p.ReturnPct != 6.28 {
		t.Fatalf("performance: %+v", p)
	}
	// 117,426.62 − 6,034.83 − 13,426.39
	if p.StartingBalance != 9796540 {
		t.Fatalf("starting balance %d", p.StartingBalance)
	}
	if len(res.Balances) != 1 || res.Balances[0].Date != "2026-01-01" || res.Balances[0].Amount != 9796540 ||
		res.Balances[0].Source != ingest.BalanceDerived {
		t.Fatalf("balances: %+v", res.Balances)
	}
}

// testdata/synthetic_multi_fund.csv: hand-written FAKE DATA.
func TestSyntheticMultiFund(t *testing.T) {
	res := parseFile(t, "testdata/synthetic_multi_fund.csv")

	// 4 funds parsed; Total row skipped; bad date range warned.
	if len(res.Performance) != 4 {
		t.Fatalf("performance rows %d, want 4", len(res.Performance))
	}
	// Zero-balance, zero-unit fund has performance but no position.
	if len(res.Holdings) != 3 {
		t.Fatalf("holdings %d, want 3", len(res.Holdings))
	}
	if res.Holdings[0].Security.Name != "Large Cap Index Fund" {
		t.Fatalf("whitespace not normalized: %q", res.Holdings[0].Security.Name)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "date range") {
		t.Fatalf("warnings: %v", res.Warnings)
	}
	if res.Performance[3].ReturnPct != nil || res.Performance[3].OtherActivity != -400000 {
		t.Fatalf("bond fund perf: %+v", res.Performance[3])
	}
	// Plan start = 100,000 − 5,750 − 2,800.
	if len(res.Balances) != 1 || res.Balances[0].Amount != 9145000 || res.Balances[0].Date != "2026-01-01" {
		t.Fatalf("balances: %+v", res.Balances)
	}
	if res.HoldingsAsOf != "2026-10-31" {
		t.Fatalf("as-of %s", res.HoldingsAsOf)
	}
}

func TestRejectsNonSummary(t *testing.T) {
	if _, err := (CSV{}).Parse(strings.NewReader("a,b,c\n1,2,3\n"), ingest.ParseOptions{}); err == nil {
		t.Fatal("expected error")
	}
	hdr := `"Plan Name","Date Range","Fund Name","Balance","Units","Price (NAV)"` + "\n"
	if _, err := (CSV{}).Parse(strings.NewReader(hdr), ingest.ParseOptions{}); err == nil {
		t.Fatal("expected error for header with no rows")
	}
}
