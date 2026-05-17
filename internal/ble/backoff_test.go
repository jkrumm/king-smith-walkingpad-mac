package ble

import (
	"testing"
	"time"
)

func TestBackoff_Schedule(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 0},
		{-1, 0},
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 30 * time.Second},
		{20, 30 * time.Second},
		{100, 30 * time.Second}, // post-clamp guard
	}
	for _, tt := range tests {
		if got := Backoff(tt.attempt); got != tt.want {
			t.Errorf("Backoff(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}
