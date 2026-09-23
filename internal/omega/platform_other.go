//go:build !linux

package omega

import (
	"errors"
	"os"
)

func supportedPlatform() error {
	return errors.New("omega: operator key storage currently requires Linux; public-client Windows support is tracked separately")
}
func owned(os.FileInfo) error                 { return supportedPlatform() }
func safeAncestor(os.FileInfo) error          { return supportedPlatform() }
func sameOwner(os.FileInfo, os.FileInfo) bool { return false }
func tryLock(*os.File) (bool, error)          { return false, supportedPlatform() }

func (s *store) promote(string, string) error { return supportedPlatform() }

func inheritOwnership(*os.File, os.FileInfo) error { return supportedPlatform() }
