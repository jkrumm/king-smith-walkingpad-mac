// Package config owns the daemon's runtime configuration.
//
// Resolution order: defaults → TOML file → environment variables. See PRD §11
// for the schema and CLAUDE.md "Argo integration" for the token model (env or
// inline only — no shellout to `op` from the binary).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the resolved daemon configuration. Field tags map to TOML keys per
// PRD §11.
type Config struct {
	Device  Device  `toml:"device"`
	Daemon  Daemon  `toml:"daemon"`
	Session Session `toml:"session"`
	Body    Body    `toml:"body"`
	Argo    Argo    `toml:"argo"`
}

// Device holds BLE peripheral selection.
type Device struct {
	// Address, if set, pins the daemon to a specific BLE peripheral (UUID on
	// macOS). Empty means "scan and pick the strongest WalkingPad".
	Address string `toml:"address"`
}

// Daemon holds process-wide knobs.
type Daemon struct {
	HTTPPort       int    `toml:"http_port"`
	HTTPToken      string `toml:"http_token"`
	PollIntervalMs int    `toml:"poll_interval_ms"`
	LogLevel       string `toml:"log_level"`
}

// Session holds the session-grouping policy (PRD §7).
type Session struct {
	GapMinutes          int `toml:"gap_minutes"`
	ResumeWithinSeconds int `toml:"resume_within_seconds"`
	// MinDurationSeconds is the floor below which a standalone closed session
	// is discarded as accidental noise (tapping start by mistake, a 30-second
	// belt twitch, etc.). The drop only fires once the resurrection window
	// has passed, so a short session that ends up being merged into a longer
	// one is never lost. Set to 0 to disable the drop pass entirely.
	MinDurationSeconds int `toml:"min_duration_seconds"`
}

// Body holds user metrics needed for client-side calorie computation.
type Body struct {
	WeightKg float64 `toml:"weight_kg"`
}

// Argo holds upstream sync configuration. Token is optional: if neither the
// inline field nor KSWP_ARGO_TOKEN is set, the sync worker stays disabled.
type Argo struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
}

// Default returns the baseline configuration. Every field is populated; the
// TOML loader and env layer only override.
func Default() Config {
	return Config{
		Daemon: Daemon{
			HTTPPort:       7706,
			PollIntervalMs: 1000,
			LogLevel:       "info",
		},
		Session: Session{
			GapMinutes:          15,
			ResumeWithinSeconds: 60,
			MinDurationSeconds:  300,
		},
		Body: Body{
			WeightKg: 80.0,
		},
		Argo: Argo{
			URL: "https://argo.jkrumm.com/api",
		},
	}
}

// LogLevels enumerates the accepted slog level names.
var LogLevels = []string{"debug", "info", "warn", "error"}

// Validate enforces hard limits that no caller should be allowed to violate.
// Bounds rationale lives next to each check.
func (c Config) Validate() error {
	if c.Daemon.HTTPPort < 1 || c.Daemon.HTTPPort > 65535 {
		return fmt.Errorf("daemon.http_port out of range: %d", c.Daemon.HTTPPort)
	}
	// PRD gotcha: never poll BLE faster than 1 Hz; the device drops frames at
	// >1.4 Hz and the MinWriteGap is 700 ms. 1000 ms is the floor.
	if c.Daemon.PollIntervalMs < 1000 {
		return fmt.Errorf("daemon.poll_interval_ms must be >= 1000 (BLE rate limit): %d", c.Daemon.PollIntervalMs)
	}
	if !containsString(LogLevels, c.Daemon.LogLevel) {
		return fmt.Errorf("daemon.log_level must be one of %v: %q", LogLevels, c.Daemon.LogLevel)
	}
	if c.Session.GapMinutes < 1 {
		return fmt.Errorf("session.gap_minutes must be >= 1: %d", c.Session.GapMinutes)
	}
	if c.Session.ResumeWithinSeconds < 1 {
		return fmt.Errorf("session.resume_within_seconds must be >= 1: %d", c.Session.ResumeWithinSeconds)
	}
	if c.Session.MinDurationSeconds < 0 {
		return fmt.Errorf("session.min_duration_seconds must be >= 0 (0 disables drop): %d", c.Session.MinDurationSeconds)
	}
	if c.Body.WeightKg <= 0 {
		return fmt.Errorf("body.weight_kg must be > 0: %g", c.Body.WeightKg)
	}
	if c.Argo.URL == "" {
		return errors.New("argo.url is empty")
	}
	return nil
}

// SyncEnabled reports whether the Argo sync worker has the credentials it needs.
func (c Config) SyncEnabled() bool {
	return c.Argo.Token != "" && c.Argo.URL != ""
}

// DataDir returns the canonical data directory for the daemon
// (~/Library/Application Support/WalkingPad).
func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "WalkingPad"), nil
}

// DefaultConfigPath returns the canonical TOML location.
func DefaultConfigPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// LogJSONLPath is the on-disk structured log sink. The pretty text stream lives
// on stderr (captured by launchd into /tmp/walkingpad.log via the plist).
const LogJSONLPath = "/tmp/walkingpad.jsonl"

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
