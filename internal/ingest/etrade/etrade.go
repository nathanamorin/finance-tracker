// Package etrade parses E*TRADE (Morgan Stanley at Work) stock plan
// exports. Currently: the Restricted Stock expanded download, an .xlsx
// from Stock Plan → Holdings → Restricted Stock → Download → "Download
// Expanded" (sheet "Restricted Stock", file Restricted_Stock*.xlsx).
//
// The sheet is hierarchical. Column A "Record Type" says what each row is:
//
//   - "Grant": one per award. Uses the grant columns (A–R): Symbol, Grant
//     Date, Granted/Withheld/Vested/Unvested/Sellable/Cancelled Qty.,
//     Est. Market Value, Grant Number, Type (RSU), Status.
//   - "Vest Schedule": one per tranche, linked by Grant Number. Uses the
//     vest columns starting at "Vest Period": Vest Date, Granted Qty.,
//     Cancelled Qty., Vested Qty., Released Qty, Released Amount, Total
//     Taxes Paid, Shares Traded for taxes, Tax Status.
//   - "Totals": a summary row, used only to cross-check the grants.
//   - Other record types (tax withholding, dividends) aren't imported yet;
//     each one is reported once as a warning.
//
// Several column names repeat across those blocks (e.g. "Granted Qty."
// is both the award total and the tranche amount), so columns are found by
// name *within their block*, not globally.
//
// Mapping:
//   - account "E*TRADE Stock Plan" (type stock_plan); the file has no
//     account number;
//   - each Grant → an EquityAward with its tranches. Unvested shares are
//     never counted as holdings or in net worth;
//   - sellable (vested, released) shares → a Holding, so they count in
//     value like any other position;
//   - a share price implied by Est. Market Value ÷ outstanding shares → a
//     Price on the snapshot date, used to value awards and holdings.
//
// Releases are kept on the tranche records only; they're not turned into
// transactions (E*TRADE's brokerage transaction export would be the
// source for those).
//
// The file has no as-of date, so the snapshot uses the upload form's date.
package etrade

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
	"github.com/nathanamorin/finance-tracker/internal/ingest/csvutil"
	"github.com/nathanamorin/finance-tracker/internal/ingest/xlsxutil"
)

// Institution is the institution name stored on accounts from this source.
const Institution = "E*TRADE"

const (
	accountID   = "stock-plan"
	accountName = "E*TRADE Stock Plan"
)

// RestrictedStock is the Restricted Stock expanded-download source.
type RestrictedStock struct{}

var _ ingest.Source = RestrictedStock{}

func (RestrictedStock) ID() string   { return "etrade_restricted_stock_xlsx" }
func (RestrictedStock) Name() string { return "E*TRADE Restricted Stock (RSU, .xlsx)" }
func (RestrictedStock) Description() string {
	return `E*TRADE → Stock Plan → Holdings → Restricted Stock → Download → "Download Expanded" (.xlsx). Imports grants and vesting schedules; unvested shares are tracked separately from net worth. Uses the "as of" date.`
}

// Detect opens the workbook and looks for the Record Type / Grant Number header.
func (RestrictedStock) Detect(filename string, content []byte) bool {
	if !xlsxutil.IsXLSX(content) {
		return false
	}
	sheets, err := xlsxutil.Read(content)
	if err != nil {
		return false
	}
	_, _, _, ok := findHeader(sheets)
	return ok
}

// header resolves column names within a block of the header row.
type header struct {
	row []string
}

func (h header) indexFrom(name string, from int) int {
	for i := from; i < len(h.row); i++ {
		if strings.EqualFold(strings.TrimSpace(h.row[i]), name) {
			return i
		}
	}
	return -1
}

func findHeader(sheets []xlsxutil.Sheet) (sheet xlsxutil.Sheet, h header, headerRow int, ok bool) {
	for _, s := range sheets {
		for i, row := range s.Rows {
			hd := header{row}
			if len(row) > 0 && strings.EqualFold(strings.TrimSpace(row[0]), "Record Type") &&
				hd.indexFrom("Grant Number", 0) >= 0 && hd.indexFrom("Vest Date", 0) >= 0 {
				return s, hd, i, true
			}
		}
	}
	return xlsxutil.Sheet{}, header{}, 0, false
}

// cols are the resolved column indexes (−1 when absent).
type cols struct {
	recordType, symbol, grantDate, granted, withheld, vested, unvested, deferred, sellable,
	estValue, grantNumber, cancelled, awardType, status int
	vestPeriod, vestDate, vGranted, vCancelled, vVested, vReleased, vReleasedAmt, vTaxesPaid,
	vSharesForTaxes, vTaxStatus int
}

