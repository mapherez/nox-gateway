//go:build unix

package transport

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func Listen(dir, id string) (net.Listener, func(), error) {
	path, err := Socket(dir, Origin(id))
	if err != nil {
		return nil, nil, err
	}
	fd, err := unix.Open(path+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, nil, err
	}
	lock := os.NewFile(uintptr(fd), path+".lock")
	release := func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = lock.Close() }
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, nil, fmt.Errorf("Suite listener already owned")
	}
	if st, e := os.Lstat(path); e == nil {
		if st.Mode()&os.ModeSocket == 0 {
			release()
			return nil, nil, ErrTarget
		}
		c, e := net.DialTimeout("unix", path, 100000000)
		if e == nil {
			c.Close()
			release()
			return nil, nil, ErrTarget
		}
		if !errors.Is(e, unix.ECONNREFUSED) && !errors.Is(e, os.ErrNotExist) {
			release()
			return nil, nil, ErrTarget
		}
		if err = os.Remove(path); err != nil {
			release()
			return nil, nil, err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		release()
		return nil, nil, e
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		release()
		return nil, nil, err
	}
	l.SetUnlinkOnClose(false)
	own, err := os.Lstat(path)
	cleanup := func() {
		l.Close()
		if st, e := os.Lstat(path); e == nil && own != nil && os.SameFile(st, own) {
			os.Remove(path)
		}
		release()
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if err = os.Chmod(path, 0660); err != nil {
		cleanup()
		return nil, nil, err
	}
	return l, cleanup, nil
}
