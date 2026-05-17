package logger

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/config"
)

func TestNewWithPath_WritesBothSinks(t *testing.T) {
	cfg := config.Default()
	jsonlPath := filepath.Join(t.TempDir(), "out.jsonl")
	var stderr bytes.Buffer

	log, closer, err := NewWithPath(cfg, jsonlPath, &stderr)
	if err != nil {
		t.Fatalf("NewWithPath: %v", err)
	}

	log.Info("hello", "k", 1)

	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// JSONL sink: must decode and contain our msg + attr.
	jsonl, err := os.ReadFile(jsonlPath) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.TrimSpace(jsonl)) == 0 {
		t.Fatal("jsonl file empty")
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(jsonl), &rec); err != nil {
		t.Fatalf("jsonl not valid JSON: %v\n%s", err, jsonl)
	}
	if rec["msg"] != "hello" {
		t.Errorf("jsonl msg = %v, want hello", rec["msg"])
	}
	if rec["k"] != float64(1) {
		t.Errorf("jsonl k = %v, want 1", rec["k"])
	}

	// Pretty sink: text contains the message somewhere.
	if !strings.Contains(stderr.String(), "hello") {
		t.Errorf("stderr missing message: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "k=1") {
		t.Errorf("stderr missing attr: %q", stderr.String())
	}
}

func TestNewWithPath_LevelFiltering(t *testing.T) {
	cfg := config.Default()
	cfg.Daemon.LogLevel = "warn"
	jsonlPath := filepath.Join(t.TempDir(), "out.jsonl")
	var stderr bytes.Buffer

	log, closer, err := NewWithPath(cfg, jsonlPath, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closer.Close() }()

	log.Debug("debug-msg")
	log.Info("info-msg")
	log.Warn("warn-msg")

	out := stderr.String()
	if strings.Contains(out, "debug-msg") || strings.Contains(out, "info-msg") {
		t.Errorf("below-threshold records leaked: %q", out)
	}
	if !strings.Contains(out, "warn-msg") {
		t.Errorf("warn-msg missing: %q", out)
	}
}

func TestNewWithPath_AppendsToExistingFile(t *testing.T) {
	cfg := config.Default()
	jsonlPath := filepath.Join(t.TempDir(), "out.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"msg":"pre-existing"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	log, closer, err := NewWithPath(cfg, jsonlPath, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	log.Info("new-line")
	_ = closer.Close()

	body, _ := os.ReadFile(jsonlPath) // #nosec G304 -- test temp path
	if !strings.Contains(string(body), "pre-existing") {
		t.Error("pre-existing line was overwritten — should be append mode")
	}
	if !strings.Contains(string(body), "new-line") {
		t.Error("new line missing")
	}
}

func TestNew_UnknownLevelRejected(t *testing.T) {
	cfg := config.Default()
	cfg.Daemon.LogLevel = "trace"

	_, _, err := NewWithPath(cfg, filepath.Join(t.TempDir(), "x.jsonl"), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for unknown level")
	}
}

func TestMultiHandler_WithAttrsAppliesToBothSinks(t *testing.T) {
	cfg := config.Default()
	jsonlPath := filepath.Join(t.TempDir(), "out.jsonl")
	var stderr bytes.Buffer

	log, closer, err := NewWithPath(cfg, jsonlPath, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	log = log.With("component", "test")
	log.Info("scoped")
	_ = closer.Close()

	jsonl, _ := os.ReadFile(jsonlPath) // #nosec G304 -- test temp path
	if !strings.Contains(string(jsonl), `"component":"test"`) {
		t.Errorf("jsonl missing scoped attr: %s", jsonl)
	}
	if !strings.Contains(stderr.String(), "component=test") {
		t.Errorf("stderr missing scoped attr: %s", stderr.String())
	}
}
