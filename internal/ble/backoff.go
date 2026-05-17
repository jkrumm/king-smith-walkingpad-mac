package ble

import "time"

// BackoffMax is the ceiling for reconnect attempt waits.
const BackoffMax = 30 * time.Second

// Backoff returns the wait before the Nth reconnect attempt. Schedule:
//
//	attempt 1 → 1 s
//	attempt 2 → 2 s
//	attempt 3 → 4 s
//	attempt 4 → 8 s
//	attempt 5 → 16 s
//	attempt ≥ 6 → 30 s
//
// Modelled on mcdax's bleak_retry_connector (briefing template). attempt
// values < 1 return 0; > 30 returns the cap.
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		return 0
	}
	// shift overflows past attempt 62; clamp via the BackoffMax check below.
	if attempt > 30 {
		return BackoffMax
	}
	d := time.Duration(1<<(attempt-1)) * time.Second
	if d > BackoffMax || d <= 0 {
		return BackoffMax
	}
	return d
}
