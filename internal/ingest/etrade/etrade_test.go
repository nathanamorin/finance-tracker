package etrade

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
)

// All fixtures in testdata/ are FAKE DATA; see testdata/README.md.
const (
	anonymised = "testdata/etrade_restricted_stock_anonymised.xlsx"
	synthetic  = "testdata/synthetic_vested_and_released.xlsx"
)

func parseFile(t *testing.T, path string) *ingest.Result {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := RestrictedStock{}.Parse(bytes.NewReader(content), ingest.ParseOptions{AsOf: "2026-11-27"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDetect(t *testing.T) {
	for _, p := range []string{anonymised, synthetic} {
		content, _ := os.ReadFile(p)
		if !(RestrictedStock{}).Detect("x.xlsx", content) {
			t.Errorf("%s not detected", p)
		}
	}
	for _, p := range []string{
		"../vanguard/testdata/vanguard_anonymised_export.csv",
		"../benefitscenter/testdata/benefits_center_anonymised_export.csv",
	} {
		content, _ := os.ReadFile(p)
		if (RestrictedStock{}).Detect("x.csv", content) {
			t.Errorf("%s wrongly detected as E*TRADE", p)
		}
	}
}

// testdata/etrade_restricted_stock_anonymised.xlsx: anonymised real export (FAKE DATA).
func TestAnonymisedExport(t *testing.T) {
	res := parseFile(t, anonymised)
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings: %v", res.Warnings)
	}
	if len(res.Accounts) != 1 || res.Accounts[0].AccountType != ingest.AccountStockPlan {
		t.Fatalf("accounts: %+v", res.Accounts)
	}
	if len(res.EquityAwards) != 3 {
		t.Fatalf("awards: %d", len(res.EquityAwards))
	}
	a := res.EquityAwards[0]
	if a.GrantNumber != "G0000001" || a.Security.Symbol != "XYZ" || a.GrantDate != "2024-02-12" ||
		a.AwardType != "RSU" || a.GrantedQty != 12.418 || a.UnvestedQty != 12.418 || a.EstMarketValue != 466323 {
		t.Fatalf("award 1: %+v", a)
	}
	if len(a.Vests) != 1 || a.Vests[0].VestDate != "2027-02-16" || a.Vests[0].GrantedQty != 12.418 ||
		a.Vests[0].Period != 1 || a.Vests[0].TaxStatus != "Due at Vest" {
		t.Fatalf("award 1 vests: %+v", a.Vests)
	}
	if v := res.EquityAwards[2].Vests[0].VestDate; v != "2029-02-16" {
		t.Fatalf("award 3 vest date %s", v)
	}
	// Nothing vested yet: no holdings, but an implied price for valuation.
	if len(res.Holdings) != 0 || len(res.Transactions) != 0 {
		t.Fatalf("holdings=%d transactions=%d", len(res.Holdings), len(res.Transactions))
	}
	if len(res.Prices) != 1 || res.Prices[0].Date != "2026-11-27" {
		t.Fatalf("prices: %+v", res.Prices)
	}
	// 15,488.99 / 41.246 shares
	if p := res.Prices[0].Price; p < 375.5 || p > 375.6 {
		t.Fatalf("implied price %v", p)
	}
}

// testdata/synthetic_vested_and_released.xlsx: generated FAKE DATA.
func TestSyntheticVestedAndReleased(t *testing.T) {
	res := parseFile(t, synthetic)

	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], `"Tax Withholding" is not imported`) {
		t.Fatalf("warnings: %v", res.Warnings)
	}
	if len(res.EquityAwards) != 2 {
		t.Fatalf("awards: %d", len(res.EquityAwards))
	}
	g1 := res.EquityAwards[0]
	if g1.VestedQty != 50 || g1.UnvestedQty != 50 || g1.SellableQty != 35 || g1.WithheldQty != 15 {
		t.Fatalf("grant 1: %+v", g1)
	}
	v1 := g1.Vests[0]
	if v1.VestDate != "2025-02-16" { // Excel serial 45704
		t.Fatalf("serial vest date: %s", v1.VestDate)
	}
	if v1.ReleasedQty != 35 || v1.SharesWithheldForTaxes != 15 || v1.ReleasedAmount != 900000 ||
		v1.TaxesPaid != 300000 || v1.TaxStatus != "Paid" {
		t.Fatalf("tranche 1: %+v", v1)
	}
	if g2 := res.EquityAwards[1]; g2.CancelledQty != 10 || g2.UnvestedQty != 0 || g2.Vests[0].CancelledQty != 10 {
		t.Fatalf("cancelled grant: %+v", g2)
	}
	// Price = 17,000 / (50 unvested + 35 sellable) = 200.
	if len(res.Prices) != 1 || res.Prices[0].Price != 200 {
		t.Fatalf("prices: %+v", res.Prices)
	}
	// Sellable shares are a real holding.
	if len(res.Holdings) != 1 || res.Holdings[0].Quantity != 35 || res.Holdings[0].MarketValue != 700000 {
		t.Fatalf("holdings: %+v", res.Holdings)
	}
}

func TestRejectsOtherFiles(t *testing.T) {
	if _, err := (RestrictedStock{}).Parse(strings.NewReader("Record Type,Grant Number\n"), ingest.ParseOptions{}); err == nil {
		t.Fatal("a CSV must not parse")
	}
}
