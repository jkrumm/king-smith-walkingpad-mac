-- Tombstones for sessions deleted locally (stitched-into-survivor or dropped
-- as too-short) that still need their corresponding row removed from Argo.
-- The sync worker drains unsynced rows via DELETE /walking-pad/sessions/:uuid.
-- Rows can be GC'd once synced_at + retention has passed.
CREATE TABLE IF NOT EXISTS session_tombstones (
    uuid       TEXT PRIMARY KEY,
    deleted_at TEXT NOT NULL,
    synced_at  TEXT NULL
);

CREATE INDEX IF NOT EXISTS idx_session_tombstones_unsynced
    ON session_tombstones(deleted_at)
    WHERE synced_at IS NULL;
