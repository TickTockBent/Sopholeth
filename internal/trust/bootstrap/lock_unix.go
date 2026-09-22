//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func tryLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func checkOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: state must belong to the current user", ErrState)
	}
	return nil
}

func checkAncestor(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) ||
		(info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
		return fmt.Errorf("%w: state ancestors must be owned by this user or root and protected from other users' replacement", ErrState)
	}
	return nil
}
