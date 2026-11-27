package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
)

// Account is an account with its latest known balance.
type Account struct {
	ID               int64   `json:"id"`
	Institution      string  `json:"institution"`
	ExternalIDMasked string  `json:"external_id_masked"`
	Name             string  `json:"name"`
	AccountType      string  `json:"account_type"`
	Currency         string  `json:"currency"`
	Closed           bool    `json:"closed"`
	Balance          float64 `json:"balance"`
	BalanceAsOf      *string `json:"balance_as_of"`
	TransactionCount int     `json:"transaction_count"`
	FirstTransaction *string `json:"first_transaction"`
	LastTransaction  *string `json:"last_transaction"`
}

// ListAccounts returns all accounts.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.institution, a.external_id, a.name, a.account_type, a.currency, a.closed,
			COALESCE(b.balance_cents, 0), b.as_of_date,
			(SELECT count(*) FROM transactions t WHERE t.account_id = a.id),
			(SELECT min(trade_date) FROM transactions t WHERE t.account_id = a.id),
			(SELECT max(trade_date) FROM transactions t WHERE t.account_id = a.id)
		FROM accounts a
		LEFT JOIN account_balances b ON b.account_id = a.id
			AND b.as_of_date = (SELECT max(as_of_date) FROM account_balances WHERE account_id = a.id)
		ORDER BY a.closed, COALESCE(b.balance_cents, 0) DESC, a.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		var ext string
		var bal int64
		if err := rows.Scan(&a.ID, &a.Institution, &ext, &a.Name, &a.AccountType, &a.Currency, &a.Closed,
			&bal, &a.BalanceAsOf, &a.TransactionCount, &a.FirstTransaction, &a.LastTransaction); err != nil {
			return nil, err
		}
		a.ExternalIDMasked = "••" + last4(ext)
		a.Balance = ingest.Cents(bal).Dollars()
		out = append(out, a)
	}
	return out, rows.Err()
}

// AccountUpdate holds editable account fields; nil means unchanged.
type AccountUpdate struct {
	Name        *string `json:"name"`
	AccountType *string `json:"account_type"`
	Closed      *bool   `json:"closed"`
}

