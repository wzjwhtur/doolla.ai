//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package memory

import (
	"context"
	"errors"
	"os"
)

func lockFile(context.Context, string) (*os.File, error) {
	return nil, errors.New("FAISS persistence requires a platform with file locking")
}
