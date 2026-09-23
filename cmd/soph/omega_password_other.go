//go:build !linux

package main

import (
	"context"
	"errors"
)

func readOmegaPassphrase(context.Context, bool) ([]byte, error) {
	return nil, errors.New("encrypted omega operator custody currently requires Linux")
}
