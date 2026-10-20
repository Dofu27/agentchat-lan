package cli

import (
	"os"
	"path/filepath"
	"strings"
)

// safeUnderRoot resolves rel under root and refuses symlink/junction escapes.
// A missing leaf is safe: the caller may remove the database row even if the
// blob is already gone.
func safeUnderRoot(root, rel string) (string, bool) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	full := filepath.Join(rootReal, clean)
	fullReal, err := filepath.EvalSymlinks(full)
	if err != nil {
		if os.IsNotExist(err) {
			return full, true
		}
		return "", false
	}
	relReal, err := filepath.Rel(rootReal, fullReal)
	if err != nil || relReal == ".." || strings.HasPrefix(relReal, ".."+string(filepath.Separator)) || filepath.IsAbs(relReal) {
		return "", false
	}
	info, err := os.Lstat(fullReal)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return fullReal, true
}
