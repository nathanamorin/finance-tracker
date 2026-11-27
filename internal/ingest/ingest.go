// Package ingest defines the standardized, source-independent model that
// every data source (Vanguard, other brokerages, banks, ...) is parsed into,
// plus a registry of source parsers.
//
// Adding a new data source means implementing Source and registering it in
// the registry built in cmd/server. Parsers never touch the database; they only
// turn an uploaded file into a Result, which internal/store persists
// idempotently.
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

// TxType is the normalized transaction type. Source-specific labels are
// preserved separately in Transaction.SourceType.
type TxType string

const (
	TxBuy             TxType = "buy"
	TxSell            TxType = "sell"
	TxDividend        TxType = "dividend"
	TxReinvest        TxType = "reinvest"
	TxInterest        TxType = "interest"
	TxCapitalGainLT   TxType = "capital_gain_lt"
	TxCapitalGainST   TxType = "capital_gain_st"
	TxContribution    TxType = "contribution"
	TxWithdrawal      TxType = "withdrawal"
	TxTransferIn      TxType = "transfer_in"
	TxTransferOut     TxType = "transfer_out"
	TxConversionIn    TxType = "conversion_in"
	TxConversionOut   TxType = "conversion_out"
	TxSweepIn         TxType = "sweep_in"
	TxSweepOut        TxType = "sweep_out"
	TxFee             TxType = "fee"
	TxTaxWithholding  TxType = "tax_withholding"
	TxCorporateAction TxType = "corporate_action"
	TxOther           TxType = "other"
)

// ExternalFlowTypes are transaction types that move money into or out of
// the tracked accounts from outside (as opposed to activity within an
// account). Their summed amount is "net contributions".
var ExternalFlowTypes = []TxType{TxContribution, TxWithdrawal, TxTransferIn, TxTransferOut}

// IncomeTypes are transaction types that represent investment income.
var IncomeTypes = []TxType{TxDividend, TxInterest, TxCapitalGainLT, TxCapitalGainST}

// AccountType is the normalized kind of account. Sources rarely provide it,
// so it defaults to "unknown" and is user-editable.
type AccountType string

const (
	AccountUnknown        AccountType = "unknown"
	AccountBrokerage      AccountType = "brokerage"
	AccountIRATraditional AccountType = "ira_traditional"
	AccountIRARoth        AccountType = "ira_roth"
	Account401k           AccountType = "401k"
	AccountHSA            AccountType = "hsa"
	Account529            AccountType = "529"
	AccountChecking       AccountType = "checking"
	AccountSavings        AccountType = "savings"
	AccountCreditCard     AccountType = "credit_card"
	AccountLoan           AccountType = "loan"
	AccountOther          AccountType = "other"
)

// AccountTypes lists every valid AccountType, for validation and UI pickers.
var AccountTypes = []AccountType{
	AccountUnknown, AccountBrokerage, AccountIRATraditional, AccountIRARoth, Account401k,
	AccountHSA, Account529, AccountChecking, AccountSavings, AccountCreditCard, AccountLoan, AccountOther,
}

// SecurityType is the normalized kind of security.
type SecurityType string

const (
	SecurityUnknown     SecurityType = "unknown"
	SecurityMutualFund  SecurityType = "mutual_fund"
	SecurityETF         SecurityType = "etf"
	SecurityStock       SecurityType = "stock"
	SecurityBond        SecurityType = "bond"
	SecurityMoneyMarket SecurityType = "money_market"
	SecurityCD          SecurityType = "cd"
	SecurityCash        SecurityType = "cash"
)

// Cents is a money amount in integer cents, so sums are exact.
type Cents int64

// ToCents rounds a float dollar amount to cents.
func ToCents(dollars float64) Cents { return Cents(math.Round(dollars * 100)) }

// Dollars converts back to a float for display/JSON.
func (c Cents) Dollars() float64 { return float64(c) / 100 }

// Date is a calendar date (no time zone), formatted YYYY-MM-DD.
type Date string

// NewDate formats t as a Date.
func NewDate(t time.Time) Date { return Date(t.Format("2006-01-02")) }

// Account identifies an account at an institution.
type Account struct {
	ExternalID  string // e.g. the brokerage account number
	Name        string // optional display name from the source
	AccountType AccountType
}

