package serve

import (
	"sync"
	"testing"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/ble"
)

func TestStatusProvider_RoundTrip(t *testing.T) {
	sp := newStatusProvider("WalkingPad")

	// Default: disconnected, empty frame.
	frame, ts, connected := sp.LastFrame()
	if connected || !ts.IsZero() || frame.State != ble.BeltStopped {
		t.Errorf("initial state wrong: connected=%v ts=%v state=%v", connected, ts, frame.State)
	}
	if got := sp.DeviceInfo(); got.Name != "WalkingPad" || got.Address != "" {
		t.Errorf("device info before connect: %+v", got)
	}

	// Connect.
	sp.markConnected("addr-1")
	if got := sp.DeviceInfo(); got.Address != "addr-1" {
		t.Errorf("device addr after connect: %q", got.Address)
	}
	if _, _, c := sp.LastFrame(); !c {
		t.Error("expected connected=true")
	}

	// Observe a frame.
	now := time.Date(2026, 5, 17, 14, 0, 0, 0, time.UTC)
	sp.observe(ble.Status{State: ble.BeltActive, SpeedKmh: 4.5}, now)
	frame, ts, connected = sp.LastFrame()
	if !connected || !ts.Equal(now) || frame.State != ble.BeltActive || frame.SpeedKmh != 4.5 {
		t.Errorf("observe round-trip: connected=%v ts=%v frame=%+v", connected, ts, frame)
	}

	// Disconnect preserves the last frame for the UI.
	sp.markDisconnected()
	frame, _, connected = sp.LastFrame()
	if connected {
		t.Error("expected connected=false after disconnect")
	}
	if frame.SpeedKmh != 4.5 {
		t.Errorf("last frame should survive disconnect: %+v", frame)
	}
}

func TestStatusProvider_ConcurrentReadersAndWriter(t *testing.T) {
	// Race-detector smoke test: one observer + many readers in parallel.
	sp := newStatusProvider("WalkingPad")
	sp.markConnected("a")

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				sp.observe(ble.Status{State: ble.BeltActive, SpeedKmh: 3}, time.Now())
			}
		}
	}()

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _, _ = sp.LastFrame()
					_ = sp.DeviceInfo()
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
