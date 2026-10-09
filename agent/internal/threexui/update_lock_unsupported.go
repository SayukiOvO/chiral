//go:build !linux && !darwin

package threexui

import (
	"errors"
	"os"
)

func lockUpdateFile(string) (*os.File, bool, error) {
	return nil, false, errors.New("durable 3x-ui update locking is unsupported on this platform")
}
