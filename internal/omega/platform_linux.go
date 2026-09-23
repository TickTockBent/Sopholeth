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
	if !ok || (os.Geteuid() != 0 && stat.Uid != uint32(os.Geteuid())) {
		return errors.New("omega: authority material must belong to the current user")
	}
	return nil
}
func safeAncestor(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || (os.Geteuid() != 0 && stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) || (info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
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

// Privileged operator commands may maintain a separate renewal account's home.
// New objects belong to that home, so the unprivileged scheduler can use them.
func inheritOwnership(file *os.File, parent os.FileInfo) error {
	if os.Geteuid() != 0 {
		return nil
	}
	stat, ok := parent.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("omega: cannot determine custody owner")
	}
	return file.Chown(int(stat.Uid), int(stat.Gid))
}
