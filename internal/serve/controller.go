package serve

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/api"
	"github.com/jkrumm/king-smith-walkingpad-mac/internal/ble"
)

// linkSender is the subset of *ble.Link the controller needs. Lets tests
// inject a fake without dragging the full Link in.
type linkSender interface {
	Send(frame []byte) error
}

// statusReader is the subset of statusProvider the controller needs to observe
// the belt state. The controller polls it to wait out the 3-2-1 start ramp
// before applying a target speed (see startAtSpeed).
type statusReader interface {
	LastFrame() (ble.Status, time.Time, bool)
}

// Defaults for the wait-for-active loop. The P1 ramps 0x09→0x08→0x07→0x02 over
// ~3 s and frames refresh at the 1 s ask-stats cadence, so active is typically
// visible ~5 s after the start frame is queued. The timeout leaves slack for a
// slow BLE write gap or a reconnect blip; if the belt never reaches active
// (obstruction, child lock) the call fails rather than hanging.
const (
	defaultActivePoll    = 300 * time.Millisecond
	defaultActiveTimeout = 15 * time.Second
)

// controller is the api.Controller implementation backed by a *ble.Link. Each
// method encodes the appropriate WiLink frame and queues it on the link's
// rate-limited writer; multi-frame commands rely on the writer's 700 ms gap to
// keep the device from dropping them.
type controller struct {
	link   linkSender
	status statusReader

	// activePoll / activeTimeout govern the wait-for-active loop. Fields (not
	// constants) so tests can shrink them.
	activePoll    time.Duration
	activeTimeout time.Duration
}

func newController(link linkSender, status statusReader) *controller {
	return &controller{
		link:          link,
		status:        status,
		activePoll:    defaultActivePoll,
		activeTimeout: defaultActiveTimeout,
	}
}

// Start switches to manual mode and gets the belt moving at speedKmh. When a
// speed is requested it starts the belt (if not already running), waits out the
// 3-2-1 ramp, then applies the speed — the P1 ignores set-speed while STOPPED
// or ramping, settling at its stored START_SPEED pref instead, so the bump must
// come after the belt reaches ACTIVE (verified on the user's P1, 2026-05-21).
// speedKmh == 0 means "just start; leave the speed at the device default".
func (c *controller) Start(ctx context.Context, speedKmh float64) error {
	if speedKmh <= 0 {
		return c.ensureRunning()
	}
	return c.startAtSpeed(ctx, speedKmh)
}

// Stop sends set-speed 0 (PRD §8 — confirmed on hardware to halt the belt).
func (c *controller) Stop(_ context.Context) error {
	return c.send(ble.EncodeStopBelt())
}

// SetSpeed sets the live belt speed. If the belt is stopped or still ramping it
// is started first and the speed is applied once it reaches ACTIVE — the same
// post-ramp rule as Start, so a /speed command from STOPPED actually moves the
// belt instead of being silently dropped.
func (c *controller) SetSpeed(ctx context.Context, speedKmh float64) error {
	return c.startAtSpeed(ctx, speedKmh)
}

// SetStartSpeed writes the PrefStartSpeed preference. The wire encoding is
// speed × 10 (single byte slot in the 10-byte pref frame, see PRD §4.3).
func (c *controller) SetStartSpeed(_ context.Context, speedKmh float64) error {
	value := uint32(math.Round(speedKmh * 10))
	return c.send(ble.EncodeSetPref(ble.PrefStartSpeed, 0, value))
}

// startAtSpeed ensures the belt is running, waits for it to reach ACTIVE, then
// applies speedKmh. The wait is the crux: set-speed frames sent during STOPPED
// or the 3-2-1 ramp are dropped by the device.
func (c *controller) startAtSpeed(ctx context.Context, speedKmh float64) error {
	if err := c.ensureRunning(); err != nil {
		return err
	}
	if err := c.waitForActive(ctx); err != nil {
		return err
	}
	frame, err := ble.EncodeSetSpeed(speedKmh)
	if err != nil {
		return fmt.Errorf("encode speed: %w", err)
	}
	return c.send(frame)
}

// ensureRunning switches to manual mode and starts the belt unless it is
// already ACTIVE or mid-ramp. Re-sending the start frame to a running P1 halts
// it (PRD §8 / gotcha #4), so the state check is load-bearing, not an
// optimization.
func (c *controller) ensureRunning() error {
	if state, connected := c.currentState(); connected && (state == ble.BeltActive || state.IsStarting()) {
		return nil
	}
	modeFrame, err := ble.EncodeSetMode(ble.ModeManual)
	if err != nil {
		return fmt.Errorf("encode mode: %w", err)
	}
	if err := c.send(modeFrame); err != nil {
		return err
	}
	return c.send(ble.EncodeStartBelt())
}

// waitForActive blocks until the belt reports ACTIVE, ctx is cancelled, or the
// timeout elapses. Returns nil once active; an error otherwise (the caller then
// skips the set-speed it would have been ignored anyway).
func (c *controller) waitForActive(ctx context.Context) error {
	if state, connected := c.currentState(); connected && state == ble.BeltActive {
		return nil
	}
	ticker := time.NewTicker(c.activePoll)
	defer ticker.Stop()
	deadline := time.NewTimer(c.activeTimeout)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("ble: belt did not reach active within %s", c.activeTimeout)
		case <-ticker.C:
			if state, connected := c.currentState(); connected && state == ble.BeltActive {
				return nil
			}
		}
	}
}

// currentState reads the most recent belt state and link connectivity.
func (c *controller) currentState() (ble.BeltState, bool) {
	frame, _, connected := c.status.LastFrame()
	return frame.State, connected
}

// send translates the BLE-level disconnect into the api-level
// ErrControllerUnavailable so handlers return the documented 503.
func (c *controller) send(frame []byte) error {
	err := c.link.Send(frame)
	if err == nil {
		return nil
	}
	if errors.Is(err, ble.ErrLinkDisconnected) {
		return api.ErrControllerUnavailable
	}
	return err
}
