package store

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
)

// EquityVest is one vesting tranche, valued at the security's latest price.
type EquityVest struct {
	Period                 int     `json:"period"`
	VestDate               string  `json:"vest_date"`
	GrantedQty             float64 `json:"granted_qty"`
	VestedQty              float64 `json:"vested_qty"`
	ReleasedQty            float64 `json:"released_qty"`
	CancelledQty           float64 `json:"cancelled_qty"`
	SharesWithheldForTaxes float64 `json:"shares_withheld_for_taxes"`
	ReleasedAmount         float64 `json:"released_amount"`
	TaxesPaid              float64 `json:"taxes_paid"`
	TaxStatus              *string `json:"tax_status"`
	// RemainingQty is still to vest: granted − vested − cancelled.
	RemainingQty float64 `json:"remaining_qty"`
	// EstValue is RemainingQty × latest price (pre-tax).
	EstValue float64 `json:"est_value"`
}

// EquityGrant is an award with its tranches.
type EquityGrant struct {
	ID            int64        `json:"id"`
	AccountID     int64        `json:"account_id"`
	AccountName   string       `json:"account_name"`
	Symbol        string       `json:"symbol"`
	GrantNumber   string       `json:"grant_number"`
	AwardType     string       `json:"award_type"`
	GrantDate     *string      `json:"grant_date"`
	Status        *string      `json:"status"`
	GrantedQty    float64      `json:"granted_qty"`
	VestedQty     float64      `json:"vested_qty"`
	UnvestedQty   float64      `json:"unvested_qty"`
	WithheldQty   float64      `json:"withheld_qty"`
	SellableQty   float64      `json:"sellable_qty"`
	CancelledQty  float64      `json:"cancelled_qty"`
	AsOfDate      string       `json:"as_of_date"`
	Price         *float64     `json:"price"`
	PriceDate     *string      `json:"price_date"`
	UnvestedValue float64      `json:"unvested_value"`
	Vests         []EquityVest `json:"vests"`
}

// UpcomingVest is a future tranche, for the vesting schedule.
type UpcomingVest struct {
	VestDate    string  `json:"vest_date"`
	Symbol      string  `json:"symbol"`
	GrantNumber string  `json:"grant_number"`
	Qty         float64 `json:"qty"`
	EstValue    float64 `json:"est_value"`
}

// Equity summarizes employer equity. Unvested value is shown on its own:
// it is not part of total value or net worth.
type Equity struct {
	Grants         []EquityGrant  `json:"grants"`
	UnvestedShares float64        `json:"unvested_shares"`
	UnvestedValue  float64        `json:"unvested_value"`
	Upcoming       []UpcomingVest `json:"upcoming"`
}

// Equity returns all grants with tranches, valued at each security's
// latest known price.
func (s *Store) Equity(ctx context.Context) (*Equity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.id, g.account_id, a.name, s.symbol, s.name, g.grant_number, g.award_type, g.grant_date, g.status,
			g.granted_qty, g.vested_qty, g.unvested_qty, g.withheld_qty, g.sellable_qty, g.cancelled_qty, g.as_of_date,
			p.price, p.date
		FROM equity_grants g
		JOIN accounts a ON a.id = g.account_id
		JOIN securities s ON s.id = g.security_id
		LEFT JOIN prices p ON p.security_id = g.security_id
			AND p.date = (SELECT max(date) FROM prices WHERE security_id = g.security_id)
		WHERE a.closed = 0
		ORDER BY g.grant_date, g.grant_number`)
	if err != nil {
		return nil, err
	}
	out := &Equity{Grants: []EquityGrant{}, Upcoming: []UpcomingVest{}}
	byID := map[int64]*EquityGrant{}
	for rows.Next() {
		var g EquityGrant
		var sym *string
		var name string
		if err := rows.Scan(&g.ID, &g.AccountID, &g.AccountName, &sym, &name, &g.GrantNumber, &g.AwardType,
			&g.GrantDate, &g.Status, &g.GrantedQty, &g.VestedQty, &g.UnvestedQty, &g.WithheldQty, &g.SellableQty,
			&g.CancelledQty, &g.AsOfDate, &g.Price, &g.PriceDate); err != nil {
			rows.Close()
			return nil, err
		}
		g.Symbol = name
		if sym != nil {
			g.Symbol = *sym
		}
		if g.Price != nil {
			g.UnvestedValue = round2(g.UnvestedQty * *g.Price)
		}
		g.Vests = []EquityVest{}
		out.Grants = append(out.Grants, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out.Grants {
		byID[out.Grants[i].ID] = &out.Grants[i]
		out.UnvestedShares += out.Grants[i].UnvestedQty
		out.UnvestedValue += out.Grants[i].UnvestedValue
	}
	out.UnvestedValue = round2(out.UnvestedValue)

	vrows, err := s.db.QueryContext(ctx, `
		SELECT grant_id, COALESCE(vest_period, 0), vest_date, granted_qty, vested_qty, released_qty, cancelled_qty,
			shares_withheld_for_taxes, released_amount_cents, taxes_paid_cents, tax_status
		FROM equity_vests ORDER BY vest_date, vest_period`)
	if err != nil {
		return nil, err
	}
	defer vrows.Close()
	today := time.Now().In(s.loc).Format("2006-01-02")
	for vrows.Next() {
		var gid int64
		var v EquityVest
		var released, taxes int64
		if err := vrows.Scan(&gid, &v.Period, &v.VestDate, &v.GrantedQty, &v.VestedQty, &v.ReleasedQty, &v.CancelledQty,
			&v.SharesWithheldForTaxes, &released, &taxes, &v.TaxStatus); err != nil {
			return nil, err
		}
		g, ok := byID[gid]
		if !ok {
			continue // grant on a closed account
		}
		v.ReleasedAmount, v.TaxesPaid = ingest.Cents(released).Dollars(), ingest.Cents(taxes).Dollars()
		v.RemainingQty = math.Max(0, round6(v.GrantedQty-v.VestedQty-v.CancelledQty))
		if g.Price != nil {
			v.EstValue = round2(v.RemainingQty * *g.Price)
		}
		g.Vests = append(g.Vests, v)
		if v.RemainingQty > 0 && v.VestDate >= today {
			out.Upcoming = append(out.Upcoming, UpcomingVest{
				VestDate: v.VestDate, Symbol: g.Symbol, GrantNumber: g.GrantNumber, Qty: v.RemainingQty, EstValue: v.EstValue,
			})
		}
	}
	sort.SliceStable(out.Upcoming, func(i, j int) bool { return out.Upcoming[i].VestDate < out.Upcoming[j].VestDate })
	return out, vrows.Err()
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }
