//go:build windows

package cli

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"
)

func createBootstrapFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ, &sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_WRITE_THROUGH, 0)
	// Apply the protected DACL at creation, not through a path-based reopen.
	runtime.KeepAlive(sd)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// CREATE_NEW + WRITE_THROUGH and file Sync above flush the file through the
// Windows cache. Hardware power-loss guarantees remain the filesystem's policy.
func syncBootstrapParent(string) error { return nil }
