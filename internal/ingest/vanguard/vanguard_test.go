package vanguard

import (
	"os"
	"strings"
	"testing"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
)

// parseFixture parses testdata/synthetic_edge_cases.csv, a hand-written
// FAKE DATA file (see testdata/README.md).
func parseFixture(t *testing.T) *ingest.Result {
	t.Helper()
	return parseFile(t, "testdata/synthetic_edge_cases.csv")
}

func parseFile(t *testing.T, path string) *ingest.Result {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := CSV{}.Parse(f, ingest.ParseOptions{AsOf: "2026-11-01"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDetect(t *testing.T) {
	for _, p := range []string{"testdata/synthetic_edge_cases.csv", "testdata/vanguard_anonymised_export.csv"} {
		head, _ := os.ReadFile(p)
		if !(CSV{}).Detect("OfxDownload.csv", head) {
			t.Fatalf("expected %s to be detected", p)
		}
	}
	if (CSV{}).Detect("x.csv", []byte("Date,Description,Amount\n")) {
		t.Fatal("generic bank CSV should not be detected as Vanguard")
	}
}

func TestParsePositions(t *testing.T) {
	res := parseFixture(t)
	if len(res.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(res.Accounts))
	}
	if len(res.Holdings) != 5 {
		t.Fatalf("holdings = %d, want 5", len(res.Holdings))
	}
	if res.HoldingsAsOf != "2026-11-01" {
		t.Fatalf("as-of = %s", res.HoldingsAsOf)
	}
	h := res.Holdings[1]
	if h.Security.Symbol != "VTSAX" || h.Quantity != 215.482 || h.MarketValue != 2824323 {
		t.Fatalf("unexpected holding %+v", h)
	}
	if res.Holdings[0].Security.Type != ingest.SecurityMoneyMarket {
		t.Fatalf("VMFXX type = %s", res.Holdings[0].Security.Type)
	}
	if res.Holdings[4].Security.Type != ingest.SecurityETF {
		t.Fatalf("VOO type = %s", res.Holdings[4].Security.Type)
	}
}

func TestParseTransactions(t *testing.T) {
	res := parseFixture(t)
	if len(res.Transactions) != 11 {
		t.Fatalf("transactions = %d, want 11", len(res.Transactions))
	}
	byDesc := func(i int) ingest.Transaction { return res.Transactions[i] }

	if tx := byDesc(0); tx.Type != ingest.TxContribution || tx.Amount != 200000 {
		t.Fatalf("funds received: %+v", tx)
	}
	if tx := byDesc(1); tx.Type != ingest.TxBuy || tx.Quantity != 7.854 || tx.Amount != -100000 {
		t.Fatalf("buy: %+v", tx)
	}
	if tx := byDesc(2); tx.Type != ingest.TxSweepOut {
		t.Fatalf("sweep: %+v", tx)
	}
	// Two identical reinvestment rows must stay distinct.
	r1, r2 := byDesc(4), byDesc(5)
	if r1.Type != ingest.TxReinvest || r1.Occurrence != 0 || r2.Occurrence != 1 {
		t.Fatalf("reinvest occurrences: %d %d", r1.Occurrence, r2.Occurrence)
	}
	if r1.Fingerprint(Institution) == r2.Fingerprint(Institution) {
		t.Fatal("identical rows must have distinct fingerprints")
	}
	if tx := byDesc(6); tx.Type != ingest.TxSell || tx.Quantity != -20 || tx.Amount != 64200 {
		t.Fatalf("sell: %+v", tx)
	}
	if tx := byDesc(8); tx.Type != ingest.TxConversionIn || tx.TradeDate != "2026-10-15" || tx.SettlementDate != "2026-10-16" {
		t.Fatalf("US-format dates / conversion: %+v", tx)
	}
	if tx := byDesc(9); tx.Amount != -99620 {
		t.Fatalf("parenthesized currency: %+v", tx)
	}
	if tx := byDesc(10); tx.Type != ingest.TxOther || tx.Security != nil || tx.Amount != 123450 {
		t.Fatalf("unknown type: %+v", tx)
	}

	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "Some New Type") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected warning about unknown type, got %v", res.Warnings)
	}
}

func TestFingerprintStableAcrossParses(t *testing.T) {
	a, b := parseFixture(t), parseFixture(t)
	for i := range a.Transactions {
		if a.Transactions[i].Fingerprint(Institution) != b.Transactions[i].Fingerprint(Institution) {
			t.Fatalf("fingerprint %d not stable", i)
		}
	}
}

func TestNoTables(t *testing.T) {
	if _, err := (CSV{}).Parse(strings.NewReader("hello,world\n"), ingest.ParseOptions{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]ingest.TxType{
		"Capital gain (LT)":     ingest.TxCapitalGainLT,
		"Capital gain (ST)":     ingest.TxCapitalGainST,
		"Transfer (outgoing)":   ingest.TxTransferOut,
		"Transfer (incoming)":   ingest.TxTransferIn,
		"Conversion (outgoing)": ingest.TxConversionOut,
		"Withdrawal":            ingest.TxWithdrawal,
		"Sweep in":              ingest.TxSweepIn,
		"Interest":              ingest.TxInterest,
		"Fee":                   ingest.TxFee,
		"Corp Action (Split)":   ingest.TxCorporateAction,
		"Exchange Out":          ingest.TxSell,
	}
	for in, want := range cases {
		if got, _ := classify(in, ""); got != want {
			t.Errorf("classify(%q) = %s, want %s", in, got, want)
		}
	}
}

// The tests below use testdata/vanguard_anonymised_export.csv: an
// anonymised real Vanguard export. It is FAKE DATA (see testdata/README.md).

func TestAnonymisedExportParses(t *testing.T) {
	res := parseFile(t, "testdata/vanguard_anonymised_export.csv")
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}
	if len(res.Accounts) != 2 || len(res.Holdings) != 7 || len(res.Transactions) != 460 {
		t.Fatalf("accounts=%d holdings=%d transactions=%d", len(res.Accounts), len(res.Holdings), len(res.Transactions))
	}
	counts := map[ingest.TxType]int{}
	for _, tx := range res.Transactions {
		counts[tx.Type]++
	}
	want := map[ingest.TxType]int{
		ingest.TxBuy: 156, ingest.TxSweepOut: 79, ingest.TxReinvest: 72, ingest.TxDividend: 72,
		ingest.TxContribution: 40, ingest.TxSweepIn: 39, ingest.TxWithdrawal: 2,
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("%s: got %d, want %d", k, counts[k], v)
		}
	}
}

func TestAnonymisedExportCashAndSweeps(t *testing.T) {
	res := parseFile(t, "testdata/vanguard_anonymised_export.csv")
	for _, tx := range res.Transactions {
		switch tx.Type {
		case ingest.TxContribution, ingest.TxWithdrawal:
			// "CASH" rows carry no security.
			if tx.Security != nil {
				t.Fatalf("cash row has a security: %+v", tx)
			}
		case ingest.TxSweepIn, ingest.TxSweepOut:
			// Sweeps report 0 shares; the money-market share delta is
			// derived from the cash amount at $1/share.
			if tx.Security == nil || tx.Security.Symbol != "VMFXX" {
				t.Fatalf("sweep without VMFXX: %+v", tx)
			}
			if want := -tx.Amount.Dollars(); tx.Quantity != want {
				t.Fatalf("sweep quantity %v, want %v", tx.Quantity, want)
			}
		}
	}
}
