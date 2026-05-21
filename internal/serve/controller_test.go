package serve

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jkrumm/king-smith-walkingpad-mac/internal/api"
	"github.com/jkrumm/king-smith-walkingpad-mac/internal/ble"
)

type fakeSender struct {
	mu     sync.Mutex
	frames [][]byte
	err    error
}

func (f *fakeSender) Send(frame []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.frames = append(f.frames, append([]byte(nil), frame...))
	return nil
}

func (f *fakeSender) snapshot() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.frames))
	copy(out, f.frames)
	return out
}

// fakeStateReader returns belt states from seq, one per LastFrame call, with
// the last entry sticky. Models the device transitioning STOPPED → ACTIVE
// across the start ramp so the controller's wait-for-active loop terminates.
type fakeStateReader struct {
	mu        sync.Mutex
	seq       []ble.BeltState
	connected bool
	i         int
}

func (f *fakeStateReader) LastFrame() (ble.Status, time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := ble.BeltStopped
	if len(f.seq) > 0 {
		idx := f.i
		if idx >= len(f.seq) {
			idx = len(f.seq) - 1
		}
		state = f.seq[idx]
		f.i++
	}
	return ble.Status{State: state}, time.Time{}, f.connected
}

// newTestController builds a controller with a tiny poll/timeout so wait loops
// resolve instantly in tests.
func newTestController(s linkSender, r statusReader) *controller {
	c := newController(s, r)
	c.activePoll = time.Millisecond
	c.activeTimeout = 200 * time.Millisecond
	return c
}

