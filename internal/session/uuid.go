package session

import (
	"crypto/rand"
	"fmt"
)

// newUUIDv4 returns an RFC 4122 v4 UUID. crypto/rand is the only dependency
// (no third-party UUID lib for a single allocator).
func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read on Darwin only fails on a syscall problem; treat as fatal.
		panic(fmt.Errorf("crypto/rand: %w", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xxxxxx
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
