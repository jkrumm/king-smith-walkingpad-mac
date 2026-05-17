// Package logger wires the daemon's slog setup.
//
// Two sinks always run in parallel:
//   - JSONL appended to a file on disk (default: /tmp/walkingpad.jsonl).
//     Structured records for grep/jq/argo ingestion.
//   - Pretty text to stderr. Captured by launchd into /tmp/walkingpad.log via
//     the plist; useful when developing in the foreground (`make run`).
//
// Level filtering is shared — once a record passes the threshold it's written
// to both sinks.
package logger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/config"
)

// New builds the logger configured by cfg. The returned io.Closer flushes and
// closes the JSONL file handle; callers should defer Close on it.
func New(cfg config.Config) (*slog.Logger, io.Closer, error) {
	return NewWithPath(cfg, config.LogJSONLPath, os.Stderr)
}

// NewWithPath is the test-friendly form of New. Production callers use New.
func NewWithPath(cfg config.Config, jsonlPath string, stderr io.Writer) (*slog.Logger, io.Closer, error) {
	lvl, err := parseLevel(cfg.Daemon.LogLevel)
	if err != nil {
		return nil, nil, err
	}

	// #nosec G304 -- jsonlPath comes from config; the daemon's own log sink
	file, err := os.OpenFile(jsonlPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file %s: %w", jsonlPath, err)
	}

	opts := &slog.HandlerOptions{Level: lvl}
	mh := multiHandler{
		slog.NewJSONHandler(file, opts),
		slog.NewTextHandler(stderr, opts),
	}

	return slog.New(mh), file, nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level: %q", s)
	}
}

// multiHandler fans every record out to all handlers. Errors are joined so a
// failure in one sink doesn't hide a failure in another.
type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, lvl) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range m {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make(multiHandler, len(m))
	for i, h := range m {
		next[i] = h.WithAttrs(attrs)
	}
	return next
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	next := make(multiHandler, len(m))
	for i, h := range m {
		next[i] = h.WithGroup(name)
	}
	return next
}
