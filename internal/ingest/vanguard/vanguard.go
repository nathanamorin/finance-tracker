// Package vanguard parses the CSV export from Vanguard's "Download center"
// (vanguard.com → My Accounts → Download center → "A spreadsheet-compatible
// CSV file"), conventionally named OfxDownload.csv.
//
// The file holds two independent CSV tables, one after the other:
//
//  1. Current positions (no date — a snapshot as of download time):
//     Account Number,Investment Name,Symbol,Shares,Share Price,Total Value,
//
//  2. Transactions for the selected date range:
//     Account Number,Trade Date,Settlement Date,Transaction Type,
//     Transaction Description,Investment Name,Symbol,Shares,Share Price,
//     Principal Amount,Commissions and Fees,Net Amount,Accrued Interest,
//     Account Type,
//
// Pure cash rows (e.g. "Funds Received") use Investment Name "CASH" and no
// symbol. The format was confirmed against a real (anonymised) export; see
// testdata/README.md.
//
// Vanguard does not publish this format, so the parser locates columns by
// header name rather than position, tolerates trailing commas, blank lines,
// a UTF-8 BOM, "$1,234.56"/"(12.34)" style numbers and both ISO and US date
// formats, and reports anything it can't understand as a warning instead of
// failing the whole import.
package vanguard

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
	"github.com/nathanamorin/finance-tracker/internal/ingest/csvutil"
)

// Institution is the institution name stored on accounts from this source.
const Institution = "Vanguard"

// CSV is the Vanguard Download Center CSV source.
type CSV struct{}

var _ ingest.Source = CSV{}

func (CSV) ID() string   { return "vanguard_csv" }
func (CSV) Name() string { return "Vanguard (CSV download)" }
func (CSV) Description() string {
	return `Vanguard → My Accounts → Download center → choose accounts and a date range → "A spreadsheet-compatible CSV file". Includes current positions and transactions.`
}

// Detect looks for Vanguard's distinctive header rows.
func (CSV) Detect(filename string, content []byte) bool {
	h := strings.ToLower(string(ingest.Head(content)))
	if !strings.Contains(h, "account number") {
		return false
	}
	return (strings.Contains(h, "investment name") && strings.Contains(h, "share price")) ||
		(strings.Contains(h, "trade date") && strings.Contains(h, "transaction type"))
}

type section int

const (
	sectionNone section = iota
	sectionPositions
	sectionTransactions
)

