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
	fmt.Fprintf(os.Stderr, `walkingpad %s

Usage:
  walkingpad <command> [flags]

Commands:
  scan       Discover nearby WalkingPad devices
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
