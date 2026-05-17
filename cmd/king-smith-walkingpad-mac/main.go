// Package main is the entrypoint for the WalkingPad daemon.
//
// The daemon runs as a macOS LaunchAgent, owns the BLE connection to the
// KingSmith WalkingPad, persists session data to a local SQLite store, and
// exposes a localhost HTTP API for the Raycast extension and other clients.
//
// The binary is named `walkingpad`; the repo and Go module keep
// `king-smith-walkingpad-mac` for SEO. See PRD.md for the full design.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/ble"
)

// Version is injected at build time via -ldflags.
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := dispatch(ctx, cmd, args)
	stop()
	os.Exit(code)
}

func dispatch(ctx context.Context, cmd string, args []string) int {
	switch cmd {
	case "scan":
		return runScan(ctx, args)
	case "connect":
		return runConnect(ctx, args)
	case "version", "--version", "-v":
		fmt.Println(Version)
		return 0
	case "help", "--help", "-h":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", cmd)
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `walkingpad %s

Usage:
  walkingpad <command> [flags]

Commands:
  scan       Discover nearby WalkingPad devices
  connect    Connect, subscribe to status frames, poll ask_stats
  version    Print the build version
  help       Show this help

For BLE access on macOS, run from inside the .app bundle:
  /Applications/WalkingPad.app/Contents/MacOS/walkingpad scan
`, Version)
}

func runScan(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	timeout := fs.Duration("timeout", 8*time.Second, "scan duration")
	all := fs.Bool("all", false, "show every advertising BLE device (no filter); use to debug permission issues")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	label := "WalkingPad devices"
	if *all {
		label = "BLE devices (unfiltered)"
	}
	fmt.Fprintf(os.Stderr, "scanning %s for %s …\n", label, *timeout)
	start := time.Now()

	found, err := ble.ScanWith(ctx, ble.ScanOptions{Timeout: *timeout, All: *all})
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan failed: %v\n", err)
		return 1
	}

	elapsed := time.Since(start).Round(10 * time.Millisecond)
	if len(found) == 0 {
		if *all {
			fmt.Fprintf(os.Stderr, `
no BLE devices at all after %s.

This almost always means CoreBluetooth denied access silently:
  - The .app bundle is unsigned. Ad-hoc sign and re-grant:
      codesign --force --deep --sign - /Applications/WalkingPad.app
      xattr -cr /Applications/WalkingPad.app
      tccutil reset Bluetooth com.jkrumm.walkingpad
      open /Applications/WalkingPad.app   # accept the prompt
  - Or: Bluetooth is off (System Settings → Bluetooth).
  - Or: no advertising devices in range (very unlikely on a Mac).
`, elapsed)
		} else {
			fmt.Fprintf(os.Stderr, `
no WalkingPad devices found after %s.

To rule out a permission issue, run the unfiltered scan:
  /Applications/WalkingPad.app/Contents/MacOS/walkingpad scan --all

Other common causes:
  - The WalkingPad is off, asleep, or out of range — step on it or
    tap the remote to wake the radio.
  - First-run Bluetooth permission was never granted: check
    System Settings → Privacy & Security → Bluetooth for "WalkingPad".
`, elapsed)
		}
		return 1
	}

	// Strongest signal first.
	sort.Slice(found, func(i, j int) bool { return found[i].RSSI > found[j].RSSI })

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ADDRESS\tNAME\tRSSI\tSERVICE\tNAME-MATCH")
	for _, d := range found {
		name := d.Name
		if name == "" {
			name = "—"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n",
			d.Address, name, d.RSSI, mark(d.HasService), mark(d.NameMatch))
	}
	_ = w.Flush()
	fmt.Fprintf(os.Stderr, "\nfound %d device(s) in %s\n", len(found), elapsed)
	return 0
}

func mark(b bool) string {
	if b {
		return "yes"
	}
	return "—"
}

