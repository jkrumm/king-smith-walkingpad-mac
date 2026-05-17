package ble

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ErrLinkDisconnected is returned by Link.Send when no BLE connection is held.
var ErrLinkDisconnected = errors.New("ble: link disconnected")

// LinkConfig configures a Link. Zero-valued durations get sensible defaults.
type LinkConfig struct {
	// Address pins the connection to a specific peripheral. Empty means
	// "scan and pick the strongest WalkingPad on every (re)connect attempt".
	Address string

	// ScanTimeout caps each scan-and-pick. Default 8 s.
	ScanTimeout time.Duration

	// PollInterval is the ask_stats cadence. Default 1 s (PRD §4.5; never go
	// below 1 s — the pad drops frames above ~1.4 Hz).
	PollInterval time.Duration

	// Watchdog is the maximum gap between status frames before we declare the
	// link dead and trigger reconnect. Default 5 s. The pad emits frames at
	// 1 Hz, so anything ≥ 3 s indicates a real outage.
	Watchdog time.Duration

	Logger *slog.Logger

	// OnStatus is required: every decoded frame is delivered here.
	OnStatus StatusHandler
	// OnError is optional: decode failures are surfaced here.
	OnError ErrorHandler
	// OnConnect fires after a successful (re)connect. addr is the bound address.
	OnConnect func(addr string)
	// OnDisconnect fires when the watchdog (or an explicit teardown) drops the
	// link. reason is nil for a clean shutdown.
	OnDisconnect func(addr string, reason error)
}

// LinkClient is the subset of *Client that Link needs. Exposed so tests can
// inject fakes without spinning a real BLE adapter.
type LinkClient interface {
	Write(frame []byte) bool
	Disconnect() error
}

// LinkDialer abstracts ble.Connect + Scan. Production uses defaultDialer; tests
// inject fakes.
type LinkDialer interface {
	Dial(ctx context.Context, addr string, onStatus StatusHandler, onErr ErrorHandler) (LinkClient, error)
	Pick(ctx context.Context, timeout time.Duration) (Discovered, error)
}

// Link is the long-lived BLE connection manager. One Link per process — the
// daemon is the sole BLE central on the Mac (CLAUDE.md gotcha #4).
//
// Run() blocks for the lifetime of the link, handling initial connect,
// per-tick ask_stats polling, watchdog-based disconnect detection, and
// exponential-backoff reconnect. Send() routes outbound commands through the
// underlying rate-limited writer when connected.
type Link struct {
	cfg    LinkConfig
	dialer LinkDialer
	log    *slog.Logger

	mu        sync.Mutex
	client    LinkClient
	boundAddr string
	connected atomic.Bool
	lastFrame atomic.Int64 // unix nanos; updated from the notification thread
}

// NewLink builds a Link with default durations applied. The returned Link must
// be Run() to do anything useful.
func NewLink(cfg LinkConfig) *Link {
	if cfg.OnStatus == nil {
		panic("ble: LinkConfig.OnStatus is required")
	}
	if cfg.ScanTimeout <= 0 {
		cfg.ScanTimeout = 8 * time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 1 * time.Second
	}
	if cfg.Watchdog <= 0 {
		cfg.Watchdog = 5 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Link{
		cfg:    cfg,
		dialer: defaultDialer{},
		log:    cfg.Logger,
	}
}

// WithDialer swaps the dialer. Intended for tests.
func (l *Link) WithDialer(d LinkDialer) *Link { l.dialer = d; return l }

// Connected reports whether the Link currently holds an active BLE connection.
func (l *Link) Connected() bool { return l.connected.Load() }

// BoundAddress returns the address Link is currently connected to (or empty).
func (l *Link) BoundAddress() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.boundAddr
}

// Send queues a command frame. Returns ErrLinkDisconnected if no link is held;
// returns nil if the frame was accepted by the rate-limited writer.
func (l *Link) Send(frame []byte) error {
	l.mu.Lock()
	cl := l.client
	l.mu.Unlock()
	if cl == nil {
		return ErrLinkDisconnected
	}
	if !cl.Write(frame) {
		return errors.New("ble: write queue full")
	}
	return nil
}