func resolve(h header) (cols, error) {
	c := cols{
		recordType:  h.indexFrom("Record Type", 0),
		symbol:      h.indexFrom("Symbol", 0),
		grantDate:   h.indexFrom("Grant Date", 0),
		granted:     h.indexFrom("Granted Qty.", 0),
		withheld:    h.indexFrom("Withheld Qty.", 0),
		vested:      h.indexFrom("Vested Qty.", 0),
		unvested:    h.indexFrom("Unvested Qty.", 0),
		deferred:    h.indexFrom("Deferred / Pending Release Qty.", 0),
		sellable:    h.indexFrom("Sellable Qty.", 0),
		estValue:    h.indexFrom("Est. Market Value", 0),
		grantNumber: h.indexFrom("Grant Number", 0),
		cancelled:   h.indexFrom("Cancelled Qty.", 0),
		awardType:   h.indexFrom("Type", 0),
		status:      h.indexFrom("Status", 0),
		vestPeriod:  h.indexFrom("Vest Period", 0),
	}
	// Vest-block columns: first occurrence at or after "Vest Period".
	vp := c.vestPeriod
	if vp < 0 {
		vp = len(h.row)
	}
	c.vestDate = h.indexFrom("Vest Date", vp)
	c.vGranted = h.indexFrom("Granted Qty.", vp)
	c.vCancelled = h.indexFrom("Cancelled Qty.", vp)
	c.vVested = h.indexFrom("Vested Qty.", vp)
	c.vReleased = h.indexFrom("Released Qty", vp)
	c.vReleasedAmt = h.indexFrom("Released Amount", vp)
	c.vTaxesPaid = h.indexFrom("Total Taxes Paid", vp)
	c.vSharesForTaxes = h.indexFrom("Shares Traded for taxes", vp)
	// "Tax Status" appears twice after Vest Period (AL and AN); tranche rows
	// fill the one after "Shares Traded for taxes".
	if c.vSharesForTaxes >= 0 {
		c.vTaxStatus = h.indexFrom("Tax Status", c.vSharesForTaxes)
	} else {
		c.vTaxStatus = h.indexFrom("Tax Status", vp)
	}

	var missing []string
	for name, idx := range map[string]int{
		"Symbol": c.symbol, "Grant Number": c.grantNumber, "Granted Qty.": c.granted,
		"Unvested Qty.": c.unvested, "Vest Date": c.vestDate,
	} {
		if idx < 0 {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return c, fmt.Errorf("missing expected columns: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func num(row []string, i int) float64 {
	v, _ := csvutil.ParseNumber(cell(row, i))
	return v
}

// date accepts text dates ("12-FEB-2024", "02/16/2027") and Excel serials.
func date(s string) (ingest.Date, error) {
	if s == "" {
		return "", nil
	}
	if d, err := csvutil.ParseDate(s); err == nil {
		return d, nil
	}
	return xlsxutil.SerialDate(s)
}

// Parse implements ingest.Source.
func (RestrictedStock) Parse(r io.Reader, opts ingest.ParseOptions) (*ingest.Result, error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	sheets, err := xlsxutil.Read(content)
	if err != nil {
		return nil, err
	}
	sheet, h, headerRow, ok := findHeader(sheets)
	if !ok {
		return nil, errors.New(`no E*TRADE Restricted Stock sheet found (expected a header row starting with "Record Type" that includes "Grant Number" and "Vest Date")`)
	}
	c, err := resolve(h)
	if err != nil {
		return nil, err
	}

	asOf := opts.AsOf
	if asOf == "" {
		asOf = ingest.NewDate(time.Now())
	}
	res := &ingest.Result{
		Institution:  Institution,
		HoldingsAsOf: asOf,
		Accounts:     []ingest.Account{{ExternalID: accountID, Name: accountName, AccountType: ingest.AccountStockPlan}},
	}
	warn := func(rowIdx int, format string, args ...any) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s row %d: ", sheet.Name, rowIdx+1)+fmt.Sprintf(format, args...))
	}

	awards := map[string]*ingest.EquityAward{}
	var order []string
	var totalsUnvested *float64
	unknownTypes := map[string]bool{}

	for i := headerRow + 1; i < len(sheet.Rows); i++ {
		row := sheet.Rows[i]
		if csvutil.IsBlank(row) {
			continue
		}
		switch rt := cell(row, c.recordType); strings.ToLower(rt) {
		case "grant":
			gn := cell(row, c.grantNumber)
			sym := strings.ToUpper(cell(row, c.symbol))
			if gn == "" || sym == "" {
				warn(i, "grant skipped: missing Grant Number or Symbol")
				continue
			}
			gd, err := date(cell(row, c.grantDate))
			if err != nil {
				warn(i, "grant %s: unreadable Grant Date %q", gn, cell(row, c.grantDate))
			}
			a := &ingest.EquityAward{
				AccountExternalID: accountID,
				Security:          ingest.Security{Symbol: sym, Name: sym, Type: ingest.SecurityStock},
				GrantNumber:       gn,
				AwardType:         firstNonEmpty(cell(row, c.awardType), "RSU"),
				GrantDate:         gd,
				Status:            cell(row, c.status),
				GrantedQty:        num(row, c.granted),
				VestedQty:         num(row, c.vested),
				UnvestedQty:       num(row, c.unvested) + num(row, c.deferred),
				WithheldQty:       num(row, c.withheld),
				SellableQty:       num(row, c.sellable),
				CancelledQty:      num(row, c.cancelled),
				EstMarketValue:    ingest.ToCents(num(row, c.estValue)),
			}
			if _, dup := awards[gn]; dup {
				warn(i, "duplicate grant %s; later row wins", gn)
			} else {
				order = append(order, gn)
			}
			awards[gn] = a

		case "vest schedule":
			gn := cell(row, c.grantNumber)
			a, ok := awards[gn]
			if !ok {
				warn(i, "vest tranche for unknown grant %q skipped", gn)
				continue
			}
			vd, err := date(cell(row, c.vestDate))
			if err != nil || vd == "" {
				warn(i, "grant %s: tranche skipped, unreadable Vest Date %q", gn, cell(row, c.vestDate))
				continue
			}
			period, _ := strconv.Atoi(cell(row, c.vestPeriod))
			a.Vests = append(a.Vests, ingest.EquityVest{
				Period:                 period,
				VestDate:               vd,
				GrantedQty:             num(row, c.vGranted),
				VestedQty:              num(row, c.vVested),
				ReleasedQty:            num(row, c.vReleased),
				CancelledQty:           num(row, c.vCancelled),
				SharesWithheldForTaxes: num(row, c.vSharesForTaxes),
				ReleasedAmount:         ingest.ToCents(num(row, c.vReleasedAmt)),
				TaxesPaid:              ingest.ToCents(num(row, c.vTaxesPaid)),
				TaxStatus:              cell(row, c.vTaxStatus),
			})

		case "totals":
			v := num(row, c.unvested) + num(row, c.deferred)
			totalsUnvested = &v

		default:
			if !unknownTypes[rt] {
				unknownTypes[rt] = true
				warn(i, "record type %q is not imported yet", rt)
			}
		}
	}

	if len(order) == 0 {
		return nil, errors.New("no Grant rows found")
	}

	// Implied price per symbol: Est. Market Value ÷ outstanding shares.
	type agg struct{ value, outstanding, sellable float64 }
	bySym := map[string]*agg{}
	var symOrder []string
	var sumUnvested float64
	for _, gn := range order {
		a := awards[gn]
		res.EquityAwards = append(res.EquityAwards, *a)
		sumUnvested += a.UnvestedQty
		g, ok := bySym[a.Security.Symbol]
		if !ok {
			g = &agg{}
			bySym[a.Security.Symbol] = g
			symOrder = append(symOrder, a.Security.Symbol)
		}
		g.value += a.EstMarketValue.Dollars()
		g.outstanding += a.UnvestedQty + a.SellableQty
		g.sellable += a.SellableQty
	}
	for _, sym := range symOrder {
		g := bySym[sym]
		sec := ingest.Security{Symbol: sym, Name: sym, Type: ingest.SecurityStock}
		var price float64
		if g.outstanding > 0 && g.value > 0 {
			price = math.Round(g.value/g.outstanding*10000) / 10000
			res.Prices = append(res.Prices, ingest.Price{Security: sec, Date: asOf, Price: price})
		}
		if g.sellable > 0 {
			res.Holdings = append(res.Holdings, ingest.Holding{
				AccountExternalID: accountID,
				Security:          sec,
				Quantity:          g.sellable,
				Price:             price,
				MarketValue:       ingest.ToCents(g.sellable * price),
			})
		}
	}

	if totalsUnvested != nil && math.Abs(*totalsUnvested-sumUnvested) > 0.001 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("unvested shares across grants (%.3f) don't match the Totals row (%.3f)", sumUnvested, *totalsUnvested))
	}
	return res, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