// UpdateAccount applies an AccountUpdate.
func (s *Store) UpdateAccount(ctx context.Context, id int64, u AccountUpdate) error {
	var sets []string
	var args []any
	if u.Name != nil {
		name := strings.TrimSpace(*u.Name)
		if name == "" || len(name) > 100 {
			return fmt.Errorf("%w: name must be 1-100 characters", ErrInvalid)
		}
		sets, args = append(sets, "name = ?"), append(args, name)
	}
	if u.AccountType != nil {
		if !validAccountType(*u.AccountType) {
			return fmt.Errorf("%w: unknown account type %q", ErrInvalid, *u.AccountType)
		}
		sets, args = append(sets, "account_type = ?"), append(args, *u.AccountType)
	}
	if u.Closed != nil {
		sets, args = append(sets, "closed = ?"), append(args, *u.Closed)
	}
	if len(sets) == 0 {
		return nil
	}
	r, err := s.db.ExecContext(ctx, `UPDATE accounts SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ErrInvalid marks bad user input.
var ErrInvalid = fmt.Errorf("invalid input")

func validAccountType(t string) bool {
	for _, v := range ingest.AccountTypes {
		if string(v) == t {
			return true
		}
	}
	return false
}

// Transaction is a transaction row joined with account/security names.
type Transaction struct {
	ID             int64    `json:"id"`
	AccountID      int64    `json:"account_id"`
	AccountName    string   `json:"account_name"`
	SecurityID     *int64   `json:"security_id"`
	Symbol         *string  `json:"symbol"`
	SecurityName   *string  `json:"security_name"`
	TradeDate      string   `json:"trade_date"`
	SettlementDate *string  `json:"settlement_date"`
	Type           string   `json:"type"`
	SourceType     string   `json:"source_type"`
	Description    string   `json:"description"`
	Quantity       float64  `json:"quantity"`
	Price          *float64 `json:"price"`
	Amount         float64  `json:"amount"`
	Fees           float64  `json:"fees"`
	ImportID       *int64   `json:"import_id"`
}

// TransactionFilter narrows ListTransactions. Zero values mean "any".
type TransactionFilter struct {
	AccountID  int64
	SecurityID int64
	Types      []string
	From, To   string // inclusive YYYY-MM-DD
	Query      string // matches description, symbol or security name
	Limit      int
	Offset     int
}

// ListTransactions returns a page of transactions (newest first) and the
// total number matching the filter.
func (s *Store) ListTransactions(ctx context.Context, f TransactionFilter) ([]Transaction, int, error) {
	where := []string{"1=1"}
	var args []any
	if f.AccountID != 0 {
		where, args = append(where, "t.account_id = ?"), append(args, f.AccountID)
	}
	if f.SecurityID != 0 {
		where, args = append(where, "t.security_id = ?"), append(args, f.SecurityID)
	}
	if len(f.Types) > 0 {
		where = append(where, "t.type IN ("+placeholders(len(f.Types))+")")
		for _, t := range f.Types {
			args = append(args, t)
		}
	}
	if f.From != "" {
		where, args = append(where, "t.trade_date >= ?"), append(args, f.From)
	}
	if f.To != "" {
		where, args = append(where, "t.trade_date <= ?"), append(args, f.To)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
		where = append(where, `(t.description LIKE ? ESCAPE '\' OR s.symbol LIKE ? ESCAPE '\' OR s.name LIKE ? ESCAPE '\' OR t.source_type LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like, like)
	}
	from := ` FROM transactions t JOIN accounts a ON a.id = t.account_id LEFT JOIN securities s ON s.id = t.security_id WHERE ` + strings.Join(where, " AND ")

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.account_id, a.name, t.security_id, s.symbol, s.name, t.trade_date, t.settlement_date,
			t.type, t.source_type, t.description, t.quantity, t.price, t.amount_cents, t.fees_cents, t.import_id`+
		from+` ORDER BY t.trade_date DESC, t.id DESC LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Transaction{}
	for rows.Next() {
		var t Transaction
		var amt, fees int64
		if err := rows.Scan(&t.ID, &t.AccountID, &t.AccountName, &t.SecurityID, &t.Symbol, &t.SecurityName,
			&t.TradeDate, &t.SettlementDate, &t.Type, &t.SourceType, &t.Description, &t.Quantity, &t.Price,
			&amt, &fees, &t.ImportID); err != nil {
			return nil, 0, err
		}
		t.Amount, t.Fees = ingest.Cents(amt).Dollars(), ingest.Cents(fees).Dollars()
		out = append(out, t)
	}
	return out, total, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Holding is a position from an account's most recent snapshot.
type Holding struct {
	AccountID    int64    `json:"account_id"`
	AccountName  string   `json:"account_name"`
	SecurityID   int64    `json:"security_id"`
	Symbol       *string  `json:"symbol"`
	SecurityName string   `json:"security_name"`
	SecurityType string   `json:"security_type"`
	AssetClass   *string  `json:"asset_class"`
	AsOfDate     string   `json:"as_of_date"`
	Quantity     float64  `json:"quantity"`
	Price        *float64 `json:"price"`
	MarketValue  float64  `json:"market_value"`
}

// LatestHoldings returns each account's most recent snapshot.
func (s *Store) LatestHoldings(ctx context.Context) ([]Holding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.account_id, a.name, h.security_id, s.symbol, s.name, s.security_type, s.asset_class, h.as_of_date,
			h.quantity, h.price, h.market_value_cents
		FROM holdings h
		JOIN accounts a ON a.id = h.account_id
		JOIN securities s ON s.id = h.security_id
		WHERE h.as_of_date = (SELECT max(as_of_date) FROM holdings WHERE account_id = h.account_id)
		  AND a.closed = 0
		ORDER BY h.market_value_cents DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Holding{}
	for rows.Next() {
		var h Holding
		var mv int64
		if err := rows.Scan(&h.AccountID, &h.AccountName, &h.SecurityID, &h.Symbol, &h.SecurityName, &h.SecurityType,
			&h.AssetClass, &h.AsOfDate, &h.Quantity, &h.Price, &mv); err != nil {
			return nil, err
		}
		h.MarketValue = ingest.Cents(mv).Dollars()
		out = append(out, h)
	}
	return out, rows.Err()
}

// Security is a security row.
type Security struct {
	ID           int64   `json:"id"`
	Symbol       *string `json:"symbol"`
	Name         string  `json:"name"`
	SecurityType string  `json:"security_type"`
	AssetClass   *string `json:"asset_class"`
}

// ListSecurities returns every known security.
func (s *Store) ListSecurities(ctx context.Context) ([]Security, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, symbol, name, security_type, asset_class FROM securities ORDER BY COALESCE(symbol, name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Security{}
	for rows.Next() {
		var sec Security
		if err := rows.Scan(&sec.ID, &sec.Symbol, &sec.Name, &sec.SecurityType, &sec.AssetClass); err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

// Performance is a per-fund period summary.
type Performance struct {
	AccountID       int64    `json:"account_id"`
	AccountName     string   `json:"account_name"`
	SecurityID      int64    `json:"security_id"`
	SecurityName    string   `json:"security_name"`
	AssetClass      *string  `json:"asset_class"`
	PeriodStart     string   `json:"period_start"`
	PeriodEnd       string   `json:"period_end"`
	StartingBalance float64  `json:"starting_balance"`
	EndingBalance   float64  `json:"ending_balance"`
	GainLoss        float64  `json:"gain_loss"`
	OtherActivity   float64  `json:"other_activity"`
	ReturnPct       *float64 `json:"return_pct"`
}

// LatestPerformance returns, for each account and fund, the most recent
// reported period (latest end date; the longest period on ties).
func (s *Store) LatestPerformance(ctx context.Context) ([]Performance, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.account_id, a.name, p.security_id, s.name, s.asset_class, p.period_start, p.period_end,
			p.starting_balance_cents, p.ending_balance_cents, p.gain_loss_cents, p.other_activity_cents, p.return_pct
		FROM period_performance p
		JOIN accounts a ON a.id = p.account_id
		JOIN securities s ON s.id = p.security_id
		WHERE p.id = (
			SELECT p2.id FROM period_performance p2
			WHERE p2.account_id = p.account_id AND p2.security_id = p.security_id
			ORDER BY p2.period_end DESC, p2.period_start ASC LIMIT 1)
		  AND a.closed = 0
		ORDER BY a.name, p.ending_balance_cents DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Performance{}
	for rows.Next() {
		var p Performance
		var sb, eb, g, o int64
		if err := rows.Scan(&p.AccountID, &p.AccountName, &p.SecurityID, &p.SecurityName, &p.AssetClass,
			&p.PeriodStart, &p.PeriodEnd, &sb, &eb, &g, &o, &p.ReturnPct); err != nil {
			return nil, err
		}
		p.StartingBalance, p.EndingBalance = ingest.Cents(sb).Dollars(), ingest.Cents(eb).Dollars()
		p.GainLoss, p.OtherActivity = ingest.Cents(g).Dollars(), ingest.Cents(o).Dollars()
		out = append(out, p)
	}
	return out, rows.Err()
}
