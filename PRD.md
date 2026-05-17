# PRD — king-smith-walkingpad-mac

**Status:** Draft v1 (2026-05-17). Living document — extend as the project evolves.
**Owner:** Johannes Krumm.
**License:** AGPL-3.0.

**Naming.** The repo and Go module keep the long form `king-smith-walkingpad-mac` for discoverability. Everything user-facing — the macOS `.app` bundle, the CLI binary, the LaunchAgent label, the bundle identifier, the data directory, log paths, and the Raycast extension — is the short form `WalkingPad` / `walkingpad`. The Go source layout (`cmd/king-smith-walkingpad-mac/…`) still uses the repo name.

---

## 1. Problem & Goals

The KingSmith WalkingPad P1 ships with the "KS Fit" mobile app and a hardware remote. Both are functional but limited: no desktop control, no machine-readable history, no integration with personal data systems. Walking-pad usage is a daily-driver workflow at the standing desk; it deserves a first-class macOS-native control surface and a clean stats pipeline.

**Goals (in priority order):**

1. **Reliable BLE control of the P1 from macOS** — start, stop, set speed (0.5–6.0 km/h), set start-speed preference, switch mode. Survives BLE drops via auto-reconnect.
2. **Live session telemetry** — capture every 750 ms–3 s sample of speed/time/distance/steps to a local SQLite store. Compute calories client-side.
3. **Smart session grouping** — pauses up to 15 minutes (coffee, bathroom) belong to the same logical session; longer gaps start a new one.
4. **Raycast extension** — menu-bar live status (speed, time, distance, steps); commands for start/stop/speed/history. The primary UI surface during walks.
5. **Argo sync** — push completed sessions to `argo.jkrumm.com/api/walking-pad/sessions` so they appear in the personal dashboard alongside Garmin, weight, sleep, etc.
6. **Shareable** — clean enough that other P1 owners can clone, `make install`, and use it.

**Non-goals (explicit):**

- **No Garmin integration.** Manual activities in Garmin Connect cannot update the watch's daily step count (the step counter is owned by the wrist sensor). User starts "Indoor Walking" on the watch manually if they want it on the watch ring; we own the desktop telemetry.
- **No Strava/Apple Health/HealthKit** export in v1. Could come later.
- **No FTMS support.** P1 speaks legacy WiLink only; FTMS is for newer KingSmith models (Z1, MC-21 family).
- **No phone app.** macOS-only. Phone owners use KS Fit.
- **No incline control.** P1 has no incline.
- **No heart-rate integration.** P1 has no HR sensor.
- **No Homebrew formula in v1.** `make install` is the supported install path. A curl-install script may follow once the project is stable.

---

## 2. User & Personas

**Primary user — Johannes (me):** standing-desk Mac user, walks ~30–90 min/day in 1–3 bouts at 3.5–5.5 km/h. Wants to control the belt without touching the remote, wants stats in his Argo dashboard, doesn't want to lose telemetry if Wi-Fi/Argo is down.

**Secondary user — other P1 owner:** clones the repo, has Go + Raycast + Make installed, wants a working setup in <10 minutes. Doesn't need Argo sync (becomes a no-op without config).

---

## 3. Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│  macOS                                                           │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────────┐ │
│  │ Raycast Extension (TypeScript)                              │ │
│  │   • menu-bar: ▶ 4.5 km/h · 12:34 · 1.2 km · 1450 steps     │ │
│  │   • commands: start, stop, ±0.5, set-speed, today, history │ │
│  └────────────────┬────────────────────────────────────────────┘ │
│                   │ HTTP localhost:7706 (token-auth optional)    │
│                   ▼                                              │
│  ┌─────────────────────────────────────────────────────────────┐ │
│  │ WalkingPad (Go daemon, LaunchAgent)                         │ │
│  │   ┌────────────┐  ┌────────────┐  ┌────────────┐            │ │
│  │   │ BLE Client │──│ Session    │──│ SQLite     │            │ │
│  │   │ tinygo-x   │  │ Manager    │  │ Store      │            │ │
│  │   └────┬───────┘  └────────────┘  └──────┬─────┘            │ │
│  │        │           ┌────────────┐        │                  │ │
│  │        │           │ HTTP API   │────────┘                  │ │
│  │        │           └────────────┘                           │ │
│  │        │           ┌────────────┐                           │ │
│  │        │           │ Argo Sync  │ ──── HTTPS ────►          │ │
│  │        │           └────────────┘                           │ │
│  │        ▼                                                    │ │
│  │   .app bundle → CoreBluetooth → P1 over GATT (FE00)         │ │
│  └─────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼ HTTPS argo.jkrumm.com/api/walking-pad/sessions
              ┌──────────────────────────────────────┐
              │ argo (Elysia + Bun + Postgres)       │
              │   new domain: /walking-pad           │
              │   table: argo.walking_pad_sessions   │
              └──────────────────────────────────────┘
                              │
                              ▼
              Argo dashboard + Hermes agent consume sessions
