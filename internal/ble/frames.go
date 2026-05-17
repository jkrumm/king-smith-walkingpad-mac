// Package ble implements the KingSmith WiLink protocol used by the WalkingPad P1.
//
// This file owns the wire-format codec: command encoding, status-frame decoding,
// and CRC. It contains no I/O — the BLE transport lives in client.go.
//
// Protocol reference: PRD.md §4 (reverse-engineered from ph4r05/ph4-walkingpad and
// cross-verified against tim-oster/walkingpad).
package ble

import (
	"errors"
	"fmt"
	"time"
)

// Frame magic bytes and lengths.
const (
	cmdStart    byte = 0xF7
	statusStart byte = 0xF8
	frameEnd    byte = 0xFD

	typeStd    byte = 0xA2
	typePref   byte = 0xA6
	typeRecord byte = 0xA7

	StatusFrameLen = 20
	CmdFrameLen    = 6
	PrefFrameLen   = 10

	// MinWriteGap is the minimum gap between BLE writes. The device drops
	// frames if commands arrive faster than ~1.4 Hz. Owned by the writer
	// goroutine in client.go.
	MinWriteGap = 700 * time.Millisecond

	// MaxSpeedKmh is the P1 hardware ceiling (hardcoded in firmware).
	MaxSpeedKmh = 6.0
)

// BeltState mirrors the raw byte at status frame offset 2.
//
// Verified empirically against the user's P1 (PRD §4.4 footnote): the original
// ph4r05 mapping (1=active, 2=paused) is stale. Current P1 firmware uses byte
// 2 for the running state, never emits byte 1, and cycles 9→8→7 as a 3-2-1
// start-countdown before settling to 2. Byte 4 appears for exactly one frame
// during decel and is the **last chance** to capture in-session counters
// before byte 0 resets them.
type BeltState uint8

// BeltState values. See type doc for the verification source.
const (
	BeltStopped   BeltState = 0 // idle; counters reset to 0 on entry
	BeltActive    BeltState = 2 // running; counters monotonic
	BeltStopping  BeltState = 4 // ~1-frame decel window; counters still hold the run total
	BeltStandby   BeltState = 5 // belt powered down (unverified on P1; preserved from ph4 docs)
	BeltStarting3 BeltState = 9 // start press; first frame of 3-2-1 ramp
	BeltStarting2 BeltState = 8 // 2nd ramp frame
	BeltStarting1 BeltState = 7 // 3rd ramp frame; next frame is BeltActive
)

func (s BeltState) String() string {
	switch s {
	case BeltStopped:
		return "stopped"
	case BeltActive:
		return "active"
	case BeltStopping:
		return "stopping"
	case BeltStandby:
		return "standby"
	case BeltStarting1, BeltStarting2, BeltStarting3:
		return "starting"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(s))
	}
}

// IsStarting reports whether the belt is in any of the 3-2-1 ramp frames.
func (s BeltState) IsStarting() bool {
	return s == BeltStarting1 || s == BeltStarting2 || s == BeltStarting3
}

// IsRunning reports whether counters are advancing this frame (active or
// the final decel frame, both of which carry real time/distance/steps).
func (s BeltState) IsRunning() bool { return s == BeltActive || s == BeltStopping }

// Mode is the belt operating mode (auto/manual/standby).
type Mode uint8

// Mode values (see PRD §4.3).
const (
	ModeAuto    Mode = 0
	ModeManual  Mode = 1
	ModeStandby Mode = 2
)

func (m Mode) String() string {
	switch m {
	case ModeAuto:
		return "auto"
	case ModeManual:
		return "manual"
	case ModeStandby:
		return "standby"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(m))
	}
}

// Button is the value of the physical remote button at status offset 16.
//
// The high bit (0x80) is a modifier observed empirically (likely "long-press"
// or "held"); DecodeStatus masks it before assigning, so callers compare only
// against the low bits. The raw byte is still available via Status.Raw[16].
type Button uint8

// Physical-remote button values reported in the status frame.
//
// Verified empirically: the P1 remote has a single power button (used both
// for start and stop) which fires ButtonPower=3 — the PRD's "3=stop" label
// from ph4r05 was incomplete.
const (
	ButtonNone  Button = 0
	ButtonUp    Button = 2
	ButtonPower Button = 3 // start AND stop (single physical button on the P1 remote)
	ButtonDown  Button = 4
)

// buttonMask strips the high modifier bit (e.g. long-press indicator).
const buttonMask byte = 0x7F

// PrefKey is the preference key used with the 10-byte set-pref command (0xA6).
type PrefKey uint8

// Preference keys for the 0xA6 set-pref command (see PRD §4.3).
const (
	PrefTarget      PrefKey = 1 // sub: 0=none, 1=dist, 2=cal, 3=time
	PrefMaxSpeed    PrefKey = 3 // value: speed × 10
	PrefStartSpeed  PrefKey = 4 // value: speed × 10
	PrefStartIntel  PrefKey = 5 // value: 0|1
	PrefSensitivity PrefKey = 6 // value: 1=high, 2=med, 3=low
	PrefDisplay     PrefKey = 7 // value: 7-bit display bitmask
	PrefUnits       PrefKey = 8 // value: 0=km, 1=miles
	PrefChildLock   PrefKey = 9 // value: 0|1
)

// Status is a decoded 20-byte device→controller status frame.
type Status struct {
	State    BeltState
	SpeedKmh float64 // 0.0 – 6.0
	Mode     Mode
	Time     time.Duration // current session active time
	Distance float64       // meters
	Steps    uint32
	AppSpeed uint8 // last commanded speed × 10 (semantics fuzzy)
	Button   Button
	Raw      [StatusFrameLen]byte
}

