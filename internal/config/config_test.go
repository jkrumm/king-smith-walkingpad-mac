package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefault_Validates(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantSub string
	}{
		{
			name:    "port too low",
			mutate:  func(c *Config) { c.Daemon.HTTPPort = 0 },
			wantSub: "http_port",
		},
		{
			name:    "port too high",
			mutate:  func(c *Config) { c.Daemon.HTTPPort = 99999 },
			wantSub: "http_port",
		},
		{
			name:    "poll faster than 1 Hz floor",
			mutate:  func(c *Config) { c.Daemon.PollIntervalMs = 500 },
			wantSub: "poll_interval_ms",
		},
		{
			name:    "unknown log level",
			mutate:  func(c *Config) { c.Daemon.LogLevel = "verbose" },
			wantSub: "log_level",
		},
		{
			name:    "gap zero",
			mutate:  func(c *Config) { c.Session.GapMinutes = 0 },
			wantSub: "gap_minutes",
		},
		{
			name:    "resume zero",
			mutate:  func(c *Config) { c.Session.ResumeWithinSeconds = 0 },
			wantSub: "resume_within_seconds",
		},
		{
			name:    "weight zero",
			mutate:  func(c *Config) { c.Body.WeightKg = 0 },
			wantSub: "weight_kg",
		},
		{
			name:    "argo url empty",
			mutate:  func(c *Config) { c.Argo.URL = "" },
			wantSub: "argo.url",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error %q must mention %q", err, tt.wantSub)
			}
		})
	}
}

func TestLoad_MissingFileUsesDefaults(t *testing.T) {
	clearEnv(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.toml")

	cfg, err := Load(missing)
	if err != nil {
		t.Fatalf("missing file must not be an error: %v", err)
	}
	if cfg != Default() {
		t.Fatalf("missing file must yield exact defaults, got %+v", cfg)
	}
}

func TestLoad_PartialTOMLPreservesDefaults(t *testing.T) {
	clearEnv(t)
	path := writeTOML(t, `
[daemon]
http_port = 9999
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Daemon.HTTPPort != 9999 {
		t.Errorf("http_port = %d, want 9999", cfg.Daemon.HTTPPort)
	}
	if cfg.Daemon.PollIntervalMs != 1000 {
		t.Errorf("poll_interval_ms = %d, want 1000 (default)", cfg.Daemon.PollIntervalMs)
	}
	if cfg.Body.WeightKg != 80.0 {
		t.Errorf("weight_kg = %g, want 80.0 (default)", cfg.Body.WeightKg)
	}
}

func TestLoad_FullTOMLRoundTrip(t *testing.T) {
	clearEnv(t)
	path := writeTOML(t, `
[device]
address = "AA-BB-CC"

[daemon]
http_port = 8000
http_token = "secret"
poll_interval_ms = 1500
log_level = "debug"

[session]
gap_minutes = 30
resume_within_seconds = 90

[body]
weight_kg = 72.5

[argo]
url = "https://argo.example.com/api"
token = "Bearer abc"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Device:  Device{Address: "AA-BB-CC"},
		Daemon:  Daemon{HTTPPort: 8000, HTTPToken: "secret", PollIntervalMs: 1500, LogLevel: "debug"},
		Session: Session{GapMinutes: 30, ResumeWithinSeconds: 90},
		Body:    Body{WeightKg: 72.5},
		Argo:    Argo{URL: "https://argo.example.com/api", Token: "Bearer abc"},
	}
	if cfg != want {
		t.Fatalf("config mismatch\n got: %+v\nwant: %+v", cfg, want)
	}
}

func TestLoad_UnknownKeysReject(t *testing.T) {
	clearEnv(t)
	path := writeTOML(t, `
[daemon]
http_port = 7706
typo_key = "oops"
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown-key error, got nil")
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("KSWP_LOG_LEVEL", "warn")
	t.Setenv("KSWP_HTTP_PORT", "8123")
	t.Setenv("KSWP_HTTP_TOKEN", "envtok")
	t.Setenv("KSWP_ARGO_URL", "https://staging.example/api")
	t.Setenv("KSWP_ARGO_TOKEN", "Bearer xyz")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Daemon.LogLevel != "warn" {
		t.Errorf("log level: got %q want warn", cfg.Daemon.LogLevel)
	}
	if cfg.Daemon.HTTPPort != 8123 {
		t.Errorf("port: got %d want 8123", cfg.Daemon.HTTPPort)
	}
	if cfg.Daemon.HTTPToken != "envtok" {
		t.Errorf("token: got %q want envtok", cfg.Daemon.HTTPToken)
	}
	if cfg.Argo.URL != "https://staging.example/api" {
		t.Errorf("argo url: got %q", cfg.Argo.URL)
	}
	if cfg.Argo.Token != "Bearer xyz" {
		t.Errorf("argo token: got %q", cfg.Argo.Token)
	}
}

func TestLoad_EnvBeatsTOML(t *testing.T) {
	clearEnv(t)
	path := writeTOML(t, `
[daemon]
http_port = 8000
`)
	t.Setenv("KSWP_HTTP_PORT", "8123")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Daemon.HTTPPort != 8123 {
		t.Errorf("env must beat TOML: got %d want 8123", cfg.Daemon.HTTPPort)
	}
}

func TestLoad_InvalidEnvIntRejected(t *testing.T) {
	clearEnv(t)
	t.Setenv("KSWP_HTTP_PORT", "not-a-number")

	if _, err := Load(""); err == nil {
		t.Fatal("expected error for non-numeric port")
	}
}

func TestSyncEnabled(t *testing.T) {
	cfg := Default()
	if cfg.SyncEnabled() {
		t.Error("default config must not enable sync (no token)")
	}
	cfg.Argo.Token = "Bearer x"
	if !cfg.SyncEnabled() {
		t.Error("token + url must enable sync")
	}
}

func TestDataDir_ContainsWalkingPad(t *testing.T) {
	dir, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dir, filepath.Join("Library", "Application Support", "WalkingPad")) {
		t.Errorf("DataDir = %q, missing canonical suffix", dir)
	}
}

// writeTOML drops a TOML body into a temp file and returns its path.
func writeTOML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	return path
}

// clearEnv unsets every KSWP_* var the loader looks at, so tests start from a
// clean slate regardless of the developer's shell.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"KSWP_LOG_LEVEL",
		"KSWP_HTTP_PORT",
		"KSWP_HTTP_TOKEN",
		"KSWP_ARGO_URL",
		"KSWP_ARGO_TOKEN",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}
