package api

import (
	"context"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/ble"
)

// Controller is the BLE write surface the API exposes. Step 6 wires a real
// implementation backed by ble.Client; tests inject fakes.
//
// Speed values are post-validation: handlers reject anything outside
// [0.5, 6.0] for SetSpeed/SetStartSpeed and round to 0.1 km/h. Start accepts
// speedKmh = 0, meaning "just send the start command without resetting speed".
type Controller interface {
	Start(ctx context.Context, speedKmh float64) error
	Stop(ctx context.Context) error
	SetSpeed(ctx context.Context, speedKmh float64) error
	SetStartSpeed(ctx context.Context, speedKmh float64) error
}

// StatusProvider exposes the daemon's live BLE state to the API. Step 6 wires
// a provider that the BLE notification handler keeps updated.
type StatusProvider interface {
	// LastFrame returns the most recent decoded frame, when it arrived, and
	// whether the BLE link is currently connected. observedAt is the zero
	// time when no frame has been received yet.
	LastFrame() (frame ble.Status, observedAt time.Time, connected bool)
	// DeviceInfo returns metadata about the bound peripheral, if any.
	DeviceInfo() DeviceInfo
}

// DeviceInfo mirrors the per-device fields the /status JSON exposes.
type DeviceInfo struct {
	Name    string
	Address string
	RSSI    int
}

// Syncer triggers an Argo upload on demand. Step 9 wires the real worker.
type Syncer interface {
	SyncNow(ctx context.Context) (synced, failed int, err error)
}

// NopController returns ErrControllerUnavailable for every write. Used until
// step 6 wires a real Controller and as a safe default for tests that don't
// exercise the write path.
type NopController struct{}

// Start implements Controller.
func (NopController) Start(context.Context, float64) error { return ErrControllerUnavailable }

// Stop implements Controller.
func (NopController) Stop(context.Context) error { return ErrControllerUnavailable }

// SetSpeed implements Controller.
func (NopController) SetSpeed(context.Context, float64) error { return ErrControllerUnavailable }

// SetStartSpeed implements Controller.
func (NopController) SetStartSpeed(context.Context, float64) error {
	return ErrControllerUnavailable
}

// NopStatus returns a disconnected snapshot. Step 6 swaps in the real provider.
type NopStatus struct{}

// LastFrame implements StatusProvider.
func (NopStatus) LastFrame() (ble.Status, time.Time, bool) { return ble.Status{}, time.Time{}, false }

// DeviceInfo implements StatusProvider.
func (NopStatus) DeviceInfo() DeviceInfo { return DeviceInfo{} }

// NopSyncer reports "sync disabled" — handlers translate this to 503. Step 9
// supplies the real Argo worker.
type NopSyncer struct{}

// SyncNow implements Syncer.
func (NopSyncer) SyncNow(context.Context) (int, int, error) { return 0, 0, ErrSyncDisabled }