```

**Single process owns the BLE link.** CoreBluetooth allows only one app to maintain a GATT connection per peripheral at a time. The daemon is the sole BLE client; Raycast, CLI, and any other consumers go through the HTTP API.

---

## 4. BLE Protocol — KingSmith WiLink

Reverse-engineered from `ph4r05/ph4-walkingpad`, cross-verified against `tim-oster/walkingpad` (Go, P1-confirmed) and `mcdax/walkingpad-controller`. Reference clones live at `/tmp/walkingpad-research/` during development.

### 4.1 Discovery & GATT

- **Advertised service UUID:** `0000fe00-0000-1000-8000-00805f9b34fb` (16-bit alias `0xFE00`).
- **Name match:** `name.lower().contains("walkingpad")`. KS Fit also accepts `KS-*`, `KINGSMITH*`, `ZP-*`, `DYNAMAX*`.
- **GATT characteristics** inside service `0xFE00`:
  - `0000fe01-…` — RX, notify-only (device → controller, status frames).
  - `0000fe02-…` — TX, **write-without-response** (controller → device, commands).
- **MTU:** default 23 bytes is sufficient. Frames are 6, 10, or 20 bytes. Do not request MTU exchange.

### 4.2 Frame format

**Controller → device (commands):**
```
[0xF7] [TYPE] [OP] [PARAM] [CRC] [0xFD]                 // 6-byte standard
[0xF7] [0xA6] [KEY] [STYPE] [V0] [V1] [V2] [0xAC] [CRC] [0xFD]   // 10-byte set_pref
```

**Device → controller (status):** 20 bytes, prefix `0xF8 0xA2`.

**CRC** (both directions): `sum(buf[1:-2]) & 0xFF` — sum of all bytes between the start byte (exclusive) and the checksum slot (exclusive). The device silently drops bad frames.

### 4.3 Commands

| Op | Bytes (hex) | Meaning |
|-|-|-|
| Poll status | `F7 A2 00 00 A2 FD` | Request current status frame. |
| Set speed | `F7 A2 01 N ?? FD` | N = speed × 10 (0–60 = 0.0–6.0 km/h). N=0 stops. |
| Switch mode | `F7 A2 02 M ?? FD` | M: 0=auto, 1=manual, 2=standby. |
| Start belt | `F7 A2 04 01 FF FD` | Begin walking in current mode. |
| Beep/ack | `F7 A2 03 07 AC FD` | Sent after connect; purpose unconfirmed but ph4 always sends it. |
| Set pref | `F7 A6 KEY STYPE V0 V1 V2 AC ?? FD` | 10 bytes. See pref keys below. |
| Last record | `F7 A7 AA FF 50 FD` | Request last-run record. Consumed after two reads. |

**Pause** = `set speed 0`. No dedicated pause opcode. State machine tracks pause vs. stop client-side.

**Preference keys** (`0xA6` opcode):

| Key | Name | Encoding |
|-|-|-|
| 1 | TARGET | stype: 0=none/1=dist/2=cal/3=time; v0..v2 = 24-bit BE value |
| 3 | MAX_SPEED | speed × 10 as 24-bit BE |
| 4 | START_SPEED | speed × 10 as 24-bit BE (persists across power cycles) |
| 5 | START_INTEL | 0/1 — auto-start on foot-step detection |
| 6 | SENSITIVITY | 1=high, 2=medium, 3=low (auto mode) |
| 7 | DISPLAY | 7-bit bitmask of fields cycled on LED display |
| 8 | UNITS | 0=km, 1=miles |
| 9 | CHILD_LOCK | 0/1 |

### 4.4 Status frame (20 bytes, type `0xA2`)

| Offset | Size | Field | Notes |
|-|-|-|-|
| 0 | 1 | magic | `0xF8` |
| 1 | 1 | type | `0xA2` |
| 2 | 1 | belt_state | see below |
| 3 | 1 | speed | uint8, km/h × 10 |
| 4 | 1 | mode | 0=auto, 1=manual, 2=standby |
| 5..7 | 3 | time | uint24 BE, seconds |
| 8..10 | 3 | distance | uint24 BE, units of **10 m** (divide by 100 for km) |
| 11..13 | 3 | steps | uint24 BE |
| 14 | 1 | app_speed | last commanded speed (semantics fuzzy) |
| 15 | 1 | reserved | "unknown" — possibly HR on equipped models, **0 on P1** |
| 16 | 1 | button | physical remote: 0=none, 2=up, 3=stop, 4=down |
| 17 | 1 | CRC | `sum(buf[1:17]) & 0xFF` |
| 18 | 1 | reserved | — |
| 19 | 1 | terminator | `0xFD` |

**Belt state values:**

| Value | State |
|-|-|
| 0 | STOPPED |
| 1 | ACTIVE |
| 2 | PAUSED (decel to 0; counters preserved) |
| 5 | STANDBY (belt powered down) |
| 9 | STARTING (ramp-up) |

### 4.5 Polling & rate limits

- **Maximum write rate ≈ 1.4 Hz.** Device drops frames if commands arrive faster. Enforce minimum 700 ms gap between any writes.
- **Default poll cadence:** 1 s (between ph4's 750 ms and tim-oster's 3 s — 1 s gives smooth menu-bar updates without stressing the firmware).
- **No protocol keep-alive.** The periodic status poll doubles as liveness probe.
- **Disconnect detection** via the `tinygo.org/x/bluetooth` adapter's disconnect callback.
- **Auto-reconnect strategy:** exponential backoff capped at 30 s; restart scan + connect cycle; resume the active session if reconnect happens within 60 s, else mark previous session ended.

### 4.6 P1-specific notes

- **Max speed 6.0 km/h** (hardcoded in tim-oster; matches P1 hardware).
- **No cadence in status frame.** Derive client-side from `(Δsteps / Δt) × 60`.
- **No calories from device.** Compute client-side using MET formula:
  ```
  kcal/min = MET(speed_kmh) × body_weight_kg × 0.0175
  ```
  with `MET ≈ 2.0 + 0.5 × (speed_kmh − 2.0)` for walking 2–6 km/h (rough but adequate). Body weight from config.
- **No heart rate, no inclination.**
- **macOS quirk:** the scanner must not pre-filter; we filter client-side by service UUID + name.

---

## 5. Go module layout

```
king-smith-walkingpad-mac/
├── cmd/king-smith-walkingpad-mac/
│   └── main.go                     // CLI entrypoint: serve, scan, status subcommands
├── internal/
│   ├── ble/                        // BLE client — tinygo.org/x/bluetooth wrapper
│   │   ├── client.go               // Connect, EnableNotifications, write-with-rate-limit
│   │   ├── frames.go               // Encode/decode commands & status frames + CRC
│   │   ├── frames_test.go          // Table tests with hex fixtures from ph4 README
│   │   └── reconnect.go            // Exponential backoff, scan-and-rebind
│   ├── session/                    // Smart grouping + calorie calc
│   │   ├── manager.go              // Open/close sessions based on 15-min gap rule
│   │   ├── calories.go             // MET formula, weight-aware
│   │   └── manager_test.go
│   ├── store/                      // SQLite persistence
│   │   ├── store.go                // modernc.org/sqlite, migrations
│   │   ├── queries.go              // CRUD + aggregations (today, week, all-time)
│   │   ├── migrations/             // SQL files, embedded via go:embed
│   │   └── store_test.go
│   ├── api/                        // HTTP API for Raycast & friends
│   │   ├── server.go               // net/http on 127.0.0.1:7706
│   │   ├── handlers.go             // /status /start /stop /speed /sessions /sessions/:id
│   │   └── handlers_test.go
│   ├── sync/                       // Argo upload worker
│   │   ├── worker.go               // Polls store for unsynced, POSTs, marks synced
│   │   └── worker_test.go          // httptest.Server fixture
│   ├── config/                     // TOML config: device addr, port, weight, argo url+token
│   │   ├── config.go
│   │   └── config_test.go
│   └── logger/                     // structured logging (slog → JSONL on disk + pretty stderr)
│       └── logger.go
├── raycast/                        // Raycast extension (TypeScript)
│   ├── package.json
│   ├── src/
│   │   ├── menu-bar.tsx
│   │   ├── start.tsx
│   │   ├── stop.tsx
│   │   ├── set-speed.tsx
│   │   ├── history.tsx
│   │   └── lib/api.ts
│   └── README.md
├── scripts/
│   ├── com.jkrumm.walkingpad.plist
│   ├── build-app-bundle.sh         // wraps binary into .app with Info.plist
│   └── Info.plist.tmpl
├── Makefile
├── go.mod / go.sum
├── PRD.md (this)
├── CLAUDE.md
├── README.md
└── LICENSE (AGPL-3.0)
```

---

## 6. SQLite schema

Stored at `~/Library/Application Support/WalkingPad/db.sqlite`. WAL mode enabled.

```sql
-- v1
CREATE TABLE IF NOT EXISTS sessions (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  uuid            TEXT NOT NULL UNIQUE,           -- client-generated, stable across syncs
  started_at      TEXT NOT NULL,                  -- ISO-8601 UTC
  ended_at        TEXT,                           -- NULL while session is open
  duration_s      INTEGER NOT NULL DEFAULT 0,     -- total active seconds (excl. pauses > grace)
  distance_m      REAL    NOT NULL DEFAULT 0,
  steps           INTEGER NOT NULL DEFAULT 0,
  avg_speed_kmh   REAL    NOT NULL DEFAULT 0,
  max_speed_kmh   REAL    NOT NULL DEFAULT 0,
  kcal            REAL    NOT NULL DEFAULT 0,     -- computed client-side
  pause_count     INTEGER NOT NULL DEFAULT 0,
  synced_at       TEXT,                           -- NULL = pending Argo sync
  created_at      TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_sessions_started_at ON sessions(started_at DESC);
CREATE INDEX idx_sessions_unsynced ON sessions(synced_at) WHERE synced_at IS NULL;

CREATE TABLE IF NOT EXISTS samples (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id      INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  ts              TEXT NOT NULL,                  -- ISO-8601 UTC
  belt_state      INTEGER NOT NULL,               -- raw byte from frame
  speed_kmh       REAL    NOT NULL,
  distance_m      REAL    NOT NULL,               -- cumulative within this session
  steps           INTEGER NOT NULL,               -- cumulative within this session
  mode            INTEGER NOT NULL,
  button          INTEGER NOT NULL DEFAULT 0,
  raw_frame_hex   TEXT                            -- for debugging; optional
);
CREATE INDEX idx_samples_session ON samples(session_id, ts);

CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY);
```

**Aggregations of interest** (queries in `store/queries.go`):
- Today: sum/max over sessions where `date(started_at) = date('now', 'localtime')`.
- This week / month: same with date range.
- All-time totals.
- Most recent N sessions for history view.

---

## 7. Session-grouping logic

State machine in `session/manager.go`, fed by every status frame from BLE.

**Inputs per tick:** `belt_state`, `speed_kmh`, `distance_m`, `steps`, `wall_time`.

**Rules:**

1. **OPEN_SESSION** when belt enters ACTIVE (state=1) from STOPPED/STANDBY/STARTING and no session is open.
2. **EXTEND_SESSION** while belt is ACTIVE or PAUSED — keep accumulating samples; reset the "idle since" clock to `wall_time` whenever active.
3. **CLOSE_SESSION** when belt has been STOPPED/STANDBY continuously for `> session_gap_minutes` (default 15). Compute final stats from samples, write `ended_at`, mark `synced_at = NULL` (queues for Argo upload), emit log line.
4. **RESUMED_SESSION** when belt re-enters ACTIVE within the grace window → keep the existing session open; increment `pause_count`.

**Edge cases:**
- Daemon restart mid-session: on startup, look for the most recent unended session; if `started_at < 6h ago`, resume it; otherwise force-close with `ended_at = last_sample_ts`.
- Distance/steps counters reset on the device when belt goes to STANDBY → trust device counters within a session, but on RESUMED_SESSION add the post-resume values to the pre-pause totals (we track `baseline_distance`, `baseline_steps` per resume).
- BLE drop mid-session: don't close immediately. Auto-reconnect for up to 60 s; if reconnect succeeds, RESUMED_SESSION; if not, treat the drop time as the effective stop and CLOSE_SESSION.

---

## 8. HTTP API

Listens on `127.0.0.1:7706` (configurable). Loopback-only; no TLS. Bearer-token optional (off by default for local use; on if `config.api_token` set).

### Endpoints

| Method & path | Body | Response | Notes |
|-|-|-|-|
| `GET /health` | — | `{"ok":true,"version":"…"}` | Unauthenticated. |
| `GET /status` | — | live status object (see below) | Last frame from BLE + current session summary. |
| `POST /start` | `{"speed_kmh":3.5}` (optional) | `{"ok":true}` | Switches to manual mode, sets speed, sends start. |
| `POST /stop` | — | `{"ok":true}` | `set_speed(0)`. |
| `POST /speed` | `{"speed_kmh":4.0}` | `{"ok":true}` | 0.5–6.0 inclusive; rounded to 0.1. |
| `POST /pref/start-speed` | `{"speed_kmh":2.0}` | `{"ok":true}` | Writes PREFS_START_SPEED. |
| `GET /sessions` | — | `{"sessions":[…]}` | Paginated: `?limit=` `?before=`. |
| `GET /sessions/:uuid` | — | full session + samples | |
| `GET /summary?period=today\|week\|month\|all` | — | aggregates | For Raycast home / menu-bar tooltips. |
| `POST /sync/argo` | — | `{"synced":N,"failed":M}` | Manual trigger (otherwise nightly + post-session). |

### Live status response shape

```json
{
  "connected": true,
  "belt_state": "active",
  "mode": "manual",
  "speed_kmh": 4.5,
  "current_session": {
    "uuid": "…",
    "started_at": "2026-05-17T13:22:11Z",
    "duration_s": 754,
    "distance_m": 942.0,
    "steps": 1450,
    "kcal": 38.2,
    "avg_speed_kmh": 4.4,
    "max_speed_kmh": 5.0,
    "samples": [
      {"ts":"…","speed_kmh":4.5,"steps":1450,"distance_m":942}
    ]
  },
  "today": { "duration_s": 4123, "distance_m": 5430, "steps": 8740, "kcal": 220 },
  "device": { "name":"WalkingPad", "address":"AA:BB:…", "rssi": -56 }
}
```

`samples` is a small ring of the last ~60 s — used by the menu-bar sparkline.

---

## 9. Raycast extension UX

### 9.1 Manifest (commands)

| Command | Mode | Title | Subtitle |
|-|-|-|-|
| `menu-bar` | `menu-bar` (interval `10s`) | live status in menu bar | — |
| `start` | `no-view` | Start | Start belt at default speed |
| `stop` | `no-view` | Stop | Stop belt |
| `set-speed` | `view` (Form) | Set Speed | Pick or enter a speed |
| `speed-up` | `no-view` | Speed Up | +0.5 km/h |
| `speed-down` | `no-view` | Speed Down | −0.5 km/h |
| `today` | `view` (List) | Today's Sessions | Sessions for today with totals |
| `history` | `view` (List) | History | All sessions, searchable |
| `current` | `view` (Detail) | Current Session | Live detail of the open session |

### 9.2 Menu-bar (the daily driver)

**Title** (when belt active):
```
▶ 4.5 · 12:34 · 1.20 km
```
Ultra-compact: play icon, speed, mm:ss, distance. ~22 chars max. When idle:
```
■ idle · today 5.43 km
```
When disconnected:
```
⊘ no link
```

**Dropdown** (on click — instant refresh):
```
Current Session
  ▶ 4.5 km/h     Set speed... ⌘S
  ⏱  12:34
  📍 1.20 km
  👣 1450 steps     Steps/min: 115
  🔥 38 kcal

  Speed up   ⌘↑
  Speed down ⌘↓
  Pause      ⌘P
  Stop       ⌘.

