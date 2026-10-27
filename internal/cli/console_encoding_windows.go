//go:build windows

package cli

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32DLL             = windows.NewLazySystemDLL("kernel32.dll")
	procGetOEMCP            = kernel32DLL.NewProc("GetOEMCP")
	procMultiByteToWideChar = kernel32DLL.NewProc("MultiByteToWideChar")
)

// decodeConsoleOutput converts bytes captured from a Windows console tool
// (schtasks, sc, systemctl equivalents) using the active OEM code page.
func decodeConsoleOutput(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	cp, _, _ := procGetOEMCP.Call()
	if cp == 0 {
		cp = 65001
	}
	n, _, _ := procMultiByteToWideChar.Call(
		cp, 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		0, 0,
	)
	if n == 0 {
		return string(b)
	}
	buf := make([]uint16, n)
	procMultiByteToWideChar.Call(
		cp, 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(n),
	)
	return windows.UTF16ToString(buf)
}
