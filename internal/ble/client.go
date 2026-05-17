package ble

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

// GATT UUIDs from PRD §4.1. The macOS scanner cannot pre-filter; we filter in
// the discovery callback by either service-UUID match or LocalName match.
var (
	// ServiceUUID is the KingSmith WiLink primary service (0xFE00 alias).
	ServiceUUID = mustUUID("0000fe00-0000-1000-8000-00805f9b34fb")
	// rxCharUUID is the notify-only RX characteristic — device → controller status frames.
	rxCharUUID = mustUUID("0000fe01-0000-1000-8000-00805f9b34fb")
	// txCharUUID is the write-without-response TX characteristic — controller → device commands.
	txCharUUID = mustUUID("0000fe02-0000-1000-8000-00805f9b34fb")
)

// LocalName prefixes / substrings the KingSmith app accepts. Lower-cased before compare.
var localNameMatches = []string{"walkingpad", "ks-", "kingsmith", "zp-", "dynamax"}

func mustUUID(s string) bluetooth.UUID {
	u, err := bluetooth.ParseUUID(s)
	if err != nil {
		panic(fmt.Sprintf("ble: bad UUID %q: %v", s, err))
	}
	return u
}

// Discovered is a single match from a Scan run. Address is stable on a given Mac
// and is what Connect needs.
type Discovered struct {
	Address    string
	Name       string
	RSSI       int16
	HasService bool // advertised the FE00 service UUID
	NameMatch  bool // LocalName matched one of the known prefixes
}

// adapterOnce guards adapter Enable(); calling it twice on Darwin races the
// CBCentralManager init.
var (
	adapterOnce sync.Once
	adapterErr  error
)

// Adapter returns the singleton bluetooth.Adapter with Enable() called exactly once.
func Adapter() (*bluetooth.Adapter, error) {
	adapterOnce.Do(func() {
		adapterErr = bluetooth.DefaultAdapter.Enable()
	})
	if adapterErr != nil {
		return nil, fmt.Errorf("ble: enable adapter: %w", adapterErr)
	}
	return bluetooth.DefaultAdapter, nil
}

// ScanOptions configures a Scan run.
type ScanOptions struct {
	// Timeout caps the scan duration. Required.
	Timeout time.Duration
	// All disables the WalkingPad service/name filter and returns every
	// advertising device. Useful for diagnosing macOS BLE permission issues:
	// an empty result with All=true almost certainly means CoreBluetooth
	// denied access silently (no permission prompt was accepted).
	All bool
}

// Scan runs a discovery scan with the WalkingPad filter applied. See ScanWith
// for the full option set.
func Scan(ctx context.Context, timeout time.Duration) ([]Discovered, error) {
	return ScanWith(ctx, ScanOptions{Timeout: timeout})
}

// ScanWith runs a discovery scan with the supplied options. Returns every
// matching device that was seen during the window. Duplicates are collapsed
// by address; the strongest seen RSSI is kept.
//
// ScanWith blocks until completion. The macOS CoreBluetooth scanner is global —
// only one Scan can be active at a time on a process.
func ScanWith(ctx context.Context, opts ScanOptions) ([]Discovered, error) {
	if opts.Timeout <= 0 {
		return nil, errors.New("ble: scan timeout must be > 0")
	}
	adapter, err := Adapter()
	if err != nil {
		return nil, err
	}

	var (
		mu       sync.Mutex
		results  = map[string]*Discovered{}
		stopOnce sync.Once
	)

	stop := func() {
		stopOnce.Do(func() {
			_ = adapter.StopScan()
		})
	}

	// Stop the scan when ctx is cancelled or the timeout fires, whichever comes first.
	doneCh := make(chan struct{})
	go func() {
		timer := time.NewTimer(opts.Timeout)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		case <-doneCh:
			return
		}
		stop()
	}()

	scanErr := adapter.Scan(func(_ *bluetooth.Adapter, r bluetooth.ScanResult) {
		hasService := r.HasServiceUUID(ServiceUUID)
		name := r.LocalName()
		nameMatch := matchesLocalName(name)
		if !opts.All && !hasService && !nameMatch {
			return
		}

		addr := r.Address.String()
		mu.Lock()
		defer mu.Unlock()
		prev, ok := results[addr]
		if !ok {
			results[addr] = &Discovered{
				Address:    addr,
				Name:       name,
				RSSI:       r.RSSI,
				HasService: hasService,
				NameMatch:  nameMatch,
			}
			return
		}
		// Keep the strongest signal and the best-known metadata.
		if r.RSSI > prev.RSSI {
			prev.RSSI = r.RSSI
		}
		if prev.Name == "" {
			prev.Name = name
		}
		prev.HasService = prev.HasService || hasService
		prev.NameMatch = prev.NameMatch || nameMatch
	})
	close(doneCh)

	if scanErr != nil && !errors.Is(scanErr, context.Canceled) {
		return nil, fmt.Errorf("ble: scan: %w", scanErr)
	}

	out := make([]Discovered, 0, len(results))
	mu.Lock()
	for _, d := range results {
		out = append(out, *d)
	}
	mu.Unlock()
	return out, nil
}

func matchesLocalName(name string) bool {
	if name == "" {
		return false
	}
	lower := strings.ToLower(name)
	for _, prefix := range localNameMatches {
		if strings.Contains(lower, prefix) {
			return true
		}
	}
	return false
}

// StatusHandler is invoked on every successfully decoded status frame.
// Implementations should not block; the BLE notification thread is the caller.
type StatusHandler func(Status)

