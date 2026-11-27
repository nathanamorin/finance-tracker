-- Per-user UI/report settings (e.g. projection assumptions), stored as
-- JSON in the user's own encrypted database so they follow the user
-- across devices.
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,   -- e.g. 'projections'
    value_json TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
