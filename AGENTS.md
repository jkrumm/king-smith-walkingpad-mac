# AGENTS.md — king-smith-walkingpad-mac

> **Naming.** The repo and Go module keep `king-smith-walkingpad-mac` for SEO.
> Everything user-facing is `WalkingPad` (display) / `walkingpad` (CLI binary,
> identifier, log paths). The `cmd/king-smith-walkingpad-mac/` directory matches
> the repo; its compiled output is `bin/walkingpad`, installed to
> `/Applications/WalkingPad.app/Contents/MacOS/walkingpad`.

Per-project guidance for coding agents. The global config (`~/.claude/CLAUDE.md` for Claude Code) applies; this file adds project-specific rules.

## What this project is

A macOS LaunchAgent (Go) + Raycast extension that controls and tracks the KingSmith **WalkingPad P1** treadmill over BLE. Local SQLite is the source of truth; completed sessions sync to the personal **Argo** API. No Garmin integration (deliberate non-goal — see README's Why section). License **AGPL-3.0**.

**Read [`README.md`](./README.md) and [`docs/protocol.md`](docs/protocol.md) first.** The
README covers architecture and the API contract; `docs/protocol.md` is the source of truth
for the BLE protocol, schema and session-grouping logic. This file is the operator's manual
for agents.

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

1. **macOS Bluetooth requires a `.app` bundle.** A bare binary anywhere on `$PATH` (`/opt/homebrew/bin`, `/usr/local/bin`, etc.) is *silently denied* by CoreBluetooth — `Scan()` just returns nothing. The daemon must run from inside `WalkingPad.app` with `Info.plist` declaring `NSBluetoothAlwaysUsageDescription` + `NSBluetoothPeripheralUsageDescription`. We do NOT install a bare binary anywhere — the `.app` at `/Applications/` is the only install target. See docs/protocol.md.
2. **BLE write rate limit.** Device drops frames if commands arrive faster than ~1.4 Hz. **Enforce a 700 ms minimum gap** between any writes (the `frames.go` writer owns this). Status polls and user commands share the same write channel.
3. **CRC scope.** `sum(buf[1:-2]) & 0xFF` — exclusive of start byte AND checksum slot AND terminator. Get this wrong and the device silently ignores commands. Always test against the fixtures in `ble/frames_test.go`.
4. **Single BLE owner — empirically confirmed on this P1.** The peripheral accepts exactly one connected central at a time (verified 2026-05-17: while our daemon held the connection, the Garmin Connect IQ app could not connect, and vice versa). The daemon is the sole BLE client on the Mac. Raycast / CLI / argo all go through the HTTP API. **Open design call:** decide a slot-release policy (always grab and hold? release when the user opens KS Fit / starts a Garmin workout? expose a `POST /release` endpoint?). Never add a second BLE client.
5. **Speed encoding.** Wire format is `speed_kmh × 10` as a single byte (uint8). Range 0–60. Always validate the input before encoding.
6. **Distance encoding.** Wire format is uint24 BE in units of **10 m**. Divide raw by 100 for km, by 10 for metres.
7. **Calories are NOT in the status frame.** Compute client-side using the MET formula (`session/calories.go`). User weight from config.
8. **Session counters reset on every STOP, not only on STANDBY.** Verified on the user's P1 (POC hardware run, 2026-05-17): the moment the belt state byte transitions to `0x00` (STOPPED), `time / distance / steps` all read 0 in that same frame. The `0x04` (STOPPING) frame is the **last chance** to capture in-session totals — `BeltState.IsRunning()` returns true for both `0x02` and `0x04` so the session manager won't drop it. Do not assume monotonic counters across any stop.
9. **State-byte mapping diverges from ph4r05.** docs/protocol.md §4.4 has the verified table; the short version is `0x00=STOPPED, 0x02=ACTIVE, 0x04=STOPPING, 0x05=STANDBY, 0x07/0x08/0x09=3-2-1 start ramp`. Byte `0x01` is never emitted by current P1 firmware. Trust the Go enum in `internal/ble/frames.go`, not the ph4 docs.
10. **Set-speed is dropped while STOPPED or ramping.** Verified on the user's P1 (2026-05-21): a set-speed frame sent before the belt is `0x02` (ACTIVE) is ignored — the belt ramps to its stored `START_SPEED` pref (default 2 km/h) instead of the requested speed. To start at a target speed you must send start, **wait for the belt to reach ACTIVE (past the 3-2-1 ramp), then send set-speed**. `serve/controller.go` owns this: `Start`/`SetSpeed` call `startAtSpeed` → `ensureRunning` → `waitForActive` → set. This blocks the HTTP request ~4-5 s; that is intentional. Don't "optimize" by sending set-speed before start — it silently does nothing.

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

Full per-file responsibilities in the Layout table above; nothing else moved.

## Devloop

```bash
make            # default — print the menu
make up         # THE one — rebuild daemon, deploy to /Applications, kickstart
                # the LaunchAgent, verify the live /health version, then start
                # the Raycast dev loop. Run after every code change.
make test       # go test -race + raycast tsc --noEmit
make logs       # tail /tmp/walkingpad.log
make scan       # list nearby WalkingPad BLE devices (dev tool)
make fmt        # gofmt + go mod tidy
make lint       # golangci-lint
make clean      # remove ./bin
```

The previous separate `build` / `build-app` / `install` / `install-agent` /
`reload` / `redeploy` / `version` / `raycast-dev` targets are gone — they all
folded into `make up`. The pre-commit hook runs gofmt + govet + golangci-lint
+ `go test -race` directly (not via make targets), so removing them doesn't
break CI.

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
- Token resolution in the daemon: env `KSWP_ARGO_TOKEN` overrides `argo.token` in config TOML. If neither is set the sync worker stays disabled — the binary does **not** shell out to `op` or `secrets-run` itself. The live LaunchAgent (`scripts/install-launch-agent.sh`) resolves it *before spawn* with `secrets-run read <ref>` (never `op run` directly — that hangs on the headless mini) and exports `KSWP_ARGO_TOKEN` into the plist's `ProgramArguments` shell wrapper. Non-jkrumm installs simply don't sync.

Per global rules: argo changes happen as a **separate PR** in the argo repo, never bundled with daemon commits.

## Don'ts

- ❌ Don't bypass the `.app` bundle to "make it simpler." It won't work.
- ❌ Don't add a second BLE client. There can only be one.
- ❌ Don't poll BLE faster than 1 Hz. The device drops frames.
- ❌ Don't trust the device's distance/steps across STANDBY transitions.
- ❌ Don't add a Garmin uploader. Explicit non-goal — see README's Why section.
- ❌ Don't add FTMS support. P1 doesn't speak it.
- ❌ Don't introduce a heavyweight HTTP framework. stdlib is fine.
- ❌ Don't put `*.sqlite` in the repo. `.gitignore` covers it; don't override.
- ❌ Don't add AI attribution to commits, PRs, or code. Global rule.
