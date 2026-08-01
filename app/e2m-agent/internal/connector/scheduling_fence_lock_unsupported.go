//go:build aix || (!darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !solaris && !windows)

package connector

import (
	"errors"
	"os"
)

func lockSchedulingFenceFile(*os.File) error {
	return errors.New("durable scheduling fence locking is not supported on this platform")
}

func unlockSchedulingFenceFile(*os.File) error { return nil }