Today
  3 sessions · 1h 08m · 5.4 km · 8.7k steps

Open Argo dashboard ↗
Preferences ⌘,
```

**Refresh:** background `interval: 10s` (Raycast minimum is 10 s). When the menu is open, instant on each click. Sparkline (last 60 s of speeds, Unicode block chars `▁▂▃▄▅▆▇█`) shown if `samples.length > 3`.

### 9.3 Preferences (Raycast settings)

| Field | Type | Default |
|-|-|-|
| `daemon_url` | textfield | `http://127.0.0.1:7706` |
| `api_token` | password (optional) | empty |
| `default_speed_kmh` | textfield | `3.5` |
| `speed_step_kmh` | textfield | `0.5` |
| `argo_dashboard_url` | textfield | `https://argo.jkrumm.com/walking-pad` |

### 9.4 History view

`List` with detail panel. Sections: Today / Yesterday / This Week / Older. Each item shows date+time as title, duration as subtitle, accessories: distance, steps. Detail panel: `Metadata.Label` rows for every aggregate plus a small Markdown body with the speed sparkline.

---

## 10. Argo sync

### 10.1 Argo side (separate PR in `argo` repo)

New domain `apps/api/src/routes/walking-pad.ts`. New Drizzle table:

```typescript
// apps/api/src/db/schema.ts (append)
export const walkingPadSessions = argoSchema.table('walking_pad_sessions', {
  id: serial('id').primaryKey(),
  uuid: text('uuid').notNull().unique(),                 // matches daemon's client uuid
  startedAt: timestamp('started_at', { withTimezone: true }).notNull(),
  endedAt: timestamp('ended_at', { withTimezone: true }).notNull(),
  durationS: integer('duration_s').notNull(),
  distanceM: doublePrecision('distance_m').notNull(),
  steps: integer('steps').notNull(),
  avgSpeedKmh: doublePrecision('avg_speed_kmh').notNull(),
  maxSpeedKmh: doublePrecision('max_speed_kmh').notNull(),
  kcal: doublePrecision('kcal').notNull(),
  pauseCount: integer('pause_count').notNull().default(0),
  source: text('source').notNull().default('walkingpad'),
  createdAt: timestamp('created_at', { withTimezone: true }).notNull().defaultNow(),
});
```

