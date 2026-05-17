package serve

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/api"
	"github.com/jkrumm/king-smith-walkingpad-mac/internal/ble"
)

// linkSender is the subset of *ble.Link the controller needs. Lets tests
// inject a fake without dragging the full Link in.
type linkSender interface {
	Send(frame []byte) error
}

// controller is the api.Controller implementation backed by a *ble.Link. Each
// method encodes the appropriate WiLink frame and queues it on the link's
// rate-limited writer; multi-frame commands rely on the writer's 700 ms gap to
// keep the device from dropping them.
type controller struct {
	link linkSender
}

func newController(link linkSender) *controller { return &controller{link: link} }

// Start switches to manual mode, optionally sets the requested speed, then
// sends the start command (PRD §8). speedKmh == 0 means "just start; leave
// the current speed alone".
func (c *controller) Start(_ context.Context, speedKmh float64) error {
	modeFrame, err := ble.EncodeSetMode(ble.ModeManual)
	if err != nil {
		return fmt.Errorf("encode mode: %w", err)
	}
	if err := c.send(modeFrame); err != nil {
		return err
	}
	if speedKmh > 0 {
		speedFrame, err := ble.EncodeSetSpeed(speedKmh)
		if err != nil {
			return fmt.Errorf("encode speed: %w", err)
		}
		if err := c.send(speedFrame); err != nil {
			return err
		}
	}
	return c.send(ble.EncodeStartBelt())
}

// Stop sends set-speed 0 (PRD §8 — confirmed on hardware to halt the belt).
func (c *controller) Stop(_ context.Context) error {
	return c.send(ble.EncodeStopBelt())
}

// SetSpeed sets the live belt speed.
func (c *controller) SetSpeed(_ context.Context, speedKmh float64) error {
	frame, err := ble.EncodeSetSpeed(speedKmh)
	if err != nil {
		return fmt.Errorf("encode speed: %w", err)
	}
	return c.send(frame)
}

// SetStartSpeed writes the PrefStartSpeed preference. The wire encoding is
// speed × 10 (single byte slot in the 10-byte pref frame, see PRD §4.3).
func (c *controller) SetStartSpeed(_ context.Context, speedKmh float64) error {
	value := uint32(math.Round(speedKmh * 10))
	return c.send(ble.EncodeSetPref(ble.PrefStartSpeed, 0, value))
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
