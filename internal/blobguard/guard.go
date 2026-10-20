// Package blobguard serializes the two-node attachment runtime and offline
// maintenance against one real blob root. OS locks release on process death.
package blobguard

import (
	"os"
	"path/filepath"
)

func Acquire(root string) (func(), error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(filepath.Dir(real), "."+filepath.Base(real)+".federation.lock")
	return acquire(path)
}
