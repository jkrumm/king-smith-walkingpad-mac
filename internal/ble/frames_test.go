package ble

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

// mustHex decodes a hex string (whitespace allowed) into a byte slice.
func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	clean := strings.NewReplacer(" ", "", "\t", "", "\n", "").Replace(s)
	b, err := hex.DecodeString(clean)
	if err != nil {
		t.Fatalf("invalid hex fixture %q: %v", s, err)
	}
	return b
}

func TestCRC(t *testing.T) {
	cases := []struct {
		name string
		hex  string
		want byte
	}{
		// 6-byte commands: scope sum(buf[1:4])
		{"ask_stats", "f7 a2 00 00 a2 fd", 0xA2},
		{"beep", "f7 a2 03 07 ac fd", 0xAC},
		{"last_record", "f7 a7 aa ff 50 fd", 0x50},
		// status frame from PRD §4.4 worked example: scope sum(buf[1:18])
		{"status_sample", "f8 a2 01 0f 01 00 0f d1 00 00 ab 00 12 ae 3c 00 00 00 3a fd", 0x3A},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := mustHex(t, tc.hex)
			if got := crc(buf); got != tc.want {
				t.Fatalf("crc = %02x, want %02x", got, tc.want)
			}
		})
	}
}

// TestDecodeStatus_Fixture is the PRD §4.4 worked example.
//
// The fixture's state byte is 0x01 — that value is undocumented in current
// firmware (the P1 uses 0x02 for active). We still decode it correctly into
// the typed enum (as the catch-all "unknown(1)" via the default String case)
// and the numeric fields are unaffected.
func TestDecodeStatus_Fixture(t *testing.T) {
	buf := mustHex(t, "f8 a2 01 0f 01 00 0f d1 00 00 ab 00 12 ae 3c 00 00 00 3a fd")

	s, err := DecodeStatus(buf)
	if err != nil {
		t.Fatalf("DecodeStatus error: %v", err)
	}
	if s.State != BeltState(1) {
		t.Errorf("State = %v, want raw byte 1", s.State)
	}
	if s.SpeedKmh != 1.5 {
		t.Errorf("SpeedKmh = %v, want 1.5", s.SpeedKmh)
	}
	if s.Mode != ModeManual {
		t.Errorf("Mode = %v, want manual", s.Mode)
	}
	if s.Time != 4049*time.Second {
		t.Errorf("Time = %v, want 4049s", s.Time)
	}
	if s.Distance != 1710 {
		t.Errorf("Distance = %v m, want 1710 m", s.Distance)
	}
	if s.Steps != 4782 {
		t.Errorf("Steps = %d, want 4782", s.Steps)
	}
	if s.AppSpeed != 0x3C {
		t.Errorf("AppSpeed = %02x, want 3c", s.AppSpeed)
	}
	if s.Button != ButtonNone {
		t.Errorf("Button = %v, want none", s.Button)
	}
	if s.Raw[0] != 0xF8 || s.Raw[StatusFrameLen-1] != 0xFD {
		t.Errorf("Raw not copied correctly")
	}
}

func TestDecodeStatus_Errors(t *testing.T) {
	good := mustHex(t, "f8 a2 01 0f 01 00 0f d1 00 00 ab 00 12 ae 3c 00 00 00 3a fd")

	cases := []struct {
		name    string
		mutate  func([]byte) []byte
		wantErr string
		isCRC   bool
	}{
		{
			name:    "wrong length",
			mutate:  func(b []byte) []byte { return b[:19] },
			wantErr: "length",
		},
		{
			name:    "bad start byte",
			mutate:  func(b []byte) []byte { c := append([]byte(nil), b...); c[0] = 0xAA; return c },
			wantErr: "header",
		},
		{
			name:    "bad type byte",
			mutate:  func(b []byte) []byte { c := append([]byte(nil), b...); c[1] = 0xA3; return c },
			wantErr: "header",
		},
		{
			name:    "bad terminator",
			mutate:  func(b []byte) []byte { c := append([]byte(nil), b...); c[19] = 0x00; return c },
			wantErr: "terminator",
		},
		{
			name:    "bad CRC",
			mutate:  func(b []byte) []byte { c := append([]byte(nil), b...); c[18] = 0x00; return c },
			wantErr: "CRC",
			isCRC:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeStatus(tc.mutate(good))
			if err == nil {
				t.Fatalf("expected error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q, want containing %q", err.Error(), tc.wantErr)
			}
			if tc.isCRC && !errors.Is(err, ErrBadCRC) {
				t.Errorf("error %q, want errors.Is(ErrBadCRC)", err.Error())
			}
		})
	}
}

