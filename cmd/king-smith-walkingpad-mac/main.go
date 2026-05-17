// Package main is the entrypoint for the king-smith-walkingpad-mac daemon.
//
// The daemon runs as a macOS LaunchAgent, owns the BLE connection to the
// KingSmith WalkingPad, persists session data to a local SQLite store, and
// exposes a localhost HTTP API for the Raycast extension and other clients.
//
// See PRD.md for the full design.
package main

import (
	"fmt"
	"os"
)

// Version is injected at build time via -ldflags.
var Version = "dev"

func main() {
	fmt.Fprintf(os.Stderr, "king-smith-walkingpad-mac %s — skeleton, not yet implemented\n", Version)
	os.Exit(0)
}
