package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
)

// Import is an import record as returned by the API.
type Import struct {
	ID                   int64    `json:"id"`
	Source               string   `json:"source"`
	Filename             string   `json:"filename"`
	FileSHA256           string   `json:"file_sha256"`
	ImportedAt           string   `json:"imported_at"`
	HoldingsAsOf         *string  `json:"holdings_as_of"`
	TransactionsParsed   int      `json:"transactions_parsed"`
	TransactionsInserted int      `json:"transactions_inserted"`
	HoldingsParsed       int      `json:"holdings_parsed"`
	Warnings             []string `json:"warnings"`
}

// SaveImport persists a parsed file in one transaction. It is idempotent:
// transactions already present (same fingerprint) are skipped, and a
// holdings snapshot replaces any earlier snapshot for the same accounts and
// date.
func (s *Store) SaveImport(ctx context.Context, sourceID, filename string, content []byte, res *ingest.Result) (*Import, error) {
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	warnings := append([]string{}, res.Warnings...)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var prevID int64
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM imports WHERE file_sha256 = ? ORDER BY id LIMIT 1`, sha).Scan(&prevID); {
	case err == nil:
		warnings = append(warnings, fmt.Sprintf("this exact file was already imported (import #%d); duplicates were skipped", prevID))
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}

	var asOf *string
	if len(res.Holdings) > 0 {
		d := string(res.HoldingsAsOf)
		asOf = &d
	}
	r, err := tx.ExecContext(ctx,
		`INSERT INTO imports (source, filename, file_sha256, imported_at, holdings_as_of) VALUES (?, ?, ?, ?, ?)`,
		sourceID, filename, sha, nowRFC3339(), asOf)
	if err != nil {
		return nil, err
	}
	importID, _ := r.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO import_files (import_id, content) VALUES (?, ?)`, importID, content); err != nil {
		return nil, err
	}

	ids := &idCache{tx: tx, institution: res.Institution, accounts: map[string]int64{}, securities: map[string]int64{}}
	for _, a := range res.Accounts {
		if _, err := ids.account(ctx, a); err != nil {
			return nil, err
		}
	}

	inserted := 0
	for _, t := range res.Transactions {
		acctID, err := ids.account(ctx, ingest.Account{ExternalID: t.AccountExternalID})
		if err != nil {
			return nil, err
		}
		var secID *int64
		if t.Security != nil {
			id, err := ids.security(ctx, *t.Security)
			if err != nil {
				return nil, err
			}
			secID = &id
		}
		var price *float64
		if t.Price != 0 {
			p := t.Price
			price = &p
		}
		r, err := tx.ExecContext(ctx, `
			INSERT INTO transactions (account_id, security_id, import_id, trade_date, settlement_date, type,
				source_type, description, quantity, price, amount_cents, fees_cents, fingerprint, created_at)
			VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (fingerprint) DO NOTHING`,
			acctID, secID, importID, t.TradeDate, t.SettlementDate, t.Type,
			t.SourceType, t.Description, t.Quantity, price, t.Amount, t.Fees, t.Fingerprint(res.Institution), nowRFC3339())
		if err != nil {
			return nil, err
		}
		if n, _ := r.RowsAffected(); n > 0 {
			inserted++
		}
		// Trade prices are real market prices; other rows (e.g. dividends)
		// often carry a placeholder price of 1.0.
		if secID != nil && t.Price > 0 && (t.Type == ingest.TxBuy || t.Type == ingest.TxSell || t.Type == ingest.TxReinvest) {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO prices (security_id, date, price, source) VALUES (?, ?, ?, 'transaction')
				ON CONFLICT (security_id, date) DO NOTHING`, *secID, t.TradeDate, t.Price); err != nil {
				return nil, err
			}
		}
	}

	if len(res.Holdings) > 0 {
		if err := saveHoldings(ctx, tx, ids, importID, res); err != nil {
			return nil, err
		}
	}

	wj, _ := json.Marshal(warnings)
	if _, err := tx.ExecContext(ctx, `
		UPDATE imports SET transactions_parsed = ?, transactions_inserted = ?, holdings_parsed = ?, warnings_json = ?
		WHERE id = ?`, len(res.Transactions), inserted, len(res.Holdings), string(wj), importID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetImport(ctx, importID)
}

func saveHoldings(ctx context.Context, tx *sql.Tx, ids *idCache, importID int64, res *ingest.Result) error {
	asOf := res.HoldingsAsOf
	totals := map[int64]ingest.Cents{}
	cleared := map[int64]bool{}
	for _, h := range res.Holdings {
		acctID, err := ids.account(ctx, ingest.Account{ExternalID: h.AccountExternalID})
		if err != nil {
			return err
		}
		if !cleared[acctID] {
			// A snapshot is the complete set of positions for that account
			// on that date: replace, don't merge, so sold positions vanish.
			if _, err := tx.ExecContext(ctx, `DELETE FROM holdings WHERE account_id = ? AND as_of_date = ?`, acctID, asOf); err != nil {
				return err
			}
			cleared[acctID] = true
		}
		secID, err := ids.security(ctx, h.Security)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO holdings (account_id, security_id, import_id, as_of_date, quantity, price, market_value_cents)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (account_id, security_id, as_of_date) DO UPDATE SET
				quantity = holdings.quantity + excluded.quantity,
				market_value_cents = holdings.market_value_cents + excluded.market_value_cents,
				price = excluded.price, import_id = excluded.import_id`,
			acctID, secID, importID, asOf, h.Quantity, nullFloat(h.Price), h.MarketValue); err != nil {
			return err
		}
		totals[acctID] += h.MarketValue
		if h.Price > 0 {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO prices (security_id, date, price, source) VALUES (?, ?, ?, 'holding')
				ON CONFLICT (security_id, date) DO UPDATE SET price = excluded.price, source = excluded.source`,
				secID, asOf, h.Price); err != nil {
				return err
			}
		}
	}
	for acctID, total := range totals {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO account_balances (account_id, as_of_date, balance_cents, source, import_id)
			VALUES (?, ?, ?, 'holdings', ?)
			ON CONFLICT (account_id, as_of_date) DO UPDATE SET
				balance_cents = excluded.balance_cents, source = excluded.source, import_id = excluded.import_id`,
			acctID, asOf, total, importID); err != nil {
			return err
		}
	}
	return nil
}

