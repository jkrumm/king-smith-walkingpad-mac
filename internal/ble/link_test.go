package ble

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- fakes -----------------------------------------------------------------

type fakeClient struct {
	mu          sync.Mutex
	writes      [][]byte
	disconnects int
	onStatus    StatusHandler
	onErr       ErrorHandler //nolint:unused // captured for completeness; tests don't drive errors
}

func (c *fakeClient) Write(b []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), b...))
	return true
}

func (c *fakeClient) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnects++
	return nil
}

func (c *fakeClient) deliver(s Status) {
	c.mu.Lock()
	h := c.onStatus
	c.mu.Unlock()
	if h != nil {
		h(s)
	}
}

func (c *fakeClient) writeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.writes)
}

type fakeDialer struct {
	dialCalls    atomic.Int32
	dialFailFor  atomic.Int32 // first N dials fail with dialErr
	dialErr      error        // error returned during the fail window
	dialDuration time.Duration

	pickCalls  atomic.Int32
	pickResult Discovered
	pickErr    error

	current atomic.Pointer[fakeClient]
}

func (d *fakeDialer) Dial(_ context.Context, _ string, onStatus StatusHandler, onErr ErrorHandler) (LinkClient, error) {
	d.dialCalls.Add(1)
	failNow := d.dialFailFor.Load() > 0
	if failNow {
		d.dialFailFor.Add(-1)
	}
	if d.dialDuration > 0 {
		time.Sleep(d.dialDuration)
	}
	if failNow {
		return nil, d.dialErr
	}
	c := &fakeClient{onStatus: onStatus, onErr: onErr}
	d.current.Store(c)
	return c, nil
}

func (d *fakeDialer) Pick(_ context.Context, _ time.Duration) (Discovered, error) {
	d.pickCalls.Add(1)
	return d.pickResult, d.pickErr
}

// --- helpers --------------------------------------------------------------

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// waitUntil polls cond every 5ms up to timeout. Returns true if cond became true.
func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// --- tests ----------------------------------------------------------------

func TestLink_InitialConnectAndPoll(t *testing.T) {
	dialer := &fakeDialer{}
	frames := atomic.Int32{}
	link := NewLink(LinkConfig{
		Address:      "abc",
		PollInterval: 10 * time.Millisecond,
		Watchdog:     500 * time.Millisecond, // long enough not to fire
		Logger:       quietLogger(),
		OnStatus:     func(_ Status) { frames.Add(1) },
	}).WithDialer(dialer)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = link.Run(ctx); close(done) }()
	defer func() {
		cancel()
		<-done
	}()

	if !waitUntil(200*time.Millisecond, link.Connected) {
		t.Fatal("link never connected")
	}
	if link.BoundAddress() != "abc" {
		t.Errorf("bound addr = %q", link.BoundAddress())
	}

	// Wait for several poll ticks to confirm ask_stats is going out.
	if !waitUntil(200*time.Millisecond, func() bool {
		c := dialer.current.Load()
		return c != nil && c.writeCount() >= 4 // beep + ask_stats + ≥2 polls
	}) {
		t.Errorf("not enough writes; got %d", dialer.current.Load().writeCount())
	}

	// Deliver a frame; the user handler must see it.
	dialer.current.Load().deliver(Status{State: BeltActive, SpeedKmh: 4})
	if !waitUntil(100*time.Millisecond, func() bool { return frames.Load() == 1 }) {
		t.Errorf("OnStatus not called; frames = %d", frames.Load())
	}
}

func TestLink_DialFailureRetries(t *testing.T) {
	dialer := &fakeDialer{dialErr: errors.New("simulated dial fail")}
	dialer.dialFailFor.Store(2)
	link := NewLink(LinkConfig{
		Address:      "abc",
		PollInterval: 10 * time.Millisecond,
		Watchdog:     500 * time.Millisecond,
		Logger:       quietLogger(),
		OnStatus:     func(_ Status) {},
	}).WithDialer(dialer)

	// Patch the backoff schedule for tests by using addresses that fail fast.
	// We can't override Backoff() per-link, so just wait long enough to cross
	// the 1s + 2s = 3s expected waits for two retries. Use a hard ceiling of
	// 6s to leave generous margin.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() { _ = link.Run(ctx); close(done) }()
	defer func() { <-done }()

	if !waitUntil(5*time.Second, link.Connected) {
		t.Fatalf("link never recovered; dialCalls=%d", dialer.dialCalls.Load())
	}
	// Two failed dials + the successful one.
	if dialer.dialCalls.Load() < 3 {
		t.Errorf("dialCalls = %d, want ≥ 3", dialer.dialCalls.Load())
	}
	cancel()
}