// equalFrame compares a sent frame to the expected encoder output.
func equalFrame(got, want []byte) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestController_Start_NoSpeed_SendsModeAndStart(t *testing.T) {
	s := &fakeSender{}
	c := newTestController(s, &fakeStateReader{connected: true, seq: []ble.BeltState{ble.BeltStopped}})
	if err := c.Start(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	frames := s.snapshot()
	if len(frames) != 2 {
		t.Fatalf("frames sent = %d, want 2 (mode + start)", len(frames))
	}
	wantMode, _ := ble.EncodeSetMode(ble.ModeManual)
	if !equalFrame(frames[0], wantMode) {
		t.Errorf("frame[0] != EncodeSetMode(manual)")
	}
	if !equalFrame(frames[1], ble.EncodeStartBelt()) {
		t.Errorf("frame[1] != EncodeStartBelt()")
	}
}

// Cold start with a speed must send mode → start → speed in that order, with
// the speed applied only after the belt reaches ACTIVE.
func TestController_Start_WithSpeed_SendsModeStartThenSpeed(t *testing.T) {
	s := &fakeSender{}
	// STOPPED for the ensureRunning check, then ACTIVE for the wait loop.
	c := newTestController(s, &fakeStateReader{connected: true, seq: []ble.BeltState{ble.BeltStopped, ble.BeltActive}})
	if err := c.Start(context.Background(), 3.5); err != nil {
		t.Fatal(err)
	}
	frames := s.snapshot()
	if len(frames) != 3 {
		t.Fatalf("frames sent = %d, want 3 (mode, start, speed)", len(frames))
	}
	wantMode, _ := ble.EncodeSetMode(ble.ModeManual)
	wantSpeed, _ := ble.EncodeSetSpeed(3.5)
	if !equalFrame(frames[0], wantMode) {
		t.Errorf("frame[0] != EncodeSetMode(manual)")
	}
	if !equalFrame(frames[1], ble.EncodeStartBelt()) {
		t.Errorf("frame[1] != EncodeStartBelt()")
	}
	if !equalFrame(frames[2], wantSpeed) {
		t.Errorf("frame[2] != EncodeSetSpeed(3.5)")
	}
}

// When the belt is already ramping (3-2-1), Start must NOT re-send the start
// frame (that halts a running P1) — only wait for ACTIVE then set the speed.
func TestController_Start_WhileRamping_WaitsThenSetsSpeed(t *testing.T) {
	s := &fakeSender{}
	c := newTestController(s, &fakeStateReader{connected: true, seq: []ble.BeltState{ble.BeltStarting2, ble.BeltActive}})
	if err := c.Start(context.Background(), 3.0); err != nil {
		t.Fatal(err)
	}
	frames := s.snapshot()
	wantSpeed, _ := ble.EncodeSetSpeed(3.0)
	if len(frames) != 1 || !equalFrame(frames[0], wantSpeed) {
		t.Fatalf("frames = %d, want single EncodeSetSpeed(3.0) (no re-start)", len(frames))
	}
}

// SetSpeed on an already-active belt is a single live set-speed frame, no
// start sequence.
func TestController_SetSpeed_WhileActive(t *testing.T) {
	s := &fakeSender{}
	c := newTestController(s, &fakeStateReader{connected: true, seq: []ble.BeltState{ble.BeltActive}})
	if err := c.SetSpeed(context.Background(), 4.5); err != nil {
		t.Fatal(err)
	}
	want, _ := ble.EncodeSetSpeed(4.5)
	frames := s.snapshot()
	if len(frames) != 1 || !equalFrame(frames[0], want) {
		t.Errorf("SetSpeed frame mismatch: got %d frames", len(frames))
	}
}

// SetSpeed from a STOPPED belt must start it (mode + start), wait for ACTIVE,
// then apply the speed — a /speed call from rest moves the belt.
func TestController_SetSpeed_FromStopped_StartsBelt(t *testing.T) {
	s := &fakeSender{}
	c := newTestController(s, &fakeStateReader{connected: true, seq: []ble.BeltState{ble.BeltStopped, ble.BeltActive}})
	if err := c.SetSpeed(context.Background(), 2.5); err != nil {
		t.Fatal(err)
	}
	frames := s.snapshot()
	if len(frames) != 3 {
		t.Fatalf("frames sent = %d, want 3 (mode, start, speed)", len(frames))
	}
	wantMode, _ := ble.EncodeSetMode(ble.ModeManual)
	wantSpeed, _ := ble.EncodeSetSpeed(2.5)
	if !equalFrame(frames[0], wantMode) || !equalFrame(frames[1], ble.EncodeStartBelt()) || !equalFrame(frames[2], wantSpeed) {
		t.Errorf("expected mode, start, speed; got %v", frames)
	}
}

// If the belt never reaches ACTIVE, the speed bump is abandoned and an error
// surfaces — better than silently sending a frame the device will drop.
func TestController_StartAtSpeed_TimesOutIfNeverActive(t *testing.T) {
	s := &fakeSender{}
	c := newTestController(s, &fakeStateReader{connected: true, seq: []ble.BeltState{ble.BeltStopped}})
	err := c.SetSpeed(context.Background(), 3.0)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	// mode + start were sent; the speed frame was not.
	frames := s.snapshot()
	if len(frames) != 2 {
		t.Errorf("frames sent = %d, want 2 (mode + start, no speed)", len(frames))
	}
}

func TestController_StartAtSpeed_RespectsContextCancel(t *testing.T) {
	s := &fakeSender{}
	c := newTestController(s, &fakeStateReader{connected: true, seq: []ble.BeltState{ble.BeltStopped}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.SetSpeed(ctx, 3.0); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func TestController_Stop_SendsSpeedZero(t *testing.T) {
	s := &fakeSender{}
	c := newTestController(s, &fakeStateReader{connected: true})
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	frames := s.snapshot()
	if len(frames) != 1 || !equalFrame(frames[0], ble.EncodeStopBelt()) {
		t.Errorf("stop must send a single EncodeStopBelt frame")
	}
}

func TestController_SetStartSpeed_EncodesValueTimesTen(t *testing.T) {
	s := &fakeSender{}
	c := newTestController(s, &fakeStateReader{connected: true})
	if err := c.SetStartSpeed(context.Background(), 2.0); err != nil {
		t.Fatal(err)
	}
	want := ble.EncodeSetPref(ble.PrefStartSpeed, 0, 20) // 2.0 × 10
	frames := s.snapshot()
	if len(frames) != 1 || !equalFrame(frames[0], want) {
		t.Errorf("SetStartSpeed frame mismatch")
	}
}

func TestController_DisconnectedReturnsUnavailable(t *testing.T) {
	s := &fakeSender{err: ble.ErrLinkDisconnected}
	c := newTestController(s, &fakeStateReader{connected: true})
	err := c.Stop(context.Background())
	if !errors.Is(err, api.ErrControllerUnavailable) {
		t.Errorf("got %v, want ErrControllerUnavailable", err)
	}
}

func TestController_OtherErrorPropagated(t *testing.T) {
	custom := errors.New("queue full")
	s := &fakeSender{err: custom}
	c := newTestController(s, &fakeStateReader{connected: true})
	if err := c.Stop(context.Background()); !errors.Is(err, custom) {
		t.Errorf("non-disconnect error must propagate: got %v", err)
	}
}
