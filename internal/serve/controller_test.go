package serve

import (
	"context"
	"errors"
	"sync"
	"testing"

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
	c := newController(s)
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

func TestController_Start_WithSpeed_SendsModeSpeedStart(t *testing.T) {
	s := &fakeSender{}
	c := newController(s)
	if err := c.Start(context.Background(), 3.5); err != nil {
		t.Fatal(err)
	}
	frames := s.snapshot()
	if len(frames) != 3 {
		t.Fatalf("frames sent = %d, want 3", len(frames))
	}
	wantSpeed, _ := ble.EncodeSetSpeed(3.5)
	if !equalFrame(frames[1], wantSpeed) {
		t.Errorf("frame[1] != EncodeSetSpeed(3.5)")
	}
}

func TestController_Stop_SendsSpeedZero(t *testing.T) {
	s := &fakeSender{}
	c := newController(s)
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	frames := s.snapshot()
	if len(frames) != 1 || !equalFrame(frames[0], ble.EncodeStopBelt()) {
		t.Errorf("stop must send a single EncodeStopBelt frame")
	}
}

func TestController_SetSpeed(t *testing.T) {
	s := &fakeSender{}
	c := newController(s)
	if err := c.SetSpeed(context.Background(), 4.5); err != nil {
		t.Fatal(err)
	}
	want, _ := ble.EncodeSetSpeed(4.5)
	frames := s.snapshot()
	if len(frames) != 1 || !equalFrame(frames[0], want) {
		t.Errorf("SetSpeed frame mismatch")
	}
}

func TestController_SetStartSpeed_EncodesValueTimesTen(t *testing.T) {
	s := &fakeSender{}
	c := newController(s)
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
	c := newController(s)
	err := c.Stop(context.Background())
	if !errors.Is(err, api.ErrControllerUnavailable) {
		t.Errorf("got %v, want ErrControllerUnavailable", err)
	}
}

func TestController_OtherErrorPropagated(t *testing.T) {
	custom := errors.New("queue full")
	s := &fakeSender{err: custom}
	c := newController(s)
	if err := c.Stop(context.Background()); !errors.Is(err, custom) {
		t.Errorf("non-disconnect error must propagate: got %v", err)
	}
}
