//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// The controlling terminal keeps passphrases out of stdin pipelines and JSON
// output. Polling lets cancellation restore echo before the command exits.
func readOmegaPassphrase(ctx context.Context, confirm bool) ([]byte, error) {
	fd, err := unix.Open("/dev/tty", unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("encrypted omega custody needs an interactive terminal; passwords are not accepted through flags, environment variables, or stdin")
	}
	file := os.NewFile(uintptr(fd), "/dev/tty")
	defer file.Close()
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, errors.New("cannot protect the omega passphrase prompt")
	}
	defer term.Restore(fd, state)
	terminal := term.NewTerminal(&passwordTerminal{ctx: ctx, fd: fd, Writer: file}, "")
	password, err := terminal.ReadPassword("Omega key passphrase: ")
	if err != nil {
		return nil, err
	}
	if password == "" {
		return nil, errors.New("omega key passphrase cannot be empty")
	}
	if confirm {
		again, err := terminal.ReadPassword("Confirm passphrase: ")
		if err != nil {
			return nil, err
		}
		if password != again {
			return nil, errors.New("omega key passphrases do not match")
		}
	}
	return []byte(password), nil
}

type passwordTerminal struct {
	ctx context.Context
	fd  int
	io.Writer
}

func (t *passwordTerminal) Read(p []byte) (int, error) {
	for {
		if err := t.ctx.Err(); err != nil {
			return 0, err
		}
		fds := []unix.PollFd{{Fd: int32(t.fd), Events: unix.POLLIN}}
		ready, err := unix.Poll(fds, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if ready == 0 {
			continue
		}
		n, err := unix.Read(t.fd, p)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n > 0 && bytes.IndexByte(p[:n], 3) >= 0 { // Ctrl-C in raw mode.
			clear(p[:n])
			return 0, context.Canceled
		}
		if n == 0 && err == nil {
			return 0, io.EOF
		}
		return n, err
	}
}