func nullFloat(f float64) *float64 {
	if f == 0 {
		return nil
	}
	return &f
}

// idCache resolves (and creates) account and security rows within an import.
type idCache struct {
	tx          *sql.Tx
	institution string
	accounts    map[string]int64
	securities  map[string]int64
}

func (c *idCache) account(ctx context.Context, a ingest.Account) (int64, error) {
	if id, ok := c.accounts[a.ExternalID]; ok {
		return id, nil
	}
	name := a.Name
	if name == "" {
		name = c.institution + " ••" + last4(a.ExternalID)
	}
	typ := a.AccountType
	if typ == "" {
		typ = ingest.AccountUnknown
	}
	if _, err := c.tx.ExecContext(ctx, `
		INSERT INTO accounts (institution, external_id, name, account_type, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (institution, external_id) DO NOTHING`,
		c.institution, a.ExternalID, name, typ, nowRFC3339()); err != nil {
		return 0, err
	}
	var id int64
	if err := c.tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE institution = ? AND external_id = ?`,
		c.institution, a.ExternalID).Scan(&id); err != nil {
		return 0, err
	}
	c.accounts[a.ExternalID] = id
	return id, nil
}

func (c *idCache) security(ctx context.Context, s ingest.Security) (int64, error) {
	key := "s:" + s.Symbol
	if s.Symbol == "" {
		key = "n:" + strings.ToLower(s.Name)
	}
	if id, ok := c.securities[key]; ok {
		return id, nil
	}
	name := s.Name
	if name == "" {
		name = s.Symbol
	}
	typ := s.Type
	if typ == "" {
		typ = ingest.SecurityUnknown
	}
	var err error
	if s.Symbol != "" {
		_, err = c.tx.ExecContext(ctx, `
			INSERT INTO securities (symbol, name, security_type, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (symbol) WHERE symbol IS NOT NULL DO UPDATE SET
				security_type = CASE WHEN securities.security_type = 'unknown' THEN excluded.security_type ELSE securities.security_type END`,
			s.Symbol, name, typ, nowRFC3339())
	} else {
		_, err = c.tx.ExecContext(ctx, `
			INSERT INTO securities (symbol, name, security_type, created_at) VALUES (NULL, ?, ?, ?)
			ON CONFLICT (name) WHERE symbol IS NULL DO NOTHING`,
			name, typ, nowRFC3339())
	}
	if err != nil {
		return 0, err
	}
	var id int64
	if s.Symbol != "" {
		err = c.tx.QueryRowContext(ctx, `SELECT id FROM securities WHERE symbol = ?`, s.Symbol).Scan(&id)
	} else {
		err = c.tx.QueryRowContext(ctx, `SELECT id FROM securities WHERE symbol IS NULL AND name = ?`, name).Scan(&id)
	}
	if err != nil {
		return 0, err
	}
	c.securities[key] = id
	return id, nil
}

func last4(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}

// ListImports returns import records, newest first.
func (s *Store) ListImports(ctx context.Context) ([]Import, error) {
	rows, err := s.db.QueryContext(ctx, importSelect+` ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Import{}
	for rows.Next() {
		im, err := scanImport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *im)
	}
	return out, rows.Err()
}

// GetImport returns one import record.
func (s *Store) GetImport(ctx context.Context, id int64) (*Import, error) {
	return scanImport(s.db.QueryRowContext(ctx, importSelect+` WHERE id = ?`, id))
}

// ImportFile returns the original uploaded bytes of an import.
func (s *Store) ImportFile(ctx context.Context, id int64) (filename string, content []byte, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT i.filename, f.content FROM imports i JOIN import_files f ON f.import_id = i.id WHERE i.id = ?`, id).
		Scan(&filename, &content)
	return
}

// DeleteImport removes an import and every transaction, holding and
// balance it created.
func (s *Store) DeleteImport(ctx context.Context, id int64) error {
	r, err := s.db.ExecContext(ctx, `DELETE FROM imports WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

const importSelect = `SELECT id, source, filename, file_sha256, imported_at, holdings_as_of,
	transactions_parsed, transactions_inserted, holdings_parsed, warnings_json FROM imports`

type scanner interface{ Scan(...any) error }

func scanImport(row scanner) (*Import, error) {
	var im Import
	var wj string
	if err := row.Scan(&im.ID, &im.Source, &im.Filename, &im.FileSHA256, &im.ImportedAt, &im.HoldingsAsOf,
		&im.TransactionsParsed, &im.TransactionsInserted, &im.HoldingsParsed, &wj); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(wj), &im.Warnings); err != nil || im.Warnings == nil {
		im.Warnings = []string{}
	}
	return &im, nil
}
