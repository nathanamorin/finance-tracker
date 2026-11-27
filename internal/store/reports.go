package store

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
)

func typeList(types []ingest.TxType) string {
	q := make([]string, len(types))
	for i, t := range types {
		q[i] = "'" + string(t) + "'"
	}
	return strings.Join(q, ",")
}

// ReportOptions tunes what reports count.
type ReportOptions struct {
	// IncludeUnvested counts unvested equity awards (valued at the latest
	// price) in total value, allocations and value history. Off by
	// default: unvested shares aren't owned yet.
	IncludeUnvested bool
}

// AllocationSlice is one bucket of the portfolio.
type AllocationSlice struct {
	Key    string  `json:"key"`
	Label  string  `json:"label"`
	Value  float64 `json:"value"`
	Weight float64 `json:"weight"`
}

// Summary is the dashboard headline data.
type Summary struct {
	TotalValue float64 `json:"total_value"`
	// IncludesUnvested says whether TotalValue and the allocations count
	// unvested equity (see ReportOptions).
	IncludesUnvested        bool    `json:"includes_unvested"`
	AsOf                    *string `json:"as_of"`
	AccountCount            int     `json:"account_count"`
	TransactionCount        int     `json:"transaction_count"`
	NetContributionsAllTime float64 `json:"net_contributions_all_time"`
	NetContributionsYTD     float64 `json:"net_contributions_ytd"`
	IncomeYTD               float64 `json:"income_ytd"`
	IncomeTTM               float64 `json:"income_ttm"`
	LastImportAt            *string `json:"last_import_at"`
	// Unvested employer equity, valued at the latest price. Included in
	// TotalValue only when IncludesUnvested is set.
	UnvestedEquityValue  float64           `json:"unvested_equity_value"`
	UnvestedEquityShares float64           `json:"unvested_equity_shares"`
	NextVest             *UpcomingVest     `json:"next_vest"`
	AllocationByType     []AllocationSlice `json:"allocation_by_type"`
	// AllocationByClass uses the source's asset class where known
	// (e.g. 401k funds), otherwise the security type.
	AllocationByClass    []AllocationSlice `json:"allocation_by_class"`
	AllocationBySecurity []AllocationSlice `json:"allocation_by_security"`
	AllocationByAccount  []AllocationSlice `json:"allocation_by_account"`
}

