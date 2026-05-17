-- Per-daemon key/value scratchpad for one-shot housekeeping flags (backfill
-- markers, schema-touching upgrade notes, etc.) that don't deserve their own
-- migration. The store package writes / reads via plain SQL.
CREATE TABLE IF NOT EXISTS daemon_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
