//go:build !windows

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func openResumeBootstrapFile(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Geteuid()) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			err = errors.New("resume credential must be private and owned by the initiating user")
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func createBootstrapFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}

func syncBootstrapParent(path string) error {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