// ErrBadCRC indicates the frame CRC did not match. Frames with bad CRCs should be dropped.
var ErrBadCRC = errors.New("ble: bad CRC")

// crc returns sum(buf[1:len(buf)-2]) & 0xFF — the KingSmith WiLink checksum.
//
// Scope is exclusive of the start byte AND the CRC slot AND the terminator.
// Verified against ph4r05/ph4-walkingpad (pad.py:fix_crc) and the PRD §4.4
// worked example. (PRD §4.4 lists status CRC offset as 17; the actual wire
// has it at offset 18. The CRC scope formula is the source of truth.)
func crc(buf []byte) byte {
	if len(buf) < 3 {
		return 0
	}
	var s uint8
	for _, b := range buf[1 : len(buf)-2] {
		s += b // 8-bit overflow is intentional
	}
	return s
}

// DecodeStatus parses a 20-byte status frame. Returns ErrBadCRC if the checksum
// fails; the caller should drop the frame and wait for the next one.
func DecodeStatus(buf []byte) (Status, error) {
	if len(buf) != StatusFrameLen {
		return Status{}, fmt.Errorf("ble: status frame length %d, want %d", len(buf), StatusFrameLen)
	}
	if buf[0] != statusStart || buf[1] != typeStd {
		return Status{}, fmt.Errorf("ble: status header %02x %02x, want %02x %02x",
			buf[0], buf[1], statusStart, typeStd)
	}
	if buf[StatusFrameLen-1] != frameEnd {
		return Status{}, fmt.Errorf("ble: status terminator %02x, want %02x",
			buf[StatusFrameLen-1], frameEnd)
	}
	if got, want := buf[StatusFrameLen-2], crc(buf); got != want {
		return Status{}, fmt.Errorf("%w: got %02x want %02x", ErrBadCRC, got, want)
	}

	var s Status
	copy(s.Raw[:], buf)
	s.State = BeltState(buf[2])
	s.SpeedKmh = float64(buf[3]) / 10.0
	s.Mode = Mode(buf[4])
	s.Time = time.Duration(uint24BE(buf[5:8])) * time.Second
	s.Distance = float64(uint24BE(buf[8:11])) * 10.0 // wire unit is 10 m
	s.Steps = uint24BE(buf[11:14])
	s.AppSpeed = buf[14]
	s.Button = Button(buf[16] & buttonMask)
	return s, nil
}

func uint24BE(b []byte) uint32 {
	_ = b[2] // bounds check elimination hint
	return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
}

// putUint24BE writes v as 24-bit big-endian into b. The high 8 bits of v are
// silently dropped (callers are expected to pass values that fit in 24 bits).
func putUint24BE(b []byte, v uint32) {
	_ = b[2]
	b[0] = byte((v >> 16) & 0xFF)
	b[1] = byte((v >> 8) & 0xFF)
	b[2] = byte(v & 0xFF)
}

// stdCmd builds a 6-byte F7 A2 op param CRC FD frame.
func stdCmd(op, param byte) []byte {
	f := []byte{cmdStart, typeStd, op, param, 0x00, frameEnd}
	f[CmdFrameLen-2] = crc(f)
	return f
}

// EncodeAskStats requests the current status frame.
func EncodeAskStats() []byte { return stdCmd(0x00, 0x00) }

// EncodeStartBelt starts the belt in the currently active mode.
func EncodeStartBelt() []byte { return stdCmd(0x04, 0x01) }

// EncodeBeep sends the post-connect ack used by ph4r05. Purpose is unconfirmed
// but ph4 always sends it after connecting.
func EncodeBeep() []byte { return stdCmd(0x03, 0x07) }

// EncodeLastRecord requests the last-run record. The device replies with a
// type-0xA7 status frame (separate from the 0xA2 stream); see ph4r05's
// WalkingPadLastStatus for the layout. The session manager (Milestone 1) is
// the consumer — for the POC the response is silently dropped in
// Client.dispatch.
func EncodeLastRecord() []byte {
	f := []byte{cmdStart, typeRecord, 0xAA, 0xFF, 0x00, frameEnd}
	f[CmdFrameLen-2] = crc(f)
	return f
}

// EncodeSetSpeed encodes a speed-change command. speedKmh must be in [0, MaxSpeedKmh].
// Pass 0 to stop the belt.
func EncodeSetSpeed(speedKmh float64) ([]byte, error) {
	if speedKmh < 0 || speedKmh > MaxSpeedKmh {
		return nil, fmt.Errorf("ble: speed %.2f km/h out of range [0, %.1f]", speedKmh, MaxSpeedKmh)
	}
	n := byte(speedKmh*10 + 0.5)
	return stdCmd(0x01, n), nil
}

// EncodeStopBelt is a convenience for EncodeSetSpeed(0).
func EncodeStopBelt() []byte {
	b, _ := EncodeSetSpeed(0)
	return b
}

// EncodeSetMode switches between auto / manual / standby.
func EncodeSetMode(m Mode) ([]byte, error) {
	switch m {
	case ModeAuto, ModeManual, ModeStandby:
		return stdCmd(0x02, byte(m)), nil
	default:
		return nil, fmt.Errorf("ble: invalid mode %d", uint8(m))
	}
}

// EncodeSetPref builds the 10-byte preference command F7 A6 KEY SUB V0 V1 V2 AC CRC FD.
// value is treated as a 24-bit big-endian payload (the high 8 bits are ignored).
func EncodeSetPref(key PrefKey, sub byte, value uint32) []byte {
	f := []byte{
		cmdStart, typePref, byte(key), sub,
		0, 0, 0,
		0xAC,
		0x00,
		frameEnd,
	}
	putUint24BE(f[4:7], value)
	f[PrefFrameLen-2] = crc(f)
	return f
}
