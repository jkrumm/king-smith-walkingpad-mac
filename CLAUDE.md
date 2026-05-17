# CLAUDE.md — king-smith-walkingpad-mac

> **Naming.** The repo and Go module keep `king-smith-walkingpad-mac` for SEO.
> Everything user-facing is `WalkingPad` (display) / `walkingpad` (CLI binary,
> identifier, log paths). The `cmd/king-smith-walkingpad-mac/` directory matches
> the repo; its compiled output is `bin/walkingpad`, installed to
> `/Applications/WalkingPad.app/Contents/MacOS/walkingpad`.

Per-project guidance for Claude Code. The global config at `~/.claude/CLAUDE.md` applies; this file adds project-specific rules.

## What this project is

A macOS LaunchAgent (Go) + Raycast extension that controls and tracks the KingSmith **WalkingPad P1** treadmill over BLE. Local SQLite is the source of truth; completed sessions sync to the personal **Argo** API. No Garmin integration (deliberate — see PRD §1 non-goals). License **AGPL-3.0**.

**Read [`PRD.md`](./PRD.md) first.** It is the source of truth for architecture, BLE protocol, schema, API contract, and milestones. This file is the operator's manual for Claude.

## Stack

| Layer | Choice | Why |
|-|-|-|
| Daemon language | **Go 1.26+** | Single binary, easy LaunchAgent, native macOS Bluetooth via cgo |
| BLE library | **`tinygo.org/x/bluetooth` v0.10.0** | Only cross-platform Go BLE lib; verified on P1 by tim-oster |
| SQLite driver | **`modernc.org/sqlite`** | Pure Go (no CGO) — keeps cross-compile easy |
| HTTP | **stdlib `net/http`** | Loopback only, no framework needed |
| Logging | **`log/slog`** → JSONL on disk + pretty on stderr | Standard library, structured |
| Config | **TOML** via `github.com/BurntSushi/toml` | Human-editable |
| Raycast extension | **TypeScript** with `@raycast/api` + `@raycast/utils` | Standard Raycast stack |

## Critical gotchas (read every time you touch these areas)

1. **macOS Bluetooth requires a `.app` bundle.** A bare binary anywhere on `$PATH` (`/opt/homebrew/bin`, `/usr/local/bin`, etc.) is *silently denied* by CoreBluetooth — `Scan()` just returns nothing. The daemon must run from inside `WalkingPad.app` with `Info.plist` declaring `NSBluetoothAlwaysUsageDescription` + `NSBluetoothPeripheralUsageDescription`. We do NOT install a bare binary anywhere — the `.app` at `/Applications/` is the only install target. See PRD §12.
2. **BLE write rate limit.** Device drops frames if commands arrive faster than ~1.4 Hz. **Enforce a 700 ms minimum gap** between any writes (the `frames.go` writer owns this). Status polls and user commands share the same write channel.
3. **CRC scope.** `sum(buf[1:-2]) & 0xFF` — exclusive of start byte AND checksum slot AND terminator. Get this wrong and the device silently ignores commands. Always test against the fixtures in `ble/frames_test.go`.
4. **Single BLE owner.** macOS allows only one GATT connection per peripheral. The daemon is the sole BLE client. Raycast / CLI / argo all go through the HTTP API. Never add a second BLE client.
5. **Speed encoding.** Wire format is `speed_kmh × 10` as a single byte (uint8). Range 0–60. Always validate the input before encoding.
6. **Distance encoding.** Wire format is uint24 BE in units of **10 m**. Divide raw by 100 for km, by 10 for metres.
7. **Calories are NOT in the status frame.** Compute client-side using the MET formula (`session/calories.go`). User weight from config.
8. **Session counters reset on device STANDBY.** When the belt powers down, the next session starts at 0. The `session.Manager` handles cross-session aggregation; do not assume monotonic counters across BLE reconnects.

## Reference implementations (cloned during research)

These three repos were cloned into `/tmp/walkingpad-research/` during the planning phase. They are not in this repo. Use them as reference only — re-clone if `/tmp` is wiped:

```bash
git clone https://github.com/ph4r05/ph4-walkingpad.git    /tmp/walkingpad-research/ph4
git clone https://github.com/tim-oster/walkingpad.git     /tmp/walkingpad-research/tim-oster
git clone https://github.com/mcdax/walkingpad-controller.git /tmp/walkingpad-research/mcdax
```