func TestDecodeStatus_StateAndMode(t *testing.T) {
	// Build a synthetic frame for every verified belt state and mode.
	base := mustHex(t, "f8 a2 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 fd")
	states := []BeltState{
		BeltStopped, BeltActive, BeltStopping, BeltStandby,
		BeltStarting1, BeltStarting2, BeltStarting3,
	}
	modes := []Mode{ModeAuto, ModeManual, ModeStandby}
	for _, st := range states {
		for _, md := range modes {
			f := append([]byte(nil), base...)
			f[2] = byte(st)
			f[4] = byte(md)
			f[18] = crc(f)
			got, err := DecodeStatus(f)
			if err != nil {
				t.Fatalf("state=%v mode=%v: %v", st, md, err)
			}
			if got.State != st || got.Mode != md {
				t.Errorf("state/mode mismatch: got %v/%v want %v/%v", got.State, got.Mode, st, md)
			}
		}
	}
}

// TestBeltState_Helpers locks the IsStarting/IsRunning semantics that the
// session manager will rely on.
func TestBeltState_Helpers(t *testing.T) {
	cases := []struct {
		state      BeltState
		isStarting bool
		isRunning  bool
	}{
		{BeltStopped, false, false},
		{BeltActive, false, true},
		{BeltStopping, false, true}, // counters still valid; must be captured
		{BeltStandby, false, false},
		{BeltStarting1, true, false},
		{BeltStarting2, true, false},
		{BeltStarting3, true, false},
		{BeltState(99), false, false}, // unknown bytes fall through cleanly
	}
	for _, tc := range cases {
		t.Run(tc.state.String(), func(t *testing.T) {
			if got := tc.state.IsStarting(); got != tc.isStarting {
				t.Errorf("IsStarting = %v, want %v", got, tc.isStarting)
			}
			if got := tc.state.IsRunning(); got != tc.isRunning {
				t.Errorf("IsRunning = %v, want %v", got, tc.isRunning)
			}
		})
	}
}

// TestDecodeStatus_ButtonMask covers the empirically-observed 0x83 modifier:
// the high bit (likely long-press) must be stripped before assignment so
// callers only compare against the documented low-bit values.
func TestDecodeStatus_ButtonMask(t *testing.T) {
	base := mustHex(t, "f8 a2 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 fd")
	cases := []struct {
		name      string
		rawButton byte
		want      Button
	}{
		{"none", 0x00, ButtonNone},
		{"up", 0x02, ButtonUp},
		{"power", 0x03, ButtonPower},
		{"down", 0x04, ButtonDown},
		{"power held (0x83)", 0x83, ButtonPower},
		{"up held (0x82)", 0x82, ButtonUp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := append([]byte(nil), base...)
			f[16] = tc.rawButton
			f[18] = crc(f)
			s, err := DecodeStatus(f)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if s.Button != tc.want {
				t.Errorf("Button = %v, want %v", s.Button, tc.want)
			}
			if s.Raw[16] != tc.rawButton {
				t.Errorf("Raw[16] = %02x, want %02x (raw byte must be preserved)", s.Raw[16], tc.rawButton)
			}
		})
	}
}

// roundTrip rebuilds a frame from its decoded value and asserts the bytes match.
// Useful for catching off-by-one mistakes in uint24 BE encoding.
func TestUint24RoundTrip(t *testing.T) {
	values := []uint32{0, 1, 0xAB, 0x000FD1, 0x0012AE, 0x00FFFFFF, 0xABCDEF}
	var buf [3]byte
	for _, v := range values {
		putUint24BE(buf[:], v)
		if got := uint24BE(buf[:]); got != v {
			t.Errorf("uint24 round-trip: got %x, want %x", got, v)
		}
	}
}

