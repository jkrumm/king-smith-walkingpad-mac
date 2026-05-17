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

// ServiceUUID is the KingSmith WiLink primary service (0xFE00 alias). See PRD §4.1.
// The macOS scanner cannot pre-filter; we filter in the discovery callback by
// either service-UUID match or LocalName match.
var ServiceUUID = mustUUID("0000fe00-0000-1000-8000-00805f9b34fb")

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

// Scan runs a discovery scan for the given timeout (or until ctx is cancelled),
// returning every device that either advertised the WalkingPad service UUID or
// whose LocalName matched a known prefix. Duplicates are collapsed by address;
// the strongest seen RSSI is kept.
//
// Scan blocks until completion. The macOS CoreBluetooth scanner is global —
// only one Scan can be active at a time on a process.
func Scan(ctx context.Context, timeout time.Duration) ([]Discovered, error) {
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
		timer := time.NewTimer(timeout)
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
		if !hasService && !nameMatch {
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
