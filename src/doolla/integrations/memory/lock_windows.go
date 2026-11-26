//go:build windows

package memory

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// A zero sharing mode makes the open handle exclusive across processes.
// Closing the handle (including after a process exits) releases the lock.
func lockFile(ctx context.Context, path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			return os.NewFile(uintptr(h), path), nil
		}
		if !errors.Is(err, syscall.Errno(32)) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(15 * time.Millisecond):
		}
	}
}
