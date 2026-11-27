-- Benefits Center / 401(k) style sources: asset classes, period summaries,
-- and balances that are reported or derived rather than computed from
-- holdings (account_balances.source is now 'holdings' | 'reported' | 'derived').

ALTER TABLE securities ADD COLUMN asset_class TEXT;  -- source's own classification, optional

-- Per-fund performance over a date range. Kept separate from transactions:
-- these summaries usually cover overlapping ranges (year-to-date), so they
-- can't be summed like ledger entries.
CREATE TABLE period_performance (
    id                      INTEGER PRIMARY KEY,
    account_id              INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    security_id             INTEGER NOT NULL REFERENCES securities(id),
    import_id               INTEGER REFERENCES imports(id) ON DELETE CASCADE,
    period_start            TEXT    NOT NULL,
    period_end              TEXT    NOT NULL,
    starting_balance_cents  INTEGER NOT NULL,   -- derived: ending - gain - other
    ending_balance_cents    INTEGER NOT NULL,
    gain_loss_cents         INTEGER NOT NULL,
    other_activity_cents    INTEGER NOT NULL,   -- contributions, match, fees, transfers (net)
    return_pct              REAL,               -- as reported, e.g. 6.28 for 6.28%
    UNIQUE (account_id, security_id, period_start, period_end)
);
CREATE INDEX period_performance_end ON period_performance(period_end);