func TestLink_WatchdogTriggersReconnect(t *testing.T) {
	dialer := &fakeDialer{}
	disconnects := atomic.Int32{}
	link := NewLink(LinkConfig{
		Address:      "abc",
		PollInterval: 10 * time.Millisecond,
		Watchdog:     80 * time.Millisecond,
		Logger:       quietLogger(),
		OnStatus:     func(_ Status) {},
		OnDisconnect: func(_ string, _ error) { disconnects.Add(1) },
	}).WithDialer(dialer)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = link.Run(ctx); close(done) }()
	defer func() {
		cancel()
		<-done
	}()

	// Initial connect.
	if !waitUntil(200*time.Millisecond, link.Connected) {
		t.Fatal("initial connect failed")
	}
	firstClient := dialer.current.Load()

	// Don't deliver any frames → watchdog should fire and reconnect attempt #1
	// begins. After 1 s backoff a second dial occurs.
	if !waitUntil(2*time.Second, func() bool { return dialer.dialCalls.Load() >= 2 }) {
		t.Fatalf("watchdog did not trigger reconnect; dialCalls=%d", dialer.dialCalls.Load())
	}
	if disconnects.Load() < 1 {
		t.Errorf("OnDisconnect not called; got %d", disconnects.Load())
	}
	if firstClient.disconnects < 1 {
		t.Errorf("Disconnect() never called on first client; got %d", firstClient.disconnects)
	}
}

func TestLink_SendBeforeConnectFails(t *testing.T) {
	link := NewLink(LinkConfig{Address: "abc", Logger: quietLogger(), OnStatus: func(_ Status) {}})
	if err := link.Send([]byte{0xAA}); !errors.Is(err, ErrLinkDisconnected) {
		t.Errorf("Send disconnected: got %v want ErrLinkDisconnected", err)
	}
}

func TestLink_SendWhenConnected(t *testing.T) {
	dialer := &fakeDialer{}
	link := NewLink(LinkConfig{
		Address:      "abc",
		PollInterval: 50 * time.Millisecond,
		Watchdog:     1 * time.Second,
		Logger:       quietLogger(),
		OnStatus:     func(_ Status) {},
	}).WithDialer(dialer)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = link.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	if !waitUntil(200*time.Millisecond, link.Connected) {
		t.Fatal("connect failed")
	}
	if err := link.Send([]byte{0x01, 0x02, 0x03}); err != nil {
		t.Errorf("Send while connected: %v", err)
	}
	// Verify the bytes made it through to the fake client.
	c := dialer.current.Load()
	if !waitUntil(100*time.Millisecond, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, w := range c.writes {
			if len(w) == 3 && w[0] == 0x01 {
				return true
			}
		}
		return false
	}) {
		t.Error("custom frame did not reach the client")
	}
}

func TestLink_ScanPickedAddressBoundCorrectly(t *testing.T) {
	dialer := &fakeDialer{pickResult: Discovered{Address: "picked-1"}}
	link := NewLink(LinkConfig{
		Address:      "", // empty triggers Pick
		PollInterval: 50 * time.Millisecond,
		Watchdog:     1 * time.Second,
		Logger:       quietLogger(),
		OnStatus:     func(_ Status) {},
	}).WithDialer(dialer)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = link.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	if !waitUntil(500*time.Millisecond, link.Connected) {
		t.Fatalf("link never connected; picks=%d dials=%d", dialer.pickCalls.Load(), dialer.dialCalls.Load())
	}
	if link.BoundAddress() != "picked-1" {
		t.Errorf("bound = %q, want picked-1", link.BoundAddress())
	}
	if dialer.pickCalls.Load() < 1 {
		t.Errorf("Pick was not called; got %d", dialer.pickCalls.Load())
	}
}

func TestLink_CleanShutdown(t *testing.T) {
	dialer := &fakeDialer{}
	link := NewLink(LinkConfig{
		Address:      "abc",
		PollInterval: 10 * time.Millisecond,
		Watchdog:     500 * time.Millisecond,
		Logger:       quietLogger(),
		OnStatus:     func(_ Status) {},
	}).WithDialer(dialer)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = link.Run(ctx); close(done) }()

	if !waitUntil(200*time.Millisecond, link.Connected) {
		t.Fatal("connect failed")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	if link.Connected() {
		t.Error("Connected should be false post-shutdown")
	}
	if dialer.current.Load().disconnects < 1 {
		t.Error("Disconnect() must be called during shutdown")
	}
}
