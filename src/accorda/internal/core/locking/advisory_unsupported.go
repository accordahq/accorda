//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package locking

import (
	"errors"
	"os"
)

func tryAdvisoryLock(*os.File) (bool, error) {
	return false, errors.New("advisory file locks are unsupported on this platform")
}

func releaseAdvisoryLock(*os.File) error {
	return nil
}
