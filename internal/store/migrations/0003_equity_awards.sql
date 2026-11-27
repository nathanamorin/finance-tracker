-- Employer equity awards (RSUs now; ESPP/options later). Unvested shares
-- live only here: they are never holdings and never count toward account
-- balances or net worth. Sellable (vested, released) shares are imported
-- as ordinary holdings.
--
-- prices.source may now also be 'reported' (a price the source states or
-- implies, e.g. E*TRADE's Est. Market Value / shares).

CREATE TABLE equity_grants (
    id                     INTEGER PRIMARY KEY,
    account_id             INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    security_id            INTEGER NOT NULL REFERENCES securities(id),
    import_id              INTEGER REFERENCES imports(id) ON DELETE CASCADE,
    grant_number           TEXT    NOT NULL,
    award_type             TEXT    NOT NULL,   -- e.g. 'RSU'
    grant_date             TEXT,
    status                 TEXT,
    granted_qty            REAL    NOT NULL,
    vested_qty             REAL    NOT NULL DEFAULT 0,
    unvested_qty           REAL    NOT NULL DEFAULT 0,
    withheld_qty           REAL    NOT NULL DEFAULT 0,
    sellable_qty           REAL    NOT NULL DEFAULT 0,
    cancelled_qty          REAL    NOT NULL DEFAULT 0,
    est_market_value_cents INTEGER,
    as_of_date             TEXT    NOT NULL,   -- snapshot these figures describe
    UNIQUE (account_id, grant_number)
);

-- Vesting tranches; replaced as a set whenever a newer snapshot of the
-- grant is imported.
CREATE TABLE equity_vests (
    id                        INTEGER PRIMARY KEY,
    grant_id                  INTEGER NOT NULL REFERENCES equity_grants(id) ON DELETE CASCADE,
    vest_period               INTEGER,
    vest_date                 TEXT    NOT NULL,
    granted_qty               REAL    NOT NULL,
    vested_qty                REAL    NOT NULL DEFAULT 0,
    released_qty              REAL    NOT NULL DEFAULT 0,
    cancelled_qty             REAL    NOT NULL DEFAULT 0,
    shares_withheld_for_taxes REAL    NOT NULL DEFAULT 0,
    released_amount_cents     INTEGER NOT NULL DEFAULT 0,
    taxes_paid_cents          INTEGER NOT NULL DEFAULT 0,
    tax_status                TEXT
);
CREATE INDEX equity_vests_grant ON equity_vests(grant_id, vest_date);