Endpoints (under bearer auth, tag `Walking Pad`):

| Method | Path | Purpose |
|-|-|-|
| `POST /walking-pad/sessions` | upsert by `uuid` (idempotent — daemon may retry) |
| `GET /walking-pad/sessions` | range query, paginated |
| `GET /walking-pad/summary` | aggregates for dashboard cards |

### 10.2 Daemon side

`internal/sync/worker.go` runs a goroutine:

- Wakes on three triggers: (a) `session.close` event channel, (b) every 30 minutes, (c) manual `POST /sync/argo`.
- Queries: `SELECT * FROM sessions WHERE synced_at IS NULL AND ended_at IS NOT NULL`.
- For each: POSTs to argo; on 2xx, sets `synced_at = NOW()`; on 4xx, logs and skips forever; on 5xx/network, backoff and retry next tick.
- Argo token resolved via 1Password reference at startup (e.g. `op://Private/argo/api_token`) — see config section.

---

## 11. Configuration

TOML file at `~/Library/Application Support/WalkingPad/config.toml`. All fields optional with sensible defaults.

```toml
[device]
# If set, prefer this BLE address; else first matching peripheral wins.
address = ""

[daemon]
http_port = 7706
http_token = ""        # if non-empty, required as Bearer on all /api routes
poll_interval_ms = 1000
log_level = "info"     # debug|info|warn|error

[session]
gap_minutes = 15       # how long before a stop counts as a new session
resume_within_seconds = 60   # BLE drop grace window

[body]
weight_kg = 80.0       # used for calorie computation; default fallback

[argo]
url = "https://argo.jkrumm.com/api"
# Either inline token, or 1Password ref resolved by `op` at daemon startup:
# token = "Bearer abc…"
# op_token_ref = "op://Private/argo/api_token"
```