// Summary computes dashboard headline numbers.
func (s *Store) Summary(ctx context.Context, opts ReportOptions) (*Summary, error) {
	now := time.Now().In(s.loc)
	ytd := fmt.Sprintf("%d-01-01", now.Year())
	ttm := now.AddDate(-1, 0, 0).Format("2006-01-02")
	flows, income := typeList(ingest.ExternalFlowTypes), typeList(ingest.IncomeTypes)

	var sum Summary
	var allTime, ytdFlows, ytdIncome, ttmIncome int64
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT count(*) FROM accounts WHERE closed = 0),
			(SELECT count(*) FROM transactions),
			COALESCE(SUM(CASE WHEN type IN (`+flows+`) THEN amount_cents END), 0),
			COALESCE(SUM(CASE WHEN type IN (`+flows+`) AND trade_date >= ? THEN amount_cents END), 0),
			COALESCE(SUM(CASE WHEN type IN (`+income+`) AND trade_date >= ? THEN amount_cents END), 0),
			COALESCE(SUM(CASE WHEN type IN (`+income+`) AND trade_date > ? THEN amount_cents END), 0),
			(SELECT max(imported_at) FROM imports)
		FROM transactions`, ytd, ytd, ttm).
		Scan(&sum.AccountCount, &sum.TransactionCount, &allTime, &ytdFlows, &ytdIncome, &ttmIncome, &sum.LastImportAt)
	if err != nil {
		return nil, err
	}
	sum.NetContributionsAllTime = ingest.Cents(allTime).Dollars()
	sum.NetContributionsYTD = ingest.Cents(ytdFlows).Dollars()
	sum.IncomeYTD = ingest.Cents(ytdIncome).Dollars()
	sum.IncomeTTM = ingest.Cents(ttmIncome).Dollars()

	holdings, err := s.LatestHoldings(ctx)
	if err != nil {
		return nil, err
	}
	if opts.IncludeUnvested {
		unvested, err := s.UnvestedHoldings(ctx)
		if err != nil {
			return nil, err
		}
		holdings = append(holdings, unvested...)
		sum.IncludesUnvested = true
	}
	byType, bySec, byAcct, byClass := map[string]float64{}, map[string]float64{}, map[string]float64{}, map[string]float64{}
	secLabel := map[string]string{}
	for _, h := range holdings {
		sum.TotalValue += h.MarketValue
		byType[h.SecurityType] += h.MarketValue
		if h.AssetClass != nil && *h.AssetClass != "" {
			byClass[*h.AssetClass] += h.MarketValue
		} else {
			byClass[h.SecurityType] += h.MarketValue
		}
		key := h.SecurityName
		if h.Symbol != nil {
			key = *h.Symbol
		}
		bySec[key] += h.MarketValue
		secLabel[key] = h.SecurityName
		byAcct[h.AccountName] += h.MarketValue
		if sum.AsOf == nil || h.AsOfDate > *sum.AsOf {
			d := h.AsOfDate
			sum.AsOf = &d
		}
	}
	sum.TotalValue = math.Round(sum.TotalValue*100) / 100
	sum.AllocationByType = slices(byType, nil, sum.TotalValue)
	sum.AllocationByClass = slices(byClass, nil, sum.TotalValue)
	sum.AllocationBySecurity = slices(bySec, secLabel, sum.TotalValue)
	sum.AllocationByAccount = slices(byAcct, nil, sum.TotalValue)

	eq, err := s.Equity(ctx)
	if err != nil {
		return nil, err
	}
	sum.UnvestedEquityValue, sum.UnvestedEquityShares = eq.UnvestedValue, eq.UnvestedShares
	if len(eq.Upcoming) > 0 {
		next := eq.Upcoming[0]
		sum.NextVest = &next
	}
	return &sum, nil
}

func slices(m map[string]float64, labels map[string]string, total float64) []AllocationSlice {
	out := make([]AllocationSlice, 0, len(m))
	for k, v := range m {
		if v == 0 {
			continue
		}
		label := k
		if labels != nil && labels[k] != "" {
			label = labels[k]
		}
		w := 0.0
		if total != 0 {
			w = v / total
		}
		out = append(out, AllocationSlice{Key: k, Label: label, Value: math.Round(v*100) / 100, Weight: w})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	return out
}

// ValuePoint is one point of the reconstructed value history.
type ValuePoint struct {
	Date             string  `json:"date"`
	Value            float64 `json:"value"`
	NetContributions float64 `json:"net_contributions"`
	Income           float64 `json:"income"`
}

// SnapshotPoint is a total value actually reported by a source on a date.
type SnapshotPoint struct {
	Date  string  `json:"date"`
	Value float64 `json:"value"`
}

// ValueHistory is portfolio value over time.
type ValueHistory struct {
	Interval  string          `json:"interval"`
	Points    []ValuePoint    `json:"points"`
	Snapshots []SnapshotPoint `json:"snapshots"`
	// UnpricedSecurities lists securities held at some point with no known
	// price; they are valued at 0 and make the history an underestimate.
	UnpricedSecurities []string `json:"unpriced_securities"`
}

// ValueHistory reconstructs portfolio value at each period end.
//
// Positions come from transactions, anchored to each account's latest
// holdings snapshot: quantity(d) = snapshotQty + Σdeltas(≤d) − Σdeltas(≤snapshotDate).
// That way positions held before the earliest imported transaction are
// still counted, and the reconstruction always agrees with the most recent
// snapshot. Accounts with no snapshot start from zero. Each position is
// valued at the latest known price on or before d (prices come from trade
// prices and snapshots, so values between trades are approximations until
// a market-data source is added).
//
// Accounts with no transactions at all (e.g. 401k fund summaries, or bank
// sources that only report balances) can't be reconstructed. They use
// their recorded balances instead, carried forward from each balance date,
// and contribute nothing before their first balance.
func (s *Store) ValueHistory(ctx context.Context, interval string, accountID int64, opts ReportOptions) (*ValueHistory, error) {
	if interval == "" {
		interval = "month"
	}
	if interval != "day" && interval != "week" && interval != "month" {
		return nil, fmt.Errorf("%w: interval must be day, week or month", ErrInvalid)
	}
	acctFilter, args := "", []any{}
	if accountID != 0 {
		acctFilter, args = " AND account_id = ?", []any{accountID}
	}

	type posKey struct{ acct, sec int64 }
	type delta struct {
		date string
		qty  float64
	}
	type flow struct {
		date           string
		contrib, incom int64
	}

	// Transactions.
	deltas := map[posKey][]delta{}
	var flows []flow
	external, income := map[string]bool{}, map[string]bool{}
	for _, t := range ingest.ExternalFlowTypes {
		external[string(t)] = true
	}
	for _, t := range ingest.IncomeTypes {
		income[string(t)] = true
	}
	start := ""
	rows, err := s.db.QueryContext(ctx, `
		SELECT account_id, security_id, trade_date, type, quantity, amount_cents
		FROM transactions WHERE 1=1`+acctFilter+` ORDER BY trade_date, id`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var acct int64
		var sec *int64
		var date, typ string
		var qty float64
		var amt int64
		if err := rows.Scan(&acct, &sec, &date, &typ, &qty, &amt); err != nil {
			rows.Close()
			return nil, err
		}
		if start == "" || date < start {
			start = date
		}
		if sec != nil && qty != 0 {
			k := posKey{acct, *sec}
			deltas[k] = append(deltas[k], delta{date, qty})
		}
		if external[typ] {
			flows = append(flows, flow{date: date, contrib: amt})
		} else if income[typ] {
			flows = append(flows, flow{date: date, incom: amt})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	txAccounts := map[int64]bool{}
	for k := range deltas {
		txAccounts[k.acct] = true
	}
	rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT account_id FROM transactions WHERE 1=1`+acctFilter, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		txAccounts[id] = true
	}
	rows.Close()

	// Balance series for accounts without transactions.
	balanceSeries := map[int64][]pricePoint{}
	rows, err = s.db.QueryContext(ctx, `
		SELECT b.account_id, b.as_of_date, b.balance_cents FROM account_balances b
		JOIN accounts a ON a.id = b.account_id WHERE a.closed = 0`+strings.ReplaceAll(acctFilter, "account_id", "b.account_id")+`
		ORDER BY b.as_of_date`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var acct, cents int64
		var date string
		if err := rows.Scan(&acct, &date, &cents); err != nil {
			rows.Close()
			return nil, err
		}
		if !txAccounts[acct] {
			balanceSeries[acct] = append(balanceSeries[acct], pricePoint{date, ingest.Cents(cents).Dollars()})
			if start == "" || date < start {
				start = date
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Anchors: each account's latest snapshot.
	anchorDate := map[int64]string{}
	anchorQty := map[posKey]float64{}
	rows, err = s.db.QueryContext(ctx, `
		SELECT account_id, security_id, as_of_date, quantity FROM holdings h
		WHERE as_of_date = (SELECT max(as_of_date) FROM holdings WHERE account_id = h.account_id)`+acctFilter, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k posKey
		var date string
		var qty float64
		if err := rows.Scan(&k.acct, &k.sec, &date, &qty); err != nil {
			rows.Close()
			return nil, err
		}
		if !txAccounts[k.acct] {
			continue // valued from balances instead
		}
		anchorDate[k.acct] = date
		anchorQty[k] += qty
		if _, ok := deltas[k]; !ok {
			deltas[k] = nil
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Snapshot totals (forward-filled across accounts).
	snapshots, firstSnap, err := s.snapshotSeries(ctx, acctFilter, args)
	if err != nil {
		return nil, err
	}
	if firstSnap != "" && (start == "" || firstSnap < start) {
		start = firstSnap
	}
	out := &ValueHistory{Interval: interval, Points: []ValuePoint{}, Snapshots: snapshots, UnpricedSecurities: []string{}}
	if start == "" && opts.IncludeUnvested {
		// Only equity awards: start the history at the earliest grant. (With
		// other data, the start isn't moved earlier, so unvested awards can't
		// stretch other accounts' reconstructions back past their data.)
		var first *string
		if err := s.db.QueryRowContext(ctx, `
			SELECT min(g.grant_date) FROM equity_grants g JOIN accounts a ON a.id = g.account_id
			WHERE a.closed = 0 AND g.grant_date IS NOT NULL`+strings.ReplaceAll(acctFilter, "account_id", "g.account_id"), args...).Scan(&first); err != nil {
			return nil, err
		}
		if first != nil {
			start = *first
		}
	}
	if start == "" {
		return out, nil
	}

	prices, secInfo, err := s.priceTable(ctx)
	if err != nil {
		return nil, err
	}

	today := time.Now().In(s.loc).Format("2006-01-02")
	dates := periodEnds(start, today, interval)

	// Per position, precompute cumulative deltas at the anchor date.
	type posState struct {
		key      posKey
		deltas   []delta
		idx      int
		cum      float64
		baseline float64 // anchorQty - cum(anchorDate)
	}
	states := make([]*posState, 0, len(deltas))
	for k, ds := range deltas {
		st := &posState{key: k, deltas: ds}
		if ad, ok := anchorDate[k.acct]; ok {
			var cumAtAnchor float64
			for _, d := range ds {
				if d.date <= ad {
					cumAtAnchor += d.qty
				}
			}
			st.baseline = anchorQty[k] - cumAtAnchor
		}
		states = append(states, st)
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].key.acct != states[j].key.acct {
			return states[i].key.acct < states[j].key.acct
		}
		return states[i].key.sec < states[j].key.sec
	})

	// Unvested tranches (optional): each counts from its grant date until
	// its vest date, net of cancelled shares, valued like any position.
	type tranche struct {
		sec         int64
		from, until string
		qty         float64
	}
	var tranches []tranche
	if opts.IncludeUnvested {
		trows, err := s.db.QueryContext(ctx, `
			SELECT g.security_id, COALESCE(g.grant_date, ''), v.vest_date, v.granted_qty - v.cancelled_qty
			FROM equity_vests v
			JOIN equity_grants g ON g.id = v.grant_id
			JOIN accounts a ON a.id = g.account_id
			WHERE a.closed = 0 AND v.granted_qty - v.cancelled_qty > 0`+strings.ReplaceAll(acctFilter, "account_id", "g.account_id"), args...)
		if err != nil {
			return nil, err
		}
		for trows.Next() {
			var t tranche
			if err := trows.Scan(&t.sec, &t.from, &t.until, &t.qty); err != nil {
				trows.Close()
				return nil, err
			}
			tranches = append(tranches, t)
		}
		trows.Close()
		if err := trows.Err(); err != nil {
			return nil, err
		}
	}

	unpriced := map[int64]bool{}
	fi := 0
	var contrib, incomeSum int64
	for _, d := range dates {
		for fi < len(flows) && flows[fi].date <= d {
			contrib += flows[fi].contrib
			incomeSum += flows[fi].incom
			fi++
		}
		var value float64
		for _, st := range states {
			for st.idx < len(st.deltas) && st.deltas[st.idx].date <= d {
				st.cum += st.deltas[st.idx].qty
				st.idx++
			}
			qty := st.baseline + st.cum
			if qty < 1e-6 {
				continue // closed position (or a small negative from data gaps)
			}
			p, ok := priceAt(prices[st.key.sec], d)
			if !ok {
				if secInfo[st.key.sec].moneyMarket {
					p = 1
				} else {
					unpriced[st.key.sec] = true
					continue
				}
			}
			value += qty * p
		}
		for _, series := range balanceSeries {
			value += balanceAt(series, d)
		}
		for _, t := range tranches {
			if t.from <= d && d < t.until {
				if p, ok := priceAt(prices[t.sec], d); ok {
					value += t.qty * p
				} else {
					unpriced[t.sec] = true
				}
			}
		}
		out.Points = append(out.Points, ValuePoint{
			Date:             d,
			Value:            math.Round(value*100) / 100,
			NetContributions: ingest.Cents(contrib).Dollars(),
			Income:           ingest.Cents(incomeSum).Dollars(),
		})
	}
	for id := range unpriced {
		out.UnpricedSecurities = append(out.UnpricedSecurities, secInfo[id].label)
	}
	sort.Strings(out.UnpricedSecurities)
	return out, nil
}

type pricePoint struct {
	date  string
	price float64
}

type secMeta struct {
	label       string
	moneyMarket bool
}

func (s *Store) priceTable(ctx context.Context) (map[int64][]pricePoint, map[int64]secMeta, error) {
	prices := map[int64][]pricePoint{}
	rows, err := s.db.QueryContext(ctx, `SELECT security_id, date, price FROM prices ORDER BY security_id, date`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id int64
		var pp pricePoint
		if err := rows.Scan(&id, &pp.date, &pp.price); err != nil {
			rows.Close()
			return nil, nil, err
		}
		prices[id] = append(prices[id], pp)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	info := map[int64]secMeta{}
	rows, err = s.db.QueryContext(ctx, `SELECT id, COALESCE(symbol, name), security_type FROM securities`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var label, typ string
		if err := rows.Scan(&id, &label, &typ); err != nil {
			return nil, nil, err
		}
		info[id] = secMeta{label: label, moneyMarket: typ == string(ingest.SecurityMoneyMarket) || typ == string(ingest.SecurityCash)}
	}
	return prices, info, rows.Err()
}

// priceAt returns the latest price on or before date, falling back to the
// earliest known price for dates before any observation.
func priceAt(pts []pricePoint, date string) (float64, bool) {
	if len(pts) == 0 {
		return 0, false
	}
	i := sort.Search(len(pts), func(i int) bool { return pts[i].date > date })
	if i == 0 {
		return pts[0].price, true
	}
	return pts[i-1].price, true
}

// balanceAt returns the latest balance on or before date, or 0 before the
// first one (no backfill: the account isn't known to exist yet).
func balanceAt(pts []pricePoint, date string) float64 {
	i := sort.Search(len(pts), func(i int) bool { return pts[i].date > date })
	if i == 0 {
		return 0
	}
	return pts[i-1].price
}

func (s *Store) snapshotSeries(ctx context.Context, acctFilter string, args []any) ([]SnapshotPoint, string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.account_id, b.as_of_date, b.balance_cents FROM account_balances b
		JOIN accounts a ON a.id = b.account_id WHERE a.closed = 0`+strings.ReplaceAll(acctFilter, "account_id", "b.account_id")+`
		ORDER BY b.as_of_date`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	latest := map[int64]int64{}
	var out []SnapshotPoint
	first := ""
	for rows.Next() {
		var acct, bal int64
		var date string
		if err := rows.Scan(&acct, &date, &bal); err != nil {
			return nil, "", err
		}
		if first == "" {
			first = date
		}
		latest[acct] = bal
		var total int64
		for _, v := range latest {
			total += v
		}
		if n := len(out); n > 0 && out[n-1].Date == date {
			out[n-1].Value = ingest.Cents(total).Dollars()
		} else {
			out = append(out, SnapshotPoint{Date: date, Value: ingest.Cents(total).Dollars()})
		}
	}
	if out == nil {
		out = []SnapshotPoint{}
	}
	return out, first, rows.Err()
}

// periodEnds lists the last day of each period from start to end
// (inclusive), always ending with end itself.
func periodEnds(start, end, interval string) []string {
	s, err1 := time.Parse("2006-01-02", start)
	e, err2 := time.Parse("2006-01-02", end)
	if err1 != nil || err2 != nil || s.After(e) {
		return []string{end}
	}
	var out []string
	switch interval {
	case "day":
		for d := s; !d.After(e); d = d.AddDate(0, 0, 1) {
			out = append(out, d.Format("2006-01-02"))
		}
		return out
	case "week":
		// Weeks end on Sunday.
		d := s.AddDate(0, 0, (7-int(s.Weekday()))%7)
		for ; d.Before(e); d = d.AddDate(0, 0, 7) {
			out = append(out, d.Format("2006-01-02"))
		}
	default: // month
		d := time.Date(s.Year(), s.Month()+1, 0, 0, 0, 0, 0, time.UTC)
		for d.Before(e) {
			out = append(out, d.Format("2006-01-02"))
			d = time.Date(d.Year(), d.Month()+2, 0, 0, 0, 0, 0, time.UTC)
		}
	}
	return append(out, end)
}

// CashFlowRow aggregates money movements for one period.
type CashFlowRow struct {
	Period        string  `json:"period"`
	Contributions float64 `json:"contributions"`
	Withdrawals   float64 `json:"withdrawals"`
	Dividends     float64 `json:"dividends"`
	Interest      float64 `json:"interest"`
	CapitalGains  float64 `json:"capital_gains"`
	Fees          float64 `json:"fees"`
	Taxes         float64 `json:"taxes"`
}

// CashFlow groups external flows, income and costs by month or year.
// Withdrawals, fees and taxes are reported as positive numbers.
func (s *Store) CashFlow(ctx context.Context, interval string, accountID int64) ([]CashFlowRow, error) {
	var period string
	switch interval {
	case "", "month":
		period = "substr(trade_date, 1, 7)"
	case "year":
		period = "substr(trade_date, 1, 4)"
	default:
		return nil, fmt.Errorf("%w: interval must be month or year", ErrInvalid)
	}
	where, args := "", []any{}
	if accountID != 0 {
		where, args = " WHERE account_id = ?", []any{accountID}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+period+` AS p,
			SUM(CASE WHEN type IN ('contribution','transfer_in') THEN amount_cents ELSE 0 END),
			SUM(CASE WHEN type IN ('withdrawal','transfer_out') THEN -amount_cents ELSE 0 END),
			SUM(CASE WHEN type = 'dividend' THEN amount_cents ELSE 0 END),
			SUM(CASE WHEN type = 'interest' THEN amount_cents ELSE 0 END),
			SUM(CASE WHEN type IN ('capital_gain_lt','capital_gain_st') THEN amount_cents ELSE 0 END),
			SUM(CASE WHEN type = 'fee' THEN -amount_cents ELSE 0 END) + SUM(fees_cents),
			SUM(CASE WHEN type = 'tax_withholding' THEN -amount_cents ELSE 0 END)
		FROM transactions`+where+` GROUP BY p ORDER BY p`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CashFlowRow{}
	for rows.Next() {
		var r CashFlowRow
		var c, w, d, i, g, f, t int64
		if err := rows.Scan(&r.Period, &c, &w, &d, &i, &g, &f, &t); err != nil {
			return nil, err
		}
		r.Contributions, r.Withdrawals = ingest.Cents(c).Dollars(), ingest.Cents(w).Dollars()
		r.Dividends, r.Interest, r.CapitalGains = ingest.Cents(d).Dollars(), ingest.Cents(i).Dollars(), ingest.Cents(g).Dollars()
		r.Fees, r.Taxes = ingest.Cents(f).Dollars(), ingest.Cents(t).Dollars()
		out = append(out, r)
	}
	return out, rows.Err()
}
