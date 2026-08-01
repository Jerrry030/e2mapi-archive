package store

import (
	"fmt"
	"time"
)

func autoSwitchLeaseDuration(leaseUntil *time.Time, callerNow time.Time) time.Duration {
	if leaseUntil == nil || callerNow.IsZero() {
		return 0
	}
	return leaseUntil.Sub(callerNow)
}

func autoSwitchLeaseMicros(duration time.Duration) (int64, error) {
	if duration <= 0 {
		return 0, fmt.Errorf("store: auto-switch lease duration must be positive: %s", duration)
	}
	micros := duration.Microseconds()
	if micros == 0 {
		micros = 1
	}
	return micros, nil
}