func runConnect(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	addr := fs.String("addr", "", "device address (UUID on macOS); if empty, scan and pick the strongest WalkingPad")
	scanTimeout := fs.Duration("scan-timeout", 8*time.Second, "scan timeout when -addr is empty")
	pollInterval := fs.Duration("poll", 1*time.Second, "ask_stats poll cadence (PRD default: 1s)")
	duration := fs.Duration("duration", 0, "auto-disconnect after this duration; 0 = run until Ctrl-C")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *addr == "" {
		picked, err := pickWalkingPad(ctx, *scanTimeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 1
		}
		*addr = picked.Address
		fmt.Fprintf(os.Stderr, "selected %s (name=%q rssi=%d)\n", picked.Address, picked.Name, picked.RSSI)
	}

	var (
		mu         sync.Mutex
		frames     int
		decodeErrs int
		firstFrame time.Time
		lastFrame  time.Time
	)

	onStatus := func(s ble.Status) {
		now := time.Now()
		mu.Lock()
		frames++
		if frames == 1 {
			firstFrame = now
		}
		lastFrame = now
		mu.Unlock()
		_, _ = fmt.Fprintf(os.Stdout,
			"[%s] state=%-8s mode=%-7s speed=%4.1f km/h time=%6s dist=%7.0f m steps=%5d btn=%d\n",
			now.Format("15:04:05.000"), s.State, s.Mode, s.SpeedKmh, s.Time, s.Distance, s.Steps, s.Button)
	}
	onErr := func(err error) {
		mu.Lock()
		decodeErrs++
		mu.Unlock()
		fmt.Fprintf(os.Stderr, "decode error: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "connecting to %s …\n", *addr)
	connectStart := time.Now()
	client, err := ble.Connect(ctx, *addr, onStatus, onErr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "connected in %s — polling every %s (Ctrl-C to stop)\n",
		time.Since(connectStart).Round(time.Millisecond), *pollInterval)

	// Kick the device so notifications start arriving immediately.
	client.Write(ble.EncodeBeep())
	client.Write(ble.EncodeAskStats())

	ticker := time.NewTicker(*pollInterval)
	defer ticker.Stop()

	var deadlineCh <-chan time.Time
	if *duration > 0 {
		deadlineCh = time.After(*duration)
	}

	runStart := time.Now()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-deadlineCh:
			fmt.Fprintf(os.Stderr, "\nduration reached; disconnecting\n")
			break loop
		case <-ticker.C:
			if !client.Write(ble.EncodeAskStats()) {
				fmt.Fprintf(os.Stderr, "write queue full; ask_stats dropped\n")
			}
		}
	}

	if err := client.Disconnect(); err != nil {
		fmt.Fprintf(os.Stderr, "disconnect: %v\n", err)
	}

	mu.Lock()
	fc, de, ff, lf := frames, decodeErrs, firstFrame, lastFrame
	mu.Unlock()

	total := time.Since(runStart).Round(time.Millisecond)
	streamSpan := lf.Sub(ff).Round(time.Millisecond)
	var rate float64
	if streamSpan > 0 {
		rate = float64(fc-1) / streamSpan.Seconds()
	}
	timeToFirst := time.Duration(0)
	if !ff.IsZero() {
		timeToFirst = ff.Sub(connectStart).Round(time.Millisecond)
	}

	fmt.Fprintf(os.Stderr, `
--- summary ---
total runtime      %s
frames received    %d
decode errors      %d
time-to-first      %s
stream span        %s
avg frame rate     %.2f Hz
`, total, fc, de, timeToFirst, streamSpan, rate)
	return 0
}

func pickWalkingPad(ctx context.Context, timeout time.Duration) (ble.Discovered, error) {
	fmt.Fprintf(os.Stderr, "scanning for %s to pick a WalkingPad …\n", timeout)
	found, err := ble.Scan(ctx, timeout)
	if err != nil {
		return ble.Discovered{}, fmt.Errorf("scan: %w", err)
	}
	if len(found) == 0 {
		return ble.Discovered{}, fmt.Errorf("no WalkingPad found in %s — run `walkingpad scan --all` to triage", timeout)
	}
	// Strongest RSSI wins; ties broken by service-UUID match.
	sort.Slice(found, func(i, j int) bool {
		if found[i].RSSI != found[j].RSSI {
			return found[i].RSSI > found[j].RSSI
		}
		return found[i].HasService && !found[j].HasService
	})
	return found[0], nil
}
