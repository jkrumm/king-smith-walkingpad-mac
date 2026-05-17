# king-smith-walkingpad-mac

A macOS daemon and Raycast extension for the KingSmith **WalkingPad P1**. Control the belt, track every session, own your data.

> **Status:** early development. Milestone 0 (BLE proof-of-concept) in progress. Hardware tested: WalkingPad P1 on macOS 26 (Apple Silicon).

<!-- Screenshots will live here once the Raycast UI is working.
     Suggested shots: menu bar with live session, history list, set-speed form. -->

## Why

The official KS Fit app and the hardware remote are fine for casual use, but they leave nothing behind: no machine-readable history, no desktop control surface, no way to plug walking data into a personal dashboard. This project gives you all three.

- **Live control from Raycast** — start, stop, change speed without bending down to the remote.
- **Menu-bar status** — current speed, distance, time, steps glanceable from anywhere on macOS.
- **Local-first history** — every session in a local SQLite database. Offline-tolerant. Yours forever.
- **Optional sync** to a private API (Argo, in this author's case) for dashboards and downstream agents.

Explicit non-goals: no Garmin upload (Garmin Connect can't update the watch's daily step count from manual activities — start "Indoor Walking" on the watch if you want that), no Apple Health export in v1, no FTMS support (P1 doesn't speak it).

## Features

- BLE control: start, stop, set speed (0.5–6.0 km/h), set default start-speed.
- Auto-reconnect on Bluetooth drops with exponential backoff.
- Smart session grouping: pauses up to 15 minutes (coffee, bathroom) stay in the same session; longer gaps start a new one.
- Live telemetry at ~1 Hz: speed, distance, steps, belt state.
- Calorie computation client-side using a MET formula (configurable body weight).
- Local SQLite store at `~/Library/Application Support/king-smith-walkingpad-mac/db.sqlite`.
- Loopback HTTP API on `127.0.0.1:7706`.
- Raycast extension: menu bar + commands for start/stop/speed/history.
- Optional sync to a personal API (Argo).

## Requirements

| | |
|-|-|
| Hardware | KingSmith WalkingPad P1 (or any WiLink-protocol model: A1, A1 Pro, C1, C2) |
| OS | macOS 11+ on Apple Silicon or Intel |
| Go | 1.26 or newer (`brew install go`) |
| Make | macOS default |
| Raycast | optional but recommended (`brew install raycast`) |
| Node | optional, only for Raycast dev (`brew install node`) |

## Install

```bash
git clone https://github.com/jkrumm/king-smith-walkingpad-mac.git
cd king-smith-walkingpad-mac
make up
```

`make up` does everything:

1. Builds the Go binary.
2. Wraps it into `WalkingPad.app` (required — macOS denies Bluetooth access to bare CLI binaries).
3. Copies the `.app` to `/Applications/`.
4. Installs and starts the LaunchAgent at `~/Library/LaunchAgents/com.jkrumm.walkingpad.plist` (only on first run; subsequent runs just kickstart it).
5. Hits `/health` and prints `daemon source` vs `daemon live` so the version match is obvious.
6. Starts the Raycast dev loop (`ray develop`).

The first run will prompt macOS to grant Bluetooth permission. Accept the prompt. If you miss it, grant it manually in **System Settings → Privacy & Security → Bluetooth**.

Re-run `make up` after every code change. It's the only command you need for the dev loop. The Raycast portion requires Node ≥22.22.2 (pinned in `raycast/.nvmrc`); if you don't have it the daemon still deploys and `make up` exits early with a friendly nudge.

## Configuration

Edit `~/Library/Application Support/king-smith-walkingpad-mac/config.toml`:

```toml
[device]
address = ""             # optional BLE address pin — leave empty to auto-discover

[daemon]
http_port = 7706
log_level = "info"       # debug | info | warn | error

[session]
gap_minutes = 15
resume_within_seconds = 60

[body]
weight_kg = 80.0         # used for calorie computation

[argo]
url = ""                 # leave empty to disable Argo sync
# token = "Bearer …"
```

All fields optional. Without `argo.url` set, sessions stay local — you still get full control and history, just no remote sync.

After editing, redeploy the daemon:

```bash
make up
```

## Usage

### From Raycast

- **Menu bar** — always-on glanceable status. `▶ 4.5 · 12:34 · 1.20 km` while walking; `■ idle · today 5.43 km` between sessions.
- **Start / Stop** — quick keyboard commands.
- **Set Speed** — form with current speed pre-filled.
- **Today's Sessions** — list of today's walks with totals.
- **History** — full searchable history with per-session detail panels.

### From the CLI (development & debugging)

```bash
./bin/king-smith-walkingpad-mac scan          # list nearby WalkingPad devices
./bin/king-smith-walkingpad-mac connect       # connect, stream live frames, exit on Ctrl-C
./bin/king-smith-walkingpad-mac status        # query running daemon's status
./bin/king-smith-walkingpad-mac serve         # foreground daemon (what the LaunchAgent runs)
```

### From the HTTP API

```bash
curl http://127.0.0.1:7706/status
curl -X POST http://127.0.0.1:7706/start -d '{"speed_kmh":3.5}'
curl -X POST http://127.0.0.1:7706/speed  -d '{"speed_kmh":4.5}'
curl -X POST http://127.0.0.1:7706/stop
curl http://127.0.0.1:7706/sessions?limit=10
curl http://127.0.0.1:7706/summary?period=week
```

Full API contract in [`PRD.md`](./PRD.md) §9.

## Architecture

A single Go process owns the BLE link (CoreBluetooth permits only one GATT connection per peripheral) and exposes everything else over a loopback HTTP API. Raycast and any other consumer talks to the API, never to the device directly.

```
Raycast extension ─HTTP→ Go daemon ─BLE→ WalkingPad P1
                              │
                              ├─→ SQLite (local source of truth)
                              └─→ Argo API (optional, async sync)
```

See [`PRD.md`](./PRD.md) for the full architecture, BLE protocol spec, and design decisions.

## Troubleshooting

| Symptom | Cause / fix |
|-|-|
| `make scan` finds no devices | macOS Bluetooth permission. Check **System Settings → Privacy & Security → Bluetooth**. The `.app` must be listed and enabled. |
| Daemon connects but no status frames | Device is in standby. Press a button on the remote to wake it, then retry. |
| "Address already in use" | Another instance of the daemon is running. `launchctl unload ~/Library/LaunchAgents/com.jkrumm.walkingpad.plist && make up`. |
| Belt ignores speed commands | Wire format is `speed × 10` — bug in your custom client. Use the API. |
| Sessions don't show up in Argo | Check `[argo]` config and `make logs`. Sync runs every 30 min plus on each session close; manual trigger: `curl -X POST http://127.0.0.1:7706/sync/argo`. |
| BLE drops mid-session | Auto-reconnect kicks in for 60 s. Longer drops close the session at the drop time. Tune `[session].resume_within_seconds`. |

## Development

```bash
make            # default — show the menu
make up         # rebuild daemon + deploy + start Raycast dev (the only command you need)
make test       # go test -race + raycast tsc --noEmit
make logs       # tail /tmp/walkingpad.log
make lint       # golangci-lint
make fmt        # gofmt + go mod tidy
make scan       # BLE device discovery (dev tool)
make clean      # remove ./bin
```

Read [`CLAUDE.md`](./CLAUDE.md) before extending. It captures the non-obvious gotchas (BLE rate limit, CRC scope, `.app` bundle requirement, etc.).

## Credits

This project would not exist without the reverse-engineering work of:

- [**ph4r05/ph4-walkingpad**](https://github.com/ph4r05/ph4-walkingpad) — the canonical Python reference implementation. All protocol knowledge ultimately traces back here.
- [**tim-oster/walkingpad**](https://github.com/tim-oster/walkingpad) — Go desktop controller, explicitly verified on the P1 and macOS Apple Silicon. The build-script template for the `.app` bundle is adapted from this project.
- [**mcdax/walkingpad-controller**](https://github.com/mcdax/walkingpad-controller) — unified WiLink + FTMS Python wrapper with the most complete protocol documentation in `docs/`.

## License

[AGPL-3.0](./LICENSE).
