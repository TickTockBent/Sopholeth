//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package bootstrap

import (
	"errors"
	"os"
)

func tryLock(*os.File) (bool, error) {
	return false, errors.New("bootstrap: durable trust state is not supported on this platform")
}

func checkOwner(os.FileInfo) error {
	return errors.New("bootstrap: durable trust state is not supported on this platform")
}

func checkAncestor(info os.FileInfo) error { return checkOwner(info) }
