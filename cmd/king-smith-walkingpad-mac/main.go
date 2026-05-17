// Package main is the entrypoint for the king-smith-walkingpad-mac daemon.
//
// The daemon runs as a macOS LaunchAgent, owns the BLE connection to the
// KingSmith WalkingPad, persists session data to a local SQLite store, and
// exposes a localhost HTTP API for the Raycast extension and other clients.
//
// See PRD.md for the full design.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
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
	fmt.Fprintf(os.Stderr, `king-smith-walkingpad-mac %s

Usage:
  king-smith-walkingpad-mac <command> [flags]

Commands:
  scan       Discover nearby WalkingPad devices
  version    Print the build version
  help       Show this help

For BLE access on macOS, run from inside the .app bundle:
  /Applications/King-Smith-WalkingPad-Mac.app/Contents/MacOS/king-smith-walkingpad-mac scan
`, Version)
}

func runScan(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	timeout := fs.Duration("timeout", 8*time.Second, "scan duration")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	fmt.Fprintf(os.Stderr, "scanning for %s …\n", *timeout)
	start := time.Now()

	found, err := ble.Scan(ctx, *timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan failed: %v\n", err)
		return 1
	}

	elapsed := time.Since(start).Round(10 * time.Millisecond)
	if len(found) == 0 {
		fmt.Fprintf(os.Stderr, `
no WalkingPad devices found after %s.

Common causes:
  - You are running the bare binary instead of the .app bundle.
    macOS silently denies CoreBluetooth without a bundled Info.plist.
    Use: /Applications/King-Smith-WalkingPad-Mac.app/Contents/MacOS/king-smith-walkingpad-mac scan
  - Bluetooth is off (System Settings → Bluetooth).
  - The WalkingPad is off, asleep, or out of range.
  - First-run permission prompt has not been accepted yet — check
    System Settings → Privacy & Security → Bluetooth.
`, elapsed)
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
