-- 0001: initial schema (PRD §6).

CREATE TABLE IF NOT EXISTS sessions (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  uuid            TEXT NOT NULL UNIQUE,
  started_at      TEXT NOT NULL,
  ended_at        TEXT,
  duration_s      INTEGER NOT NULL DEFAULT 0,
  distance_m      REAL    NOT NULL DEFAULT 0,
  steps           INTEGER NOT NULL DEFAULT 0,
  avg_speed_kmh   REAL    NOT NULL DEFAULT 0,
  max_speed_kmh   REAL    NOT NULL DEFAULT 0,
  kcal            REAL    NOT NULL DEFAULT 0,
  pause_count     INTEGER NOT NULL DEFAULT 0,
  synced_at       TEXT,
  created_at      TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_sessions_started_at ON sessions(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_unsynced ON sessions(synced_at) WHERE synced_at IS NULL;

CREATE TABLE IF NOT EXISTS samples (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id      INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  ts              TEXT NOT NULL,
  belt_state      INTEGER NOT NULL,
  speed_kmh       REAL    NOT NULL,
  distance_m      REAL    NOT NULL,
  steps           INTEGER NOT NULL,
  mode            INTEGER NOT NULL,
  button          INTEGER NOT NULL DEFAULT 0,
  raw_frame_hex   TEXT
);
CREATE INDEX IF NOT EXISTS idx_samples_session ON samples(session_id, ts);