// Security identifies an investment. Symbol may be empty (e.g. some
// 401k funds or CDs), in which case Name is the identity.
type Security struct {
	Symbol string
	Name   string
	Type   SecurityType
}

// Transaction is one normalized ledger entry.
//
// Sign conventions (enforced by parsers):
//   - Quantity: signed share delta for the account's position (positive =
//     shares added, e.g. buy/reinvest; negative = shares removed). Zero for
//     cash-only activity.
//   - Amount: signed cash impact on the account (positive = cash in, e.g.
//     a sale, dividend or contribution; negative = cash out, e.g. a buy).
type Transaction struct {
	AccountExternalID string
	Security          *Security // nil for pure cash activity
	TradeDate         Date
	SettlementDate    Date // optional
	Type              TxType
	SourceType        string // the source's own label, kept verbatim
	Description       string
	Quantity          float64
	Price             float64
	Amount            Cents
	Fees              Cents
	// Occurrence disambiguates rows that are otherwise byte-for-byte
	// identical within one file (e.g. two same-day reinvestments of the
	// same amount); it is the 0-based count of earlier identical rows.
	Occurrence int
}

// Fingerprint returns a stable identity for de-duplicating transactions
// across overlapping uploads. Sources without native transaction IDs (like
// Vanguard CSVs) rely on this.
func (t Transaction) Fingerprint(institution string) string {
	sym, name := "", ""
	if t.Security != nil {
		sym, name = t.Security.Symbol, t.Security.Name
	}
	key := strings.Join([]string{
		institution, t.AccountExternalID, string(t.TradeDate), string(t.SettlementDate),
		strings.ToLower(t.SourceType), sym, strings.ToLower(name), strings.ToLower(t.Description),
		fmt.Sprintf("%.6f", t.Quantity), fmt.Sprintf("%.6f", t.Price),
		fmt.Sprintf("%d", t.Amount), fmt.Sprintf("%d", t.Fees), fmt.Sprintf("%d", t.Occurrence),
	}, "\x1f")
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Holding is a point-in-time position snapshot.
type Holding struct {
	AccountExternalID string
	Security          Security
	Quantity          float64
	Price             float64
	MarketValue       Cents
}

// Result is everything a parser extracted from one file.
type Result struct {
	Institution  string
	Accounts     []Account
	Transactions []Transaction
	Holdings     []Holding
	// HoldingsAsOf is the date the Holdings snapshot is valid for.
	HoldingsAsOf Date
	// Warnings are non-fatal problems (skipped rows, unknown types, ...)
	// surfaced to the user on the import record.
	Warnings []string
}

// ParseOptions carries user-supplied context for a parse.
type ParseOptions struct {
	// AsOf is the date to stamp on position snapshots when the file
	// itself does not say (Vanguard's positions section has no date).
	AsOf Date
}

// Source is a parser for one kind of uploaded file.
type Source interface {
	// ID is a stable identifier stored on import records, e.g. "vanguard_csv".
	ID() string
	// Name is a human-readable label.
	Name() string
	// Description explains where to get the file.
	Description() string
	// Detect reports whether the file looks like this source's format,
	// given its name and first few KB.
	Detect(filename string, head []byte) bool
	// Parse converts the file into the standardized model.
	Parse(r io.Reader, opts ParseOptions) (*Result, error)
}

// ErrUnknownSource is returned when no registered source matches a file.
var ErrUnknownSource = errors.New("could not detect the file format; choose a source explicitly")

// Registry holds the available sources.
type Registry struct {
	sources []Source
}

// NewRegistry builds a registry from the given sources, in detection order.
func NewRegistry(sources ...Source) *Registry { return &Registry{sources: sources} }

// All returns every registered source.
func (r *Registry) All() []Source { return r.sources }

// Get looks up a source by ID.
func (r *Registry) Get(id string) (Source, bool) {
	for _, s := range r.sources {
		if s.ID() == id {
			return s, true
		}
	}
	return nil, false
}

// Detect finds the first source that claims the file.
func (r *Registry) Detect(filename string, head []byte) (Source, error) {
	for _, s := range r.sources {
		if s.Detect(filename, head) {
			return s, nil
		}
	}
	return nil, ErrUnknownSource
}
