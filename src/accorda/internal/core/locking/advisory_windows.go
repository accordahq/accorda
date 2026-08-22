//go:build windows

package locking

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
	errorLockViolation      = syscall.Errno(33)
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx       = kernel32.NewProc("LockFileEx")
	unlockFileEx     = kernel32.NewProc("UnlockFileEx")
	lockFileExFlags  = uintptr(lockfileFailImmediately | lockfileExclusiveLock)
	lockByteCountLow = uintptr(1)
)

func tryAdvisoryLock(file *os.File) (bool, error) {
	var overlapped syscall.Overlapped
	result, _, callErr := lockFileEx.Call(
		file.Fd(), lockFileExFlags, 0, lockByteCountLow, 0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result != 0 {
		return true, nil
	}
	if errors.Is(callErr, errorLockViolation) {
		return false, nil
	}
	return false, callErr
}

func releaseAdvisoryLock(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := unlockFileEx.Call(
		file.Fd(), 0, lockByteCountLow, 0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result == 0 {
		return callErr
	}
	return nil
}
