//go:build linux

package omega

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func supportedPlatform() error { return nil }
func owned(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("omega: authority material must belong to the current user")
	}
	return nil
}
func safeAncestor(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) || (info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
		return errors.New("omega: authority ancestors must be directories owned by this user or root and protected from replacement by other users")
	}
	return nil
}
func tryLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

// RENAME_NOREPLACE is the visibility commit: there is never a partially
// populated final directory, and an existing authority can never be replaced.
func (s *store) promote(staging, final string) error {
	f, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Renameat2(int(f.Fd()), staging, int(f.Fd()), final, unix.RENAME_NOREPLACE)
}
