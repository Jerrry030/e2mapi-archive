//go:build windows

package connector

import (
	"os"

	"golang.org/x/sys/windows"
)

const schedulingFenceLockBytes = ^uint32(0)

func lockSchedulingFenceFile(file *os.File) error {
	return windows.LockFileEx(
		windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0,
		schedulingFenceLockBytes, schedulingFenceLockBytes, new(windows.Overlapped),
	)
}

func unlockSchedulingFenceFile(file *os.File) error {
	return windows.UnlockFileEx(
		windows.Handle(file.Fd()), 0,
		schedulingFenceLockBytes, schedulingFenceLockBytes, new(windows.Overlapped),
	)
}
