package session

import (
	"regexp"
	"testing"
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUIDv4_Format(t *testing.T) {
	for i := 0; i < 100; i++ {
		u := newUUIDv4()
		if !uuidRE.MatchString(u) {
			t.Fatalf("malformed v4 UUID: %q", u)
		}
	}
}

func TestNewUUIDv4_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		u := newUUIDv4()
		if seen[u] {
			t.Fatalf("collision after %d iterations: %q", i, u)
		}
		seen[u] = true
	}
}