// Run drives the connect → poll → reconnect loop. Blocks until ctx is
// cancelled. Returns nil on clean shutdown.
func (l *Link) Run(ctx context.Context) error {
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}

		// Resolve which address to dial.
		addr, err := l.resolveAddr(ctx)
		if err != nil {
			attempt++
			wait := Backoff(attempt)
			l.log.Warn("ble.resolve_failed", "err", err, "attempt", attempt, "retry_in", wait)
			if !sleepCtx(ctx, wait) {
				return nil
			}
			continue
		}

		// Dial.
		client, err := l.dialer.Dial(ctx, addr, l.onStatusInternal, l.cfg.OnError)
		if err != nil {
			attempt++
			wait := Backoff(attempt)
			l.log.Warn("ble.dial_failed", "addr", addr, "err", err, "attempt", attempt, "retry_in", wait)
			if !sleepCtx(ctx, wait) {
				return nil
			}
			continue
		}

		// Connected — reset backoff.
		attempt = 0
		l.mu.Lock()
		l.client = client
		l.boundAddr = addr
		l.mu.Unlock()
		l.connected.Store(true)
		l.lastFrame.Store(time.Now().UnixNano())
		l.log.Info("ble.connected", "addr", addr)
		if l.cfg.OnConnect != nil {
			l.cfg.OnConnect(addr)
		}

		// Drive the polling + watchdog loop until the link drops.
		reason := l.serveLoop(ctx, client)

		// Teardown.
		l.connected.Store(false)
		l.mu.Lock()
		l.client = nil
		l.mu.Unlock()
		if err := client.Disconnect(); err != nil {
			l.log.Warn("ble.disconnect_err", "err", err)
		}
		l.log.Info("ble.disconnected", "addr", addr, "reason", reasonOrNil(reason))
		if l.cfg.OnDisconnect != nil {
			l.cfg.OnDisconnect(addr, reason)
		}

		if ctx.Err() != nil {
			return nil
		}
		// Backoff before next attempt unless the link died healthy (it didn't,
		// or we'd have returned via ctx.Err above).
		attempt++
		wait := Backoff(attempt)
		l.log.Info("ble.reconnect_scheduled", "in", wait, "attempt", attempt)
		if !sleepCtx(ctx, wait) {
			return nil
		}
	}
}

// resolveAddr returns the address to dial. If cfg.Address is set, we use it
// directly (the user pinned the device). Otherwise scan-and-pick.
func (l *Link) resolveAddr(ctx context.Context) (string, error) {
	if l.cfg.Address != "" {
		return l.cfg.Address, nil
	}
	d, err := l.dialer.Pick(ctx, l.cfg.ScanTimeout)
	if err != nil {
		return "", err
	}
	return d.Address, nil
}

// serveLoop runs the active-link loop and returns the disconnect reason.
// nil = clean shutdown via ctx; non-nil = watchdog or write failure.
func (l *Link) serveLoop(ctx context.Context, client LinkClient) error {
	// Prime the device — the connect command worked because the existing
	// `connect` CLI uses the same pattern: beep + ask_stats so notifications
	// start flowing immediately.
	_ = client.Write(EncodeBeep())
	_ = client.Write(EncodeAskStats())

	pollTicker := time.NewTicker(l.cfg.PollInterval)
	defer pollTicker.Stop()

	// Check the watchdog at half the configured interval so we catch a stall
	// within ~Watchdog/2 of when it actually fires.
	watchdogTicker := time.NewTicker(l.cfg.Watchdog / 2)
	defer watchdogTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pollTicker.C:
			if !client.Write(EncodeAskStats()) {
				l.log.Warn("ble.poll_dropped", "reason", "queue full")
			}
		case <-watchdogTicker.C:
			gap := time.Since(time.Unix(0, l.lastFrame.Load()))
			if gap > l.cfg.Watchdog {
				return fmt.Errorf("watchdog: no frame for %s (limit %s)", gap.Round(time.Millisecond), l.cfg.Watchdog)
			}
		}
	}
}

// onStatusInternal updates the watchdog timestamp before fanning out to the
// user-supplied handler. Runs on the BLE notification thread — must not block.
func (l *Link) onStatusInternal(s Status) {
	l.lastFrame.Store(time.Now().UnixNano())
	l.cfg.OnStatus(s)
}

// --- default dialer --------------------------------------------------------

type defaultDialer struct{}

func (defaultDialer) Dial(ctx context.Context, addr string, onStatus StatusHandler, onErr ErrorHandler) (LinkClient, error) {
	return Connect(ctx, addr, onStatus, onErr)
}

func (defaultDialer) Pick(ctx context.Context, timeout time.Duration) (Discovered, error) {
	found, err := Scan(ctx, timeout)
	if err != nil {
		return Discovered{}, fmt.Errorf("scan: %w", err)
	}
	if len(found) == 0 {
		return Discovered{}, fmt.Errorf("no WalkingPad found in %s", timeout)
	}
	// Strongest RSSI first; ties broken by service-UUID match.
	sort.Slice(found, func(i, j int) bool {
		if found[i].RSSI != found[j].RSSI {
			return found[i].RSSI > found[j].RSSI
		}
		return found[i].HasService && !found[j].HasService
	})
	return found[0], nil
}

// --- helpers ---------------------------------------------------------------

// sleepCtx waits for d or ctx cancellation. Returns true if d elapsed, false
// on cancellation.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// reasonOrNil flattens an error for structured logging without producing
// "<nil>" strings in the JSON sink.
func reasonOrNil(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}
