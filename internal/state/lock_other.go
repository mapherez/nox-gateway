//go:build !darwin && !linux

package state

import (
	"errors"
	"os"
)

func lockFile(*os.File) error {
	return errors.New("persistent state currently requires Linux or macOS")
}
