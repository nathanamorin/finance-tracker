-- Standardized finance schema. One copy of this lives in each user's own
-- encrypted SQLite database, so there is no user_id column anywhere.
--
-- Conventions:
--   * dates are TEXT 'YYYY-MM-DD'; timestamps are TEXT RFC 3339 (UTC)
--   * money is INTEGER cents (*_cents) so sums are exact
--   * share quantities and per-share prices are REAL
--   * transactions.quantity is a signed share delta, transactions.amount_cents
--     a signed cash impact (see internal/ingest.Transaction)

-- One row per uploaded file. The original bytes are kept (encrypted, in
-- this same database) so files can be re-parsed when parsers improve.
CREATE TABLE imports (
    id                    INTEGER PRIMARY KEY,
    source                TEXT    NOT NULL,           -- ingest.Source ID, e.g. 'vanguard_csv'
    filename              TEXT    NOT NULL,
    file_sha256           TEXT    NOT NULL,
    imported_at           TEXT    NOT NULL,
    holdings_as_of        TEXT,
    transactions_parsed   INTEGER NOT NULL DEFAULT 0,
    transactions_inserted INTEGER NOT NULL DEFAULT 0,
    holdings_parsed       INTEGER NOT NULL DEFAULT 0,
    warnings_json         TEXT    NOT NULL DEFAULT '[]'
);
CREATE INDEX imports_sha ON imports(file_sha256);

CREATE TABLE import_files (
    import_id INTEGER PRIMARY KEY REFERENCES imports(id) ON DELETE CASCADE,
    content   BLOB NOT NULL
);

CREATE TABLE accounts (
    id           INTEGER PRIMARY KEY,
    institution  TEXT    NOT NULL,                    -- 'Vanguard'
    external_id  TEXT    NOT NULL,                    -- account number as the source reports it
    name         TEXT    NOT NULL,                    -- user-editable display name
    account_type TEXT    NOT NULL DEFAULT 'unknown',  -- ingest.AccountType
    currency     TEXT    NOT NULL DEFAULT 'USD',
    closed       INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT    NOT NULL,
    UNIQUE (institution, external_id)
);

CREATE TABLE securities (
    id            INTEGER PRIMARY KEY,
    symbol        TEXT,                               -- NULL when the source has none
    name          TEXT    NOT NULL,
    security_type TEXT    NOT NULL DEFAULT 'unknown', -- ingest.SecurityType
    created_at    TEXT    NOT NULL
);
-- A security is identified by symbol when it has one, otherwise by name.
CREATE UNIQUE INDEX securities_symbol ON securities(symbol) WHERE symbol IS NOT NULL;
CREATE UNIQUE INDEX securities_name_nosymbol ON securities(name) WHERE symbol IS NULL;

CREATE TABLE transactions (
    id              INTEGER PRIMARY KEY,
    account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    security_id     INTEGER REFERENCES securities(id),
    import_id       INTEGER REFERENCES imports(id) ON DELETE CASCADE,
    trade_date      TEXT    NOT NULL,
    settlement_date TEXT,
    type            TEXT    NOT NULL,                 -- ingest.TxType
    source_type     TEXT    NOT NULL,                 -- the source's own label
    description     TEXT    NOT NULL DEFAULT '',
    quantity        REAL    NOT NULL DEFAULT 0,
    price           REAL,
    amount_cents    INTEGER NOT NULL,
    fees_cents      INTEGER NOT NULL DEFAULT 0,
    category        TEXT,                             -- reserved for bank/card sources
    notes           TEXT,
    fingerprint     TEXT    NOT NULL UNIQUE,          -- de-duplication across overlapping uploads
    created_at      TEXT    NOT NULL
);
CREATE INDEX transactions_account_date ON transactions(account_id, trade_date);
CREATE INDEX transactions_date ON transactions(trade_date);
CREATE INDEX transactions_type ON transactions(type);
CREATE INDEX transactions_security ON transactions(security_id);

-- Point-in-time position snapshots, one per (account, security, date).
CREATE TABLE holdings (
    id                 INTEGER PRIMARY KEY,
    account_id         INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    security_id        INTEGER NOT NULL REFERENCES securities(id),
    import_id          INTEGER REFERENCES imports(id) ON DELETE CASCADE,
    as_of_date         TEXT    NOT NULL,
    quantity           REAL    NOT NULL,
    price              REAL,
    market_value_cents INTEGER NOT NULL,
    UNIQUE (account_id, security_id, as_of_date)
);
CREATE INDEX holdings_date ON holdings(as_of_date);

-- Total account value on a date. Derived from holdings for brokerage
-- sources; bank/card sources that report balances directly write here.
CREATE TABLE account_balances (
    account_id    INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    as_of_date    TEXT    NOT NULL,
    balance_cents INTEGER NOT NULL,
    source        TEXT    NOT NULL,                   -- 'holdings' | 'reported'
    import_id     INTEGER REFERENCES imports(id) ON DELETE CASCADE,
    PRIMARY KEY (account_id, as_of_date)
);

-- Known prices per security per day, gathered from transaction prices and
-- holdings snapshots (and, later, market-data sources). Used to value
-- reconstructed historical positions.
CREATE TABLE prices (
    security_id INTEGER NOT NULL REFERENCES securities(id) ON DELETE CASCADE,
    date        TEXT    NOT NULL,
    price       REAL    NOT NULL,
    source      TEXT    NOT NULL,                     -- 'transaction' | 'holding' | 'market'
    PRIMARY KEY (security_id, date)
);