Resolution order: env vars (`KSWP_*`) override TOML override defaults.

---

## 12. macOS Bluetooth permission — critical

CoreBluetooth on macOS 11+ requires the calling process to be a bundled `.app` with `Info.plist` keys `NSBluetoothAlwaysUsageDescription` and `NSBluetoothPeripheralUsageDescription`. **A bare CLI binary placed anywhere on `$PATH` (`/opt/homebrew/bin`, `/usr/local/bin`, etc.) will be silently denied access — `Scan()` returns no devices and there is no error.** Verified in tim-oster's repo: their build pipeline wraps the Go binary in a `.app` bundle.

**Therefore: we do not install a bare binary at all.** The only install target is `/Applications/WalkingPad.app`. The Go binary lives at `Contents/MacOS/walkingpad` inside the bundle, and is invoked directly from there by the LaunchAgent. No symlinks to `$PATH`. Users who want a CLI from Terminal can alias to the bundled binary path themselves.

### Deployment

- `make install` builds `WalkingPad.app` via `scripts/build-app-bundle.sh`, copies it to `/Applications/`, and writes a LaunchAgent plist that invokes `/Applications/WalkingPad.app/Contents/MacOS/walkingpad`.
- First launch triggers the macOS Bluetooth-permission prompt. User must accept once.
- The `.app` is unsigned in v1 (Gatekeeper requires user to right-click → Open the first time). Code-signing/notarisation is a v2 polish item.

### Plist template (`Info.plist.tmpl`)

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key>          <string>com.jkrumm.walkingpad</string>
  <key>CFBundleName</key>                <string>King-Smith-WalkingPad-Mac</string>
  <key>CFBundleExecutable</key>          <string>king-smith-walkingpad-mac</string>
  <key>CFBundleVersion</key>             <string>__VERSION__</string>
  <key>CFBundleShortVersionString</key>  <string>__VERSION__</string>
  <key>LSUIElement</key>                 <true/>   <!-- background-only, no Dock icon -->
  <key>LSMinimumSystemVersion</key>      <string>11.0</string>
  <key>NSBluetoothAlwaysUsageDescription</key>
    <string>King-Smith-WalkingPad-Mac connects to your WalkingPad over Bluetooth to control the belt and record sessions.</string>
  <key>NSBluetoothPeripheralUsageDescription</key>
    <string>King-Smith-WalkingPad-Mac connects to your WalkingPad over Bluetooth to control the belt and record sessions.</string>
</dict>
</plist>
```

---

## 13. LaunchAgent

`scripts/com.jkrumm.walkingpad.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>     <string>com.jkrumm.walkingpad</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Applications/WalkingPad.app/Contents/MacOS/walkingpad</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>      <true/>
  <key>KeepAlive</key>      <true/>
  <key>StandardOutPath</key><string>/tmp/walkingpad.log</string>
  <key>StandardErrorPath</key><string>/tmp/walkingpad.err</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key><string>__HOME__</string>
  </dict>