// ErrorHandler is invoked on decode errors. The caller's goroutine is the BLE
// notification thread — keep it short.
type ErrorHandler func(error)

// Client is a connected WalkingPad. It owns the BLE device handle plus a
// single goroutine that serialises writes and enforces MinWriteGap. There is
// at most one in-flight BLE connection per process — the daemon is the sole
// owner per PRD §17.2.
type Client struct {
	device bluetooth.Device
	rx     bluetooth.DeviceCharacteristic
	tx     bluetooth.DeviceCharacteristic

	writeCh chan []byte
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// Connect dials addr (as printed by Scan), discovers the WiLink service +
// characteristics, enables status-frame notifications, and starts the
// rate-limited writer goroutine.
//
// onStatus is called for every successfully decoded frame; onErr (may be nil)
// is called on decode failures so callers can count drops. Both run on the
// BLE notification thread — do not block.
//
// The returned Client must be Disconnect()ed to release the BLE slot.
func Connect(ctx context.Context, addr string, onStatus StatusHandler, onErr ErrorHandler) (*Client, error) {
	if onStatus == nil {
		return nil, errors.New("ble: onStatus is required")
	}
	adapter, err := Adapter()
	if err != nil {
		return nil, err
	}

	var devAddr bluetooth.Address
	devAddr.Set(addr)

	// ConnectionParams{} keeps tinygo's defaultConnectionTimeout (10 s on Darwin).
	device, err := adapter.Connect(devAddr, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, fmt.Errorf("ble: connect %s: %w", addr, err)
	}

	rx, tx, err := discoverChars(device)
	if err != nil {
		_ = device.Disconnect()
		return nil, err
	}

	cl := &Client{
		device:  device,
		rx:      rx,
		tx:      tx,
		writeCh: make(chan []byte, 16),
	}

	if err := rx.EnableNotifications(func(buf []byte) {
		cl.dispatch(buf, onStatus, onErr)
	}); err != nil {
		_ = device.Disconnect()
		return nil, fmt.Errorf("ble: enable notifications: %w", err)
	}

	loopCtx, cancel := context.WithCancel(ctx)
	cl.cancel = cancel
	cl.wg.Add(1)
	go cl.writeLoop(loopCtx)

	return cl, nil
}

// discoverChars resolves the FE01 (RX/notify) and FE02 (TX/write) characteristics
// inside the FE00 service. cbgo can return either the 16-bit alias or the full
// 128-bit form, so we match by string-prefix instead of equality.
func discoverChars(device bluetooth.Device) (bluetooth.DeviceCharacteristic, bluetooth.DeviceCharacteristic, error) {
	services, err := device.DiscoverServices([]bluetooth.UUID{ServiceUUID})
	if err != nil {
		return bluetooth.DeviceCharacteristic{}, bluetooth.DeviceCharacteristic{}, fmt.Errorf("ble: discover services: %w", err)
	}
	if len(services) == 0 {
		return bluetooth.DeviceCharacteristic{}, bluetooth.DeviceCharacteristic{}, fmt.Errorf("ble: service %s not found", ServiceUUID)
	}

	var (
		rx, tx           bluetooth.DeviceCharacteristic
		foundRx, foundTx bool
	)
	for _, svc := range services {
		chars, err := svc.DiscoverCharacteristics(nil)
		if err != nil {
			return bluetooth.DeviceCharacteristic{}, bluetooth.DeviceCharacteristic{}, fmt.Errorf("ble: discover characteristics: %w", err)
		}
		for _, c := range chars {
			switch c.UUID() {
			case rxCharUUID:
				rx, foundRx = c, true
			case txCharUUID:
				tx, foundTx = c, true
			}
		}
	}
	if !foundRx || !foundTx {
		return bluetooth.DeviceCharacteristic{}, bluetooth.DeviceCharacteristic{},
			fmt.Errorf("ble: missing characteristics (rx=%t tx=%t)", foundRx, foundTx)
	}
	return rx, tx, nil
}

func (c *Client) dispatch(buf []byte, onStatus StatusHandler, onErr ErrorHandler) {
	if len(buf) < 2 || buf[0] != 0xF8 || buf[1] != typeStd {
		// Non-status frames (e.g. last-record response, type 0xA7) end up here.
		// Silently ignore for the POC; the session manager (Milestone 1) will
		// route these later.
		return
	}
	s, err := DecodeStatus(buf)
	if err != nil {
		if onErr != nil {
			onErr(err)
		}
		return
	}
	onStatus(s)
}

// Write queues frame for the rate-limited writer. Returns true if accepted,
// false if the queue is full (callers may decide to drop or retry).
func (c *Client) Write(frame []byte) bool {
	select {
	case c.writeCh <- frame:
		return true
	default:
		return false
	}
}

// writeLoop drains writeCh, enforcing MinWriteGap between successive
// WriteWithoutResponse calls. Returns on ctx cancellation.
func (c *Client) writeLoop(ctx context.Context) {
	defer c.wg.Done()
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-c.writeCh:
			if elapsed := time.Since(last); elapsed < MinWriteGap {
				wait := MinWriteGap - elapsed
				t := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					t.Stop()
					return
				case <-t.C:
				}
			}
			if _, err := c.tx.WriteWithoutResponse(frame); err != nil {
				// Surface via the next decode or status — the device drops
				// bad frames silently anyway. For the POC, swallow.
				_ = err
			}
			last = time.Now()
		}
	}
}

// Disconnect cancels the writer goroutine and releases the BLE connection.
// Safe to call multiple times; subsequent calls are no-ops.
func (c *Client) Disconnect() error {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	return c.device.Disconnect()
}
