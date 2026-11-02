// Package securefile reads bounded local inputs without following final symlinks.
package securefile

import (
	"errors"
	"io"
	"os"
)

var ErrRead = errors.New("local certificate or secret file is unreadable, unsafe, or oversized")

func Read(path string, secret bool, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || (secret && info.Mode().Perm()&0077 != 0) {
		return nil, ErrRead
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrRead
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return nil, ErrRead
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(b) == 0 || int64(len(b)) > limit {
		return nil, ErrRead
	}
	return b, nil
}
