//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package connector

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockSchedulingFenceFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX)
}

func unlockSchedulingFenceFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