</dict>
</plist>
```

---

## 14. Testing strategy

- **Unit tests** are the default. Every package has `_test.go`.
  - `ble/frames`: table-driven, ~30 fixtures from ph4 docs + tim-oster's test vectors. Goal: 100% coverage of encoder/decoder + CRC.
  - `session/manager`: feed synthetic frame sequences, assert session boundaries, pauses, resume.
  - `store`: ephemeral in-memory SQLite per test; CRUD + aggregation queries.
  - `api/handlers`: `httptest`, mock BLE client + store.
  - `sync/worker`: `httptest.Server` standing in for Argo.
- **Integration test:** `make test-ble` (gated by `KSWP_BLE_INTEGRATION=1` env var) runs against a real P1 — discovers, connects, polls 5 s, asserts non-zero status, disconnects. Skipped by default in CI.
- **Linting:** `golangci-lint` with `errcheck`, `gosec`, `govet`, `ineffassign`, `staticcheck`, `unconvert`, `unparam`, `revive`. Config in `.golangci.yml`.
- **Formatting:** `gofmt -s` enforced via `make fmt`; `go vet` part of `make test`.

---

## 15. Milestones

### Milestone 0 — POC (current focus)

Crack the P1 BLE protocol end-to-end. No persistence, no HTTP, no Raycast.

- `cmd scan` subcommand: discover and print WalkingPad devices.
- `cmd connect`: connect, dump every status frame to stdout for 30 s, disconnect cleanly.
- `cmd start --speed 3.0 --duration 20s`: start belt, run for 20 s, stop, disconnect.
- `frames` package complete, unit-tested.
- Verified macOS .app bundle + Bluetooth permission flow.

**Exit criterion:** I can run `make build && ./bin/walkingpad connect` and see live frames stream from my P1 for ≥60 s with no drops.

### Milestone 1 — MVP

Everything from POC plus:

- `serve` subcommand (HTTP API on 7706, session manager, SQLite store).
- LaunchAgent installable via `make install`.
- Auto-reconnect on BLE drop.
- Minimal Raycast extension: menu-bar + start/stop/set-speed/today.
- Argo route + Drizzle migration (separate PR in argo repo).
- Argo sync worker.

**Exit criterion:** Daily use replaces the remote control. Sessions land in Argo automatically.

### Milestone 2 — Polish

- History view in Raycast with detail panels and search.
- Speed sparkline in menu bar.
- Preferences editing UI (set start-speed, max-speed, sensitivity).
- Smarter calorie formula (Compendium-of-Physical-Activities MET values).
- Code-signed/notarised `.app`.
- Curl-install script for non-Go-installing users.
- README screenshots + GIF.

### Milestone 3 — Maybe-laters

- Raycast Store publication.
- Apple HealthKit export (would require a HealthKit-capable macOS app — non-trivial).
- Web dashboard inside Argo's dashboard app showing all-time walking-pad stats.
- Hermes integration (Slack `/walk start 3.5` from phone).
- Multi-device profile support (track other family members' sessions).

---

## 16. Open questions & risks

1. **Bluetooth permission on LaunchAgent:** does the `.app`-bundled binary actually receive the prompt when invoked by `launchd`, or does macOS suppress it? Tim-oster's app is launched manually by the user, not by launchd. **Verify in POC.** Fallback: instruct the user to launch the app once interactively before enabling the LaunchAgent.

2. **Single BLE owner:** if both the daemon and KS Fit on a phone connect to the P1, they may conflict. We're not the only client. Document this; don't try to multiplex.

3. **Calorie accuracy:** the MET formula is rough. Acceptable for v1 — users who care about precision use Garmin. Confirm in user testing that the numbers feel plausible.

4. **Session-grouping edge case:** what if you actively stop the session (via Stop command) but plan to resume in 5 minutes? Stop is a user intent; should we override the 15-min rule? **Proposed:** add a `?force=true` flag on `POST /stop` that immediately closes the session; default Stop respects the grace window.

5. **Step-counter reset behavior on PAUSED→ACTIVE:** ph4 docs imply counters survive a pause. **Verify in POC** by pausing for 30 s and watching whether steps stay or jump back.

6. **Time-zone handling for `started_at`:** store UTC, render local. Argo expects UTC ISO-8601 with `Z` suffix.

7. **Argo schema migration:** the new table is additive; safe to deploy independently of daemon. Daemon should degrade gracefully if argo isn't reachable (queue locally, sync later).

8. **Multiple WalkingPads in range:** if two are advertising, the daemon picks the first. Once `config.device.address` is set, it pins. **POC task:** scan should print all candidates with RSSI so the user knows which to pin.

---

## 17. Implementation handover (for a fresh-context agent)

**You are the implementation agent.** A planning session already produced this PRD, the [`CLAUDE.md`](./CLAUDE.md), and the repo skeleton. Your job is to take the project from skeleton to working POC, then MVP, in disciplined steps. This section is your operating manual.

### 17.1 First things to do in your first session

1. **Read in this order:** [`CLAUDE.md`](./CLAUDE.md) (5 min — critical gotchas), then this PRD §1–4 and §11–16 (skim §5–10, refer back as needed).
2. **Clone the inspiration repos** if they're not at `/tmp/walkingpad-research/` (they were cloned during planning; `/tmp` may have been wiped):
   ```bash
   mkdir -p /tmp/walkingpad-research
   git clone https://github.com/ph4r05/ph4-walkingpad.git    /tmp/walkingpad-research/ph4
   git clone https://github.com/tim-oster/walkingpad.git     /tmp/walkingpad-research/tim-oster
   git clone https://github.com/mcdax/walkingpad-controller.git /tmp/walkingpad-research/mcdax
   ```
3. **Sanity check the build:** `make build && make help`. Should produce `./bin/walkingpad` and print 17 targets.
4. **Check git state:** `git status` and `git log --oneline -5`. The initial commit should contain the skeleton + docs.

### 17.2 Operating principles (non-negotiable)

These are inherited from the user's global `~/.claude/CLAUDE.md` and reinforced here because the long-running nature of this work makes them easy to forget:

- **Scope discipline.** Implement only what the current milestone requires. Do not pre-build Milestone 2 polish during Milestone 0. The user explicitly hates speculative additions.
- **Wait for the user on big direction calls.** Implementation details are yours; architectural shifts (e.g. "should we change BLE library?") are the user's. When uncertain, surface 2 options + your tendency + ask, per global guidance.
- **Commit per logical unit.** Use `/commit` (see global skills). Never bundle the BLE frames work with the SQLite work in one commit. Conventional Commits prefix.
- **No AI attribution anywhere.** Not in commits, not in PR descriptions, not in code comments. Global rule.
- **English artifacts, even if the user chats in German.** Code, comments, commits, docs — all English.
- **Senior-to-senior tone in responses.** No "great question!", no superlatives, no narration of your own thinking.
- **Read CLAUDE.md before edits in unfamiliar areas.** Especially the "Critical gotchas" and "Don'ts" sections.

### 17.3 The POC plan — Milestone 0

The single exit criterion: `./bin/walkingpad connect` streams live status frames from the actual P1 for ≥60 seconds with no drops, on the user's Mac (Apple Silicon, macOS 26). Do not declare the POC done until this is verified on hardware.

Ordered steps, each its own commit:

**Step 1 — Frames package (pure logic, no hardware).**
- Implement `internal/ble/frames.go` with:
  - Typed enums: `BeltState`, `Mode`, `PrefKey`.
  - `Encode*` functions for every command in PRD §4.3.
  - `DecodeStatus(buf []byte) (Status, error)` for the 20-byte status frame.
  - `crc(buf []byte) byte` implementing `sum(buf[1:len(buf)-2]) & 0xFF`.
- Table-driven tests in `frames_test.go` using the fixtures in PRD §4.4 (the worked example: `f8 a2 01 0f 01 00 0f d1 00 00 ab 00 12 ae 3c 00 00 00 3a fd` decodes to state=ACTIVE, speed=1.5, mode=manual, time=4049 s, distance=1710 m, steps=4782).
- Add at least one fixture per command opcode.
- 100% coverage on this package is realistic and required.
- `make test` must pass. Commit as `feat(ble): add WiLink frame codec`.

**Step 2 — `.app` bundle build script (deployment foundation).**
- Write `scripts/Info.plist.tmpl` per PRD §12.
- Write `scripts/build-app-bundle.sh` that:
  1. Takes a built binary path and a version string.
  2. Creates `WalkingPad.app/Contents/{MacOS,Resources}`.
  3. Substitutes `__VERSION__` into `Info.plist`.
  4. Copies the binary into `Contents/MacOS/walkingpad`.
- Add a `make build-app` target that runs `build` then `build-app-bundle.sh`.
- Smoke-test: `make build-app && open WalkingPad.app` — the binary should be invoked. (It'll print the skeleton stderr line and exit; that's fine.)
- Commit as `feat(scripts): add .app bundle build for Bluetooth permission`.

**Step 3 — BLE scan subcommand.**
- Add `internal/ble/client.go` with:
  - `Scan(ctx, timeout)` using `tinygo.org/x/bluetooth`. Filter by service UUID `0xFE00` AND name containing "walkingpad" (case-insensitive). Return discovered peripherals + RSSI.
- Add `scan` subcommand in `cmd/king-smith-walkingpad-mac/main.go` that prints the table.
- `go get tinygo.org/x/bluetooth@v0.10.0` (verify current version in the inspiration tim-oster repo's `go.mod` before pinning).
- **First hardware test:** `make build-app && /Applications/WalkingPad.app/Contents/MacOS/walkingpad scan`. The macOS Bluetooth permission prompt should appear. Accept it. The P1 should appear.
  - If no devices appear and no prompt: check System Settings → Privacy & Security → Bluetooth. Confirm the `.app` is listed. If not listed: re-build, re-run.
  - If still nothing: this is a major risk per PRD §16 Q1 — **stop and escalate to the user**. Do not try increasingly elaborate workarounds.
- Commit as `feat(ble): add device scan command`.

**Step 4 — Connect + decode loop.**
- Extend `client.go`:
  - `Connect(ctx, addr)` returns a connected client.
  - `Subscribe(ctx, onFrame func(Status))` enables notifications on `0xFE01` and calls back on each decoded status frame.
  - `Write(ctx, frame []byte)` enforces a 700 ms minimum gap between writes.
- Add `connect` subcommand: connect to the first/configured device, send `ask_stats` once per second, print every decoded status frame to stderr, exit cleanly on Ctrl-C.
- **Hardware test:** run on the user's P1 for 60 s, walking and stopping. Confirm: frames arrive, speed/distance/steps change as expected, CRCs all valid, no decode errors logged.
- Commit as `feat(ble): add live connect+poll command`.

**🚦 POC exit gate.** If the hardware test in Step 4 passes for ≥60 s without drops, the POC is done. Report back to the user with: time-to-first-frame, average frame interval, total frames received, any anomalies. Wait for user sign-off before starting Milestone 1.

### 17.4 Hardware-in-the-loop policy

You cannot run the BLE integration tests in your own session — they require the user's physical hardware and active walking. The protocol guarantees you can verify cover ~80 % of correctness; the remaining 20 % is what the device actually does.

When you reach a step that needs hardware:
1. Make the artifact runnable (binary builds, `.app` bundles).
2. Write a precise test script the user can run (`make build-app && open … && tail -f …`).
3. Tell the user exactly what success looks like ("you should see a frame line per second for 60 s, all CRCs match, distance increases monotonically while you walk").
4. Wait for the result. Don't keep coding in the meantime — your next decision depends on what the hardware did.

### 17.5 After the POC — Milestone 1 sequence

Only start after explicit user OK. Each step is one commit-cluster (use `/commit --split` if it touches multiple concerns):

1. **Config + logger.** TOML loader, slog wiring. `internal/config`, `internal/logger`.
2. **SQLite store + migrations.** Schema per PRD §6. Embedded SQL files. CRUD + the aggregation queries needed for `/summary`.
3. **Session manager.** State machine per PRD §7. Synthetic-frame unit tests.
4. **HTTP API.** Handlers per PRD §9. `httptest`-based handler tests.
5. **Reconnect logic.** Exponential backoff, scan-and-rebind. Hardware test: turn off the P1 and back on; confirm daemon reconnects within ~10 s.
6. **`serve` subcommand.** Wires BLE + session + store + HTTP into one process. Signal handlers for clean shutdown.
7. **LaunchAgent.** `make install-agent` + `make uninstall-agent` end-to-end. **Critical risk:** verify Bluetooth permission survives the launchd invocation (PRD §16 Q1). If launchd-launched binary loses permission, fall back to the documented workaround (user launches `.app` interactively once first).
8. **Argo schema + route.** Separate PR in `~/SourceRoot/argo`. Use the existing `weight-log` route as the template. Commits in argo never bundle with daemon commits.
9. **Argo sync worker.** `internal/sync`. `httptest.Server` for unit tests. Wire up the `op` token resolution (see CLAUDE.md "Argo integration").
10. **Raycast extension v1.** Menu-bar + start/stop/set-speed/today. Use `~/SourceRoot/ticktick-raycast` as the structural template (per the planning session's survey).

Milestone 2 (polish) and beyond live in PRD §15 — re-read when you get there.

### 17.6 Tooling shortcuts

- **`/research <query>`** — sideclaw MCP for tech research. Cached versions, cross-verification. Use this before pulling in any new dependency or before re-implementing protocol details from memory. Don't trust your training data on library versions.
- **`/check`** — runs format + lint + typecheck + test via sideclaw. Run before every commit.
- **`/commit`** — conventional commits. Use `--split` when a single change touches multiple concerns.
- **`/ship`** — full flow when ready to merge (check → review → commit → PR if needed → merge → release).
- **Explore subagent** — for "where is X defined" questions. Faster and cheaper than spawning a full general-purpose agent.

### 17.7 When to ask the user

Ask:
- Before changing the architecture documented in this PRD.
- Before adding a new top-level dependency (e.g. swapping BLE library).
- When a hardware test fails in a way that suggests a protocol assumption is wrong.
- When you've completed a milestone.
- When you're stuck for >15 minutes on the same problem.

Don't ask:
- Routine implementation choices (variable names, file organization, what package to put a helper in).
- Whether to run tests. Just run them.
- Whether to format code. Just format.
- Whether to commit a clean unit of work. Commit it.

### 17.8 What the planning session already verified

So you don't re-do it:

- **Protocol** is fully reverse-engineered and documented in PRD §4. Cross-verified against three independent implementations. Trust it, but unit-test against the fixtures.
- **`tinygo.org/x/bluetooth`** is the right Go BLE library — verified on P1 + macOS in tim-oster's repo.
- **Garmin integration is a deliberate non-goal.** Don't second-guess this. The user wants manual "Indoor Walking" on the watch; we own the desktop telemetry.
- **`garth` Python library is deprecated** (March 2026). Even if you somehow start thinking about Garmin: don't use garth.
- **`cyberjunky/python-garminconnect` v0.3.4** is the current Garmin library — irrelevant for this project, noted only so you don't re-research it.
- **macOS Bluetooth permission needs the `.app` bundle.** Bare binary = silent denial. This is the single biggest deployment gotcha.
- **`darnfish/walkingpad`** (Node/TS) is not viable — too immature, P1 unverified. Don't get tempted into a Node rewrite.

### 17.9 Stop conditions

Stop and ask the user if:
- The BLE permission story turns out to be more painful than documented (e.g. `.app` bundle doesn't help on the user's macOS version).
- The protocol doesn't match the spec on the actual P1 (e.g. distance is in different units, status frame is a different length).
- A core dependency (`tinygo.org/x/bluetooth`, `modernc.org/sqlite`) doesn't compile or work on Apple Silicon.
- You're more than 60 minutes deep into something and feel like the design is fighting you.

The right move at any of those points is one good question to the user, not three increasingly heroic workarounds.

---

## 18. Glossary

| Term | Meaning |
|-|-|
| **P1** | KingSmith WalkingPad P1 model (0.5–6 km/h, foldable, indoor) |
| **WiLink** | Legacy proprietary BLE protocol used by older WalkingPad models (incl. P1) |
| **FTMS** | Bluetooth SIG standard Fitness Machine Service (used by newer KingSmith — not P1) |
| **GATT** | Generic Attribute Profile — BLE's service/characteristic abstraction |
| **MET** | Metabolic Equivalent of Task — energy-cost unit for calorie computation |
| **Argo** | Personal API + dashboard server (separate repo `~/SourceRoot/argo`) |
| **Session** | Continuous walking activity, with pauses ≤15 min folded in |
| **Sample** | A single status frame, polled at ~1 Hz |
