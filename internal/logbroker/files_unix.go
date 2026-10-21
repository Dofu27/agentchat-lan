//go:build !windows

package logbroker

import (
	"golang.org/x/sys/unix"
	"os"
	"time"
)

func acquire(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(time.Second)
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }, nil
		}
		if (err != unix.EWOULDBLOCK && err != unix.EAGAIN) || time.Now().After(deadline) {
			f.Close()
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func openRead(path string) (*os.File, error) { return os.Open(path) }
func PreparePermissions(dir, userSID string) error {
	if userSID != "" {
		return ErrOptions
	}
	paths, err := permissionPaths(dir)
	if err != nil {
		return err
	}
	for i, path := range paths {
		mode := os.FileMode(0600)
		if i == 0 {
			mode = 0700
		}
		if err = os.Chmod(path, mode); err != nil {
			return err
		}
	}
	return nil
}