- **`ph4r05/ph4-walkingpad`** — canonical Python implementation, source of all protocol knowledge. Read `pad.py` for the frame layout.
- **`tim-oster/walkingpad`** — Go implementation explicitly verified on P1 + macOS M1. The closest analogue to what we're building. Read `walkingpad.go` for the tinygo-bluetooth usage pattern and the `.app`-bundle build script.
- **`mcdax/walkingpad-controller`** — Python wrapper that unifies WiLink + FTMS. Its `docs/ks-fit-reverse-engineering.md` is the most complete protocol reference.

## Layout

```
cmd/king-smith-walkingpad-mac/main.go    # CLI entrypoint; compiled to `walkingpad`
internal/ble/                            # BLE client, frame codec, reconnect
internal/session/                        # Session state machine, calorie calc
internal/store/                          # SQLite + migrations
internal/api/                            # HTTP server (localhost:7706)
internal/sync/                           # Argo upload worker
internal/config/                         # TOML config
internal/logger/                         # slog setup
raycast/                                 # TypeScript Raycast extension
scripts/                                 # plist, Info.plist.tmpl, build-app-bundle.sh
```

Full diagram and per-file responsibilities in PRD §5.

## Devloop

```bash
make build               # → ./bin/walkingpad
make test                # go test -race
make test-cover          # coverage report
make fmt                 # gofmt + go mod tidy
make lint                # golangci-lint (brew install golangci-lint)
make run                 # build + run in foreground
make scan                # dev tool: list nearby WalkingPad devices
make install-agent       # install LaunchAgent plist (after build-app-bundle)
make reload              # kickstart the agent
make logs                # tail /tmp/walkingpad.log
make raycast-dev         # cd raycast && ray develop
```

For BLE work, prefer `make run` over the LaunchAgent (faster iteration, foreground stderr).

## Coding conventions

- **Strong typing.** No `interface{}` / `any` unless justified at a true protocol boundary. Use typed enums (`type BeltState uint8`) for everything that maps to a fixed wire value.
- **Table-driven tests.** Especially in `ble/frames` — every opcode + every fixture from the PRD goes in a table.
- **Errors are returned, not logged-and-swallowed.** Wrap with `fmt.Errorf("…: %w", err)`. Log at the top of the call stack.
- **No goroutine without a context.** Every long-running goroutine takes a `ctx context.Context` and respects cancellation. The daemon's `serve` command wires a single root context with signal handlers.
- **Atomic state.** Shared state (last status frame, current session) is owned by a single goroutine; other readers go through a channel or a mutex-guarded accessor. No "just one quick mutex" sprinkled in handlers.
- **No global state** outside `main.Version`.
- **Comments only where the why isn't obvious.** Don't restate `setSpeed` does what its name says. Document the reasoning behind the 700 ms gap, the `0x80` ack semantics, the 15-min session rule. Per global rules: no AI/tool attribution anywhere.

## Argo integration

- Argo lives at `~/SourceRoot/argo`. New domain goes in `apps/api/src/routes/walking-pad.ts`. New Drizzle table `walking_pad_sessions` in `apps/api/src/db/schema.ts`. Follow the `weight-log` domain as the template (single file per domain, no controllers/services).
- API base URL: `https://argo.jkrumm.com/api`. Bearer auth via `API_SECRET`.
- Token resolution in the daemon: env `KSWP_ARGO_TOKEN` overrides `argo.token` in config TOML; if neither present, attempt `op_token_ref` via `op read --account tkrumm`.

Per global rules: argo changes happen as a **separate PR** in the argo repo, never bundled with daemon commits.

## Don'ts

- ❌ Don't bypass the `.app` bundle to "make it simpler." It won't work.
- ❌ Don't add a second BLE client. There can only be one.
- ❌ Don't poll BLE faster than 1 Hz. The device drops frames.
- ❌ Don't trust the device's distance/steps across STANDBY transitions.
- ❌ Don't add a Garmin uploader. Explicit non-goal — see PRD §1.
- ❌ Don't add FTMS support. P1 doesn't speak it.
- ❌ Don't introduce a heavyweight HTTP framework. stdlib is fine.
- ❌ Don't put `*.sqlite` in the repo. `.gitignore` covers it; don't override.
- ❌ Don't add AI attribution to commits, PRs, or code. Global rule.

## Milestone reminder

We're in **Milestone 0 — POC**. Exit criterion: `make build && ./bin/walkingpad connect` streams live status frames from the P1 for ≥60 s with no drops. Until that works on the actual hardware, nothing else matters. Resist scope creep into Milestone 1 features.