// Parse implements ingest.Source.
func (CSV) Parse(r io.Reader, opts ingest.ParseOptions) (*ingest.Result, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	cr := csv.NewReader(bytes.NewReader(data))
	cr.FieldsPerRecord = -1 // sections have different widths
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true

	res := &ingest.Result{Institution: Institution, HoldingsAsOf: opts.AsOf}
	if res.HoldingsAsOf == "" {
		res.HoldingsAsOf = ingest.NewDate(time.Now())
	}

	accounts := map[string]bool{}
	addAccount := func(id string) {
		if !accounts[id] {
			accounts[id] = true
			res.Accounts = append(res.Accounts, ingest.Account{ExternalID: id, AccountType: ingest.AccountUnknown})
		}
	}
	warn := func(line int, format string, args ...any) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("line %d: ", line)+fmt.Sprintf(format, args...))
	}

	var (
		cur      section
		cols     csvutil.Header
		seen     = map[string]int{} // identical-row counter for Occurrence
		sawTable bool
	)

	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line, _ := cr.FieldPos(0)
		if err != nil {
			warn(line, "unreadable CSV row: %v", err)
			continue
		}
		if csvutil.IsBlank(rec) {
			continue
		}

		if strings.EqualFold(strings.TrimSpace(rec[0]), "Account Number") {
			cols = csvutil.NewHeader(rec)
			switch {
			case cols.Has("trade date", "transaction type"):
				cur = sectionTransactions
			case cols.Has("shares") && (cols.Has("total value") || cols.Has("share price")):
				cur = sectionPositions
			default:
				cur = sectionNone
				warn(line, "unrecognized table header %q; skipping its rows", strings.Join(rec, ","))
			}
			sawTable = sawTable || cur != sectionNone
			continue
		}

		get := func(names ...string) string { return cols.Get(rec, names...) }

		switch cur {
		case sectionPositions:
			acct := get("account number")
			if !looksLikeAccount(acct) {
				// Vanguard sometimes appends footnotes/disclaimers.
				continue
			}
			shares, err := parseNumber(get("shares"))
			if err != nil {
				warn(line, "position skipped: bad Shares %q", get("shares"))
				continue
			}
			price, _ := parseNumber(get("share price"))
			total, err := parseNumber(get("total value"))
			if err != nil {
				total = shares * price
			}
			sec := security(get("symbol"), get("investment name"))
			if sec.Name == "" && sec.Symbol == "" {
				warn(line, "position skipped: no investment name or symbol")
				continue
			}
			addAccount(acct)
			res.Holdings = append(res.Holdings, ingest.Holding{
				AccountExternalID: acct,
				Security:          sec,
				Quantity:          shares,
				Price:             price,
				MarketValue:       ingest.ToCents(total),
			})

		case sectionTransactions:
			acct := get("account number")
			if !looksLikeAccount(acct) {
				continue
			}
			trade, err := parseDate(get("trade date"))
			if err != nil {
				warn(line, "transaction skipped: bad Trade Date %q", get("trade date"))
				continue
			}
			settle, _ := parseDate(get("settlement date"))
			rawType := get("transaction type")
			desc := get("transaction description")
			shares, _ := parseNumber(get("shares"))
			price, _ := parseNumber(get("share price"))
			principal, _ := parseNumber(get("principal amount"))
			// Older exports say "Commission Fees", newer "Commissions and Fees".
			fees, _ := parseNumber(get("commissions and fees", "commission fees"))
			netStr := get("net amount")
			net, err := parseNumber(netStr)
			if err != nil || netStr == "" {
				net = principal - math.Abs(fees)
			}

			txType, known := classify(rawType, desc)
			if !known {
				warn(line, "unrecognized transaction type %q (%q); stored as \"other\"", rawType, desc)
			}

			var sec *ingest.Security
			if s := security(get("symbol"), get("investment name")); s.Symbol != "" || s.Name != "" {
				// Vanguard labels pure cash rows with a placeholder name.
				if !strings.EqualFold(s.Name, "cash") || s.Symbol != "" {
					sec = &s
				}
			}

			tx := ingest.Transaction{
				AccountExternalID: acct,
				Security:          sec,
				TradeDate:         trade,
				SettlementDate:    settle,
				Type:              txType,
				SourceType:        rawType,
				Description:       desc,
				Quantity:          normalizeQuantity(txType, shares, net, sec),
				Price:             price,
				Amount:            ingest.ToCents(net),
				Fees:              ingest.ToCents(math.Abs(fees)),
			}
			key := strings.Join(rec, "\x1f")
			tx.Occurrence = seen[key]
			seen[key]++

			addAccount(acct)
			res.Transactions = append(res.Transactions, tx)
		}
	}

	if !sawTable {
		return nil, errors.New("no Vanguard positions or transactions table found (expected a header row starting with \"Account Number\")")
	}
	return res, nil
}

