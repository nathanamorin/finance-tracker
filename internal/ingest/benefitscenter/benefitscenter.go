// Package benefitscenter parses the 401(k) "Benefits Center" fund summary
// CSV export: one row per fund in the plan, with the current balance, units
// and NAV plus gain/loss and other activity over the selected date range:
//
//	"Plan Name","Date Range","Fund Name","Asset Class","Relative Risk",
//	"Balance","% Invested","Future Investments","Gain/Losses",
//	"Other Activity","Fund Returns","Units","Price (NAV)"
//
// For example:
//
//	"Employer 401(k) Plan - All Account","Jan 01, 2026 - Nov 25, 2026",
//	"U.S Stock Market - Index","Stocks--US Stocks","Moderate","$117,426.62",
//	"100%","100%","+$6,034.83","+$13,426.39","6.28%","2,189.071568","$53.642202"
//
// The file is a summary, not a transaction history, so it produces:
//   - a holdings snapshot dated at the END of the Date Range (not the
//     upload date): Units × Price (NAV) = Balance per fund;
//   - one PeriodPerformance per fund (gain/loss, other activity, return);
//   - a derived starting balance per plan at the START of the range
//     (Balance − Gain/Losses − Other Activity), so value history has a
//     real data point there.
//
// "Other Activity" is never turned into contribution transactions: these
// downloads are usually year-to-date, so successive downloads overlap and
// summing them would double count.
//
// The plan has no account number in the file; the Plan Name is used as the
// account identity. "All Account" exports already combine every money
// source (pre-tax, Roth, match, ...); importing per-source exports as well
// would count the same money twice under different plan names.
package benefitscenter

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
	"github.com/nathanamorin/finance-tracker/internal/ingest/csvutil"
)

// Institution is the institution name stored on accounts from this source.
const Institution = "Benefits Center"

// CSV is the Benefits Center 401(k) fund summary source.
type CSV struct{}

var _ ingest.Source = CSV{}

func (CSV) ID() string   { return "benefits_center_401k_csv" }
func (CSV) Name() string { return "Benefits Center 401(k) (CSV fund summary)" }
func (CSV) Description() string {
	return `401(k) Benefits Center → your plan → investments/balances → download the fund summary as CSV ("Plan Name, Date Range, Fund Name, ... Units, Price (NAV)"). Positions are dated at the end of the file's date range.`
}

var required = []string{"plan name", "date range", "fund name", "balance"}

// Detect looks for the distinctive header row.
func (CSV) Detect(filename string, content []byte) bool {
	h := strings.ToLower(string(ingest.Head(content)))
	for _, col := range required {
		if !strings.Contains(h, col) {
			return false
		}
	}
	return strings.Contains(h, "price (nav)") || strings.Contains(h, "units")
}

// Parse implements ingest.Source.
func (CSV) Parse(r io.Reader, opts ingest.ParseOptions) (*ingest.Result, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	cr := csv.NewReader(bytes.NewReader(data))
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true

	res := &ingest.Result{Institution: Institution}
	warn := func(line int, format string, args ...any) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("line %d: ", line)+fmt.Sprintf(format, args...))
	}

	var cols csvutil.Header
	type planTotals struct {
		start, end      ingest.Date
		ending, gain    ingest.Cents
		other           ingest.Cents
		rangesDisagreed bool
	}
	plans := map[string]*planTotals{}
	var planOrder []string

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
		if cols == nil {
			h := csvutil.NewHeader(rec)
			if h.Has(required...) {
				cols = h
			}
			// Anything before the header (titles, banners) is ignored.
			continue
		}
		get := func(names ...string) string { return cols.Get(rec, names...) }

		plan := get("plan name")
		fund := strings.Join(strings.Fields(get("fund name")), " ")
		if plan == "" || fund == "" || strings.EqualFold(fund, "total") || strings.EqualFold(plan, "total") {
			continue // totals rows and footnotes
		}
		start, end, err := csvutil.ParseDateRange(get("date range"))
		if err != nil {
			warn(line, "row skipped: %v", err)
			continue
		}
		balance, err := csvutil.ParseNumber(get("balance"))
		if err != nil {
			warn(line, "row skipped: bad Balance %q", get("balance"))
			continue
		}
		units, errU := csvutil.ParseNumber(get("units"))
		price, errP := csvutil.ParseNumber(get("price (nav)", "price"))
		if errU != nil || errP != nil {
			warn(line, "bad Units/Price for %q; position recorded by balance only", fund)
			units, price = 0, 0
		}
		gain, errG := csvutil.ParseNumber(get("gain/losses", "gain/loss"))
		other, errO := csvutil.ParseNumber(get("other activity"))
		if errG != nil || errO != nil {
			warn(line, "bad Gain/Losses or Other Activity for %q; treated as 0", fund)
		}

		sec := ingest.Security{
			Name:       fund,
			Type:       ingest.SecurityMutualFund, // plan funds: mutual funds or collective trusts
			AssetClass: get("asset class"),
		}

		pt, ok := plans[plan]
		if !ok {
			pt = &planTotals{start: start, end: end}
			plans[plan] = pt
			planOrder = append(planOrder, plan)
			res.Accounts = append(res.Accounts, ingest.Account{ExternalID: plan, Name: plan, AccountType: ingest.Account401k})
		} else if pt.start != start || pt.end != end {
			pt.rangesDisagreed = true
		}
		pt.ending += ingest.ToCents(balance)
		pt.gain += ingest.ToCents(gain)
		pt.other += ingest.ToCents(other)

		if balance != 0 || units != 0 {
			res.Holdings = append(res.Holdings, ingest.Holding{
				AccountExternalID: plan,
				Security:          sec,
				Quantity:          units,
				Price:             price,
				MarketValue:       ingest.ToCents(balance),
			})
		}

		perf := ingest.PeriodPerformance{
			AccountExternalID: plan,
			Security:          sec,
			PeriodStart:       start,
			PeriodEnd:         end,
			EndingBalance:     ingest.ToCents(balance),
			GainLoss:          ingest.ToCents(gain),
			OtherActivity:     ingest.ToCents(other),
		}
		perf.StartingBalance = perf.EndingBalance - perf.GainLoss - perf.OtherActivity
		// "--" means "not available", which is different from 0%.
		if s := get("fund returns", "fund return"); s != "" && s != "--" && s != "N/A" {
			if v, err := csvutil.ParseNumber(s); err == nil {
				perf.ReturnPct = &v
			}
		}
		res.Performance = append(res.Performance, perf)
	}

	if cols == nil {
		return nil, errors.New(`no fund summary header found (expected columns "Plan Name", "Date Range", "Fund Name", "Balance")`)
	}
	if len(planOrder) == 0 {
		return nil, errors.New("the file has a header but no fund rows")
	}

	// One snapshot date for the whole file: the end of the date range.
	for _, plan := range planOrder {
		pt := plans[plan]
		if res.HoldingsAsOf == "" || pt.end > res.HoldingsAsOf {
			res.HoldingsAsOf = pt.end
		}
		if pt.rangesDisagreed {
			res.Warnings = append(res.Warnings, fmt.Sprintf("plan %q: rows have different date ranges; using %s – %s for its starting balance", plan, pt.start, pt.end))
		}
		res.Balances = append(res.Balances, ingest.Balance{
			AccountExternalID: plan,
			Date:              pt.start,
			Amount:            pt.ending - pt.gain - pt.other,
			Source:            ingest.BalanceDerived,
		})
	}
	// opts.AsOf is deliberately unused: the file's own date range is
	// authoritative (see Description).
	return res, nil
}
