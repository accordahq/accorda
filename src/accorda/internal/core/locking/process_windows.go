//go:build windows

package locking

import (
	"errors"
	"syscall"
)

const stillActive = 259

func processAlive(pid int) bool {
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		// Access denied proves a process owns the PID even though its state
		// cannot be queried by this user.
		return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
	}
	defer syscall.CloseHandle(handle)
	var exitCode uint32
	if err := syscall.GetExitCodeProcess(handle, &exitCode); err != nil {
		return true
	}
	return exitCode == stillActive
}