// classify maps Vanguard's transaction type labels to normalized types.
// Order matters: more specific labels are matched first.
func classify(rawType, desc string) (ingest.TxType, bool) {
	t := strings.ToLower(strings.TrimSpace(rawType))
	d := strings.ToLower(desc)
	switch {
	case strings.Contains(t, "reinvest"):
		return ingest.TxReinvest, true
	case strings.Contains(t, "capital gain") || strings.Contains(t, "cap gain"):
		if strings.Contains(t, "(st)") || strings.Contains(t, "short") {
			return ingest.TxCapitalGainST, true
		}
		return ingest.TxCapitalGainLT, true
	case strings.Contains(t, "dividend"):
		return ingest.TxDividend, true
	case strings.Contains(t, "interest"):
		return ingest.TxInterest, true
	case strings.Contains(t, "sweep in"):
		return ingest.TxSweepIn, true
	case strings.Contains(t, "sweep out"):
		return ingest.TxSweepOut, true
	case strings.Contains(t, "conversion"):
		if strings.Contains(t, "out") {
			return ingest.TxConversionOut, true
		}
		return ingest.TxConversionIn, true
	case strings.Contains(t, "transfer"):
		if strings.Contains(t, "out") || strings.Contains(d, "outgoing") {
			return ingest.TxTransferOut, true
		}
		return ingest.TxTransferIn, true
	case strings.Contains(t, "exchange"):
		if strings.Contains(t, "out") {
			return ingest.TxSell, true
		}
		return ingest.TxBuy, true
	case t == "buy" || strings.HasPrefix(t, "buy") || strings.Contains(t, "purchase"):
		return ingest.TxBuy, true
	case t == "sell" || strings.HasPrefix(t, "sell") || strings.Contains(t, "redemption") && !strings.Contains(t, "corp"):
		return ingest.TxSell, true
	case strings.Contains(t, "funds received") || strings.Contains(t, "contribution") || strings.Contains(t, "deposit"):
		return ingest.TxContribution, true
	case strings.Contains(t, "withdraw") || strings.Contains(t, "distribution") || strings.Contains(t, "disbursement"):
		return ingest.TxWithdrawal, true
	case strings.Contains(t, "withholding") || strings.Contains(t, "tax"):
		return ingest.TxTaxWithholding, true
	case strings.Contains(t, "fee"):
		return ingest.TxFee, true
	case strings.Contains(t, "corp action") || strings.Contains(t, "corporate action") ||
		strings.Contains(t, "split") || strings.Contains(t, "merger") || strings.Contains(t, "spin"):
		return ingest.TxCorporateAction, true
	}
	return ingest.TxOther, false
}

// normalizeQuantity enforces the share-delta sign convention for types
// whose direction is unambiguous; Vanguard is not consistent about signs
// across export versions.
//
// Sweeps into/out of the settlement money-market fund (VMFXX) are reported
// with 0 shares and only a cash amount ("Sweep in" = -$X leaves cash and
// enters the fund). Money-market funds are fixed at $1/share, so the share
// delta is the negated dollar amount; without this the fund's position
// could never be reconstructed from transactions.
func normalizeQuantity(t ingest.TxType, shares, net float64, sec *ingest.Security) float64 {
	switch t {
	case ingest.TxSweepIn, ingest.TxSweepOut:
		if shares == 0 && sec != nil && sec.Type == ingest.SecurityMoneyMarket {
			return math.Round(-net*100) / 100
		}
		return shares
	case ingest.TxBuy, ingest.TxReinvest, ingest.TxTransferIn, ingest.TxConversionIn:
		return math.Abs(shares)
	case ingest.TxSell, ingest.TxTransferOut, ingest.TxConversionOut:
		return -math.Abs(shares)
	}
	return shares
}

func security(symbol, name string) ingest.Security {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	name = strings.Join(strings.Fields(name), " ")
	if symbol == "--" || symbol == "N/A" {
		symbol = ""
	}
	return ingest.Security{Symbol: symbol, Name: name, Type: guessSecurityType(symbol, name)}
}

func guessSecurityType(symbol, name string) ingest.SecurityType {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "money market") || strings.Contains(n, "federal money mkt") ||
		symbol == "VMFXX" || symbol == "VMRXX" || symbol == "VUSXX":
		return ingest.SecurityMoneyMarket
	case strings.Contains(n, " etf"):
		return ingest.SecurityETF
	case strings.Contains(n, "admiral") || strings.Contains(n, "investor sh") || strings.Contains(n, "inv shs") ||
		strings.Contains(n, "fund") || strings.Contains(n, "target retirement") || strings.Contains(n, "trust"):
		return ingest.SecurityMutualFund
	case strings.Contains(n, " cd ") || strings.HasPrefix(n, "cd "):
		return ingest.SecurityCD
	case strings.Contains(n, "treas") || strings.Contains(n, "bond") || strings.Contains(n, " note"):
		return ingest.SecurityBond
	case len(symbol) == 5 && strings.HasSuffix(symbol, "X"):
		return ingest.SecurityMutualFund
	}
	return ingest.SecurityUnknown
}

// looksLikeAccount accepts account identifiers (digits, possibly with
// masking like "xxxx1234" or dashes) and rejects footer text.
func looksLikeAccount(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '-' || r == '*' || r == 'x' || r == 'X' || r == ' ':
		default:
			return false
		}
	}
	return digits >= 3
}

func parseNumber(s string) (float64, error) { return csvutil.ParseNumber(s) }

func parseDate(s string) (ingest.Date, error) { return csvutil.ParseDate(s) }
