# WalkingPad protocol, schema and session logic

*Harvested from `PRD.md` (deleted — it was a 2026-05-17 plan doc; this is the durable*
*reference half: the BLE protocol, the SQLite schema and the session-grouping rule).*

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
| Last record | `F7 A7 AA FF 50 FD` | Request last-run record. Device replies with a single type-0xA7 status frame (see `ph4r05/ph4-walkingpad` `WalkingPadLastStatus`). |

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
| 16 | 1 | button | physical remote — see Button values below |
| 17 | 1 | reserved | always 0x00 on observed P1 |
| 18 | 1 | CRC | `sum(buf[1:18]) & 0xFF` |
| 19 | 1 | terminator | `0xFD` |

> CRC offset corrected from the original ph4r05 docs (which listed offset 17). The verified scope is `sum(buf[1:len(buf)-2])`; on a 20-byte status frame that puts the CRC at offset 18.

**Belt state values (verified on user's P1, 2026-05-17):**

| Byte | State | Notes |
|-|-|-|
| `0x00` | STOPPED | idle; counters reset to 0 immediately on entry |
| `0x02` | ACTIVE | running; counters monotonic. **The only state called "ACTIVE".** |
| `0x04` | STOPPING | ~1-frame decel window; counters still hold the run total — **last chance** to capture session stats |
| `0x05` | STANDBY | belt powered down (preserved from ph4 docs; not yet verified on user's P1) |
| `0x07` | STARTING (1) | 3rd ramp frame; next frame is ACTIVE |
| `0x08` | STARTING (2) | middle ramp frame |
| `0x09` | STARTING (3) | first frame after start press |

> **Original ph4r05 table (stale).** The ph4r05 docs list `0x01=ACTIVE`, `0x02=PAUSED`, `0x09=STARTING` with no entries for `0x04 / 0x07 / 0x08`. On current P1 firmware byte `0x01` is never emitted, byte `0x02` is active (not paused), and start press cycles `0x09 → 0x08 → 0x07 → 0x02` over three seconds (matches the 3-2-1 countdown on the LED display). Stop press cycles `0x02 → 0x04 → 0x00`. The Go enum in `internal/ble/frames.go` reflects the verified mapping and exposes `IsStarting()` / `IsRunning()` helpers; `IsRunning()` is true for both `0x02` and `0x04` so the session manager doesn't lose the final decel sample.

**Button values:**

| Byte | Button | Notes |
|-|-|-|
| `0x00` | none | most common |
| `0x02` | up (+) | speed increase |
| `0x03` | power | start AND stop (single physical button on the P1 remote) |
| `0x04` | down (−) | speed decrease |

> Bit `0x80` of the button byte is a held-press modifier (observed empirically as `0x83` = power held). The decoder masks the high bit before assigning, so callers compare only against the table above. The raw byte is preserved at `Status.Raw[16]` for diagnostics.

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
| `POST /start` | `{"speed_kmh":3.5}` (optional) | `{"ok":true}` | Switches to manual mode, starts the belt, **waits out the 3-2-1 ramp, then applies the speed** — the P1 drops set-speed sent while STOPPED/ramping and would settle at its stored START_SPEED pref otherwise, so the request blocks ~4-5 s. Idempotent: when the belt is already active or ramping, /start never re-sends the raw start frame (that halts a running P1) — it just waits for ACTIVE and sets speed (or no-ops if no speed given). |
| `POST /stop` | — | `{"ok":true}` | `set_speed(0)`. |
| `POST /speed` | `{"speed_kmh":4.0}` | `{"ok":true}` | 0.5–6.0 inclusive; rounded to 0.1. Same start-then-bump rule as /start: from a STOPPED belt it starts the belt, waits for ACTIVE, then applies the speed rather than silently dropping the frame. |
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

## macOS Bluetooth permission and deployment


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