func TestEncodeCommands(t *testing.T) {
	cases := []struct {
		name string
		got  []byte
		want string
	}{
		{"ask_stats", EncodeAskStats(), "f7 a2 00 00 a2 fd"},
		{"start_belt", EncodeStartBelt(), "f7 a2 04 01 a7 fd"},
		{"beep", EncodeBeep(), "f7 a2 03 07 ac fd"},
		{"last_record", EncodeLastRecord(), "f7 a7 aa ff 50 fd"},
		{"stop_belt", EncodeStopBelt(), "f7 a2 01 00 a3 fd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := mustHex(t, tc.want)
			if !bytesEqual(tc.got, want) {
				t.Fatalf("encoded %x, want %x", tc.got, want)
			}
			// Sanity: every encoded frame's CRC is self-consistent.
			if got, w := tc.got[len(tc.got)-2], crc(tc.got); got != w {
				t.Errorf("self-CRC %02x, want %02x", got, w)
			}
		})
	}
}

func TestEncodeSetSpeed(t *testing.T) {
	cases := []struct {
		name  string
		speed float64
		want  string
		err   bool
	}{
		{"zero (stop)", 0, "f7 a2 01 00 a3 fd", false},
		{"1.5 km/h", 1.5, "f7 a2 01 0f b2 fd", false},
		{"max 6.0 km/h", 6.0, "f7 a2 01 3c df fd", false},
		{"negative", -0.1, "", true},
		{"over max", 6.1, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeSetSpeed(tc.speed)
			if tc.err {
				if err == nil {
					t.Fatalf("expected error for speed %v", tc.speed)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := mustHex(t, tc.want)
			if !bytesEqual(got, want) {
				t.Fatalf("encoded %x, want %x", got, want)
			}
		})
	}
}

func TestEncodeSetMode(t *testing.T) {
	cases := []struct {
		name string
		mode Mode
		want string
		err  bool
	}{
		{"auto", ModeAuto, "f7 a2 02 00 a4 fd", false},
		{"manual", ModeManual, "f7 a2 02 01 a5 fd", false},
		{"standby", ModeStandby, "f7 a2 02 02 a6 fd", false},
		{"invalid", Mode(7), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeSetMode(tc.mode)
			if tc.err {
				if err == nil {
					t.Fatalf("expected error for mode %v", tc.mode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytesEqual(got, mustHex(t, tc.want)) {
				t.Fatalf("encoded %x, want %s", got, tc.want)
			}
		})
	}
}

func TestEncodeSetPref(t *testing.T) {
	cases := []struct {
		name  string
		key   PrefKey
		sub   byte
		value uint32
		// We don't pin exact bytes for every case — instead assert structural invariants.
		wantTypeByte byte
		wantKey      byte
		wantValBytes [3]byte
	}{
		{"max speed 6 km/h", PrefMaxSpeed, 0, 60, typePref, byte(PrefMaxSpeed), [3]byte{0, 0, 60}},
		{"start speed 1.5 km/h", PrefStartSpeed, 0, 15, typePref, byte(PrefStartSpeed), [3]byte{0, 0, 15}},
		{"child lock on", PrefChildLock, 0, 1, typePref, byte(PrefChildLock), [3]byte{0, 0, 1}},
		{"target time 30 min", PrefTarget, 3, 30, typePref, byte(PrefTarget), [3]byte{0, 0, 30}},
		{"target dist 24-bit max", PrefTarget, 1, 0x00FFFFFF, typePref, byte(PrefTarget), [3]byte{0xFF, 0xFF, 0xFF}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := EncodeSetPref(tc.key, tc.sub, tc.value)
			if len(f) != PrefFrameLen {
				t.Fatalf("len = %d, want %d", len(f), PrefFrameLen)
			}
			if f[0] != cmdStart || f[1] != tc.wantTypeByte || f[2] != tc.wantKey || f[3] != tc.sub {
				t.Fatalf("header bytes wrong: %x", f[:4])
			}
			if f[4] != tc.wantValBytes[0] || f[5] != tc.wantValBytes[1] || f[6] != tc.wantValBytes[2] {
				t.Fatalf("value bytes %02x %02x %02x, want %02x %02x %02x",
					f[4], f[5], f[6], tc.wantValBytes[0], tc.wantValBytes[1], tc.wantValBytes[2])
			}
			if f[7] != 0xAC {
				t.Fatalf("fixed byte = %02x, want ac", f[7])
			}
			if f[9] != frameEnd {
				t.Fatalf("terminator = %02x, want fd", f[9])
			}
			if got, w := f[8], crc(f); got != w {
				t.Fatalf("CRC %02x, want %02x", got, w)
			}
		})
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
