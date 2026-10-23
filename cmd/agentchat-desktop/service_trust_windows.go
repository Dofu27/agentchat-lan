//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var securityDLL = windows.NewLazySystemDLL("advapi32.dll")
var accessCheckProc = securityDLL.NewProc("AccessCheck")
var restrictTokenProc = securityDLL.NewProc("CreateRestrictedToken")

type serviceFileIdentity struct{ volume, high, low uint32 }
type servicePathObject struct {
	handle windows.Handle
	path   string
	id     serviceFileIdentity
}
type serviceBinaryGuard struct{ objects []servicePathObject }

// SCM still takes a path, but its executable is derived exclusively from the
// verified, pinned final handle, never from a second configuration read.
func (g *serviceBinaryGuard) ExecutablePath() (string, error) {
	if g == nil || len(g.objects) == 0 {
		return "", errors.New("service executable guard is unavailable")
	}
	if err := g.Verify(); err != nil {
		return "", err
	}
	return finalHandlePath(g.objects[len(g.objects)-1].handle)
}

// Bind the SCM command's parsed executable to the pinned executable by FileId,
// not by a configuration string or by the protected-directory name alone.
func (g *serviceBinaryGuard) VerifyExecutable(path string) error {
	canonical, err := g.ExecutablePath()
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	id, attr, err := fileIdentity(h)
	if err != nil {
		return err
	}
	if id != g.objects[len(g.objects)-1].id || attr&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return errors.New("SCM executable differs from pinned file identity")
	}
	final, err := finalHandlePath(h)
	if err != nil {
		return err
	}
	if !strings.EqualFold(final, canonical) {
		return errors.New("SCM executable differs from pinned final path")
	}
	return nil
}

func (g *serviceBinaryGuard) Close() {
	for i := len(g.objects) - 1; i >= 0; i-- {
		windows.CloseHandle(g.objects[i].handle)
	}
	g.objects = nil
}

func fileIdentity(h windows.Handle) (serviceFileIdentity, uint32, error) {
	var info windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(h, &info)
	return serviceFileIdentity{info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow}, info.FileAttributes, err
}

func finalHandlePath(h windows.Handle) (string, error) {
	buf := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", err
	}
	if n >= uint32(len(buf)) {
		return "", errors.New("service path too long")
	}
	return filepath.Clean(strings.TrimPrefix(windows.UTF16ToString(buf[:n]), `\\?\`)), nil
}

func (g *serviceBinaryGuard) Verify() error {
	for _, obj := range g.objects {
		id, attr, err := fileIdentity(obj.handle)
		if err != nil {
			return err
		}
		if id != obj.id || attr&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return errors.New("service path identity changed")
		}
		final, err := finalHandlePath(obj.handle)
		if err != nil {
			return err
		}
		if !strings.EqualFold(final, obj.path) {
			return errors.New("service path resolves to a different object")
		}
	}
	return nil
}

// Build an impersonation token with admin groups disabled and privileges removed,
// even when the console itself is elevated. Never substitute an admin token on error.
func ordinaryUserToken() (windows.Token, error) {
	var current windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &current); err != nil {
		return 0, err
	}
	defer current.Close()
	var restricted windows.Token
	r, _, err := restrictTokenProc.Call(uintptr(current), 0x1|0x4, 0, 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted))) // DISABLE_MAX_PRIVILEGE | LUA_TOKEN
	if r == 0 {
		return 0, fmt.Errorf("create ordinary-user token: %w", err)
	}
	defer restricted.Close()
	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_QUERY, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		return 0, err
	}
	return impersonation, nil
}

type fileGenericMapping struct{ read, write, execute, all uint32 }

// AccessCheck computes effective rights (including deny ACEs and ownership),
// rather than inferring permissions from a directory name or a Unix mode bit.
func ordinaryUserCanMutate(sd *windows.SECURITY_DESCRIPTOR, token windows.Token) (bool, error) {
	absolute, err := sd.ToAbsolute()
	if err != nil {
		return false, err
	}
	mapping := fileGenericMapping{windows.FILE_GENERIC_READ, windows.FILE_GENERIC_WRITE, windows.FILE_GENERIC_EXECUTE, 0x1f01ff}
	privileges := make([]uint64, 512)
	size := uint32(len(privileges) * 8)
	var granted, allowed uint32
	r, _, callErr := accessCheckProc.Call(uintptr(unsafe.Pointer(absolute)), uintptr(token), uintptr(windows.MAXIMUM_ALLOWED), uintptr(unsafe.Pointer(&mapping)), uintptr(unsafe.Pointer(&privileges[0])), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&granted)), uintptr(unsafe.Pointer(&allowed)))
	runtime.KeepAlive(absolute)
	if r == 0 {
		return false, fmt.Errorf("AccessCheck service object: %w", callErr)
	}
	const mutation = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | 0x40 /* FILE_DELETE_CHILD */ | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER
	return allowed != 0 && granted&mutation != 0, nil
}

// Pin every object, from the volume root to the executable, without write/delete
// sharing. The returned guard must remain alive throughout SCM creation/start.
func openTrustedServiceBinary(path string) (_ *serviceBinaryGuard, retErr error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("service executable must be absolute")
	}
	abs := filepath.Clean(path)
	volume := filepath.VolumeName(abs)
	if len(volume) != 2 || volume[1] != ':' {
		return nil, errors.New("service executable must be on a local volume")
	}
	protected := false
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("WINDIR")} {
		if root != "" && strings.HasPrefix(strings.ToLower(abs), strings.ToLower(filepath.Clean(root))+`\`) {
			protected = true
		}
	}
	if !protected {
		return nil, errors.New("service executable must be in a protected machine directory")
	}
	g := &serviceBinaryGuard{}
	defer func() {
		if retErr != nil {
			g.Close()
		}
	}()
	paths := []string{volume + `\`}
	for _, part := range strings.Split(strings.TrimPrefix(abs, volume+`\`), `\`) {
		paths = append(paths, filepath.Join(paths[len(paths)-1], part))
	}
	for i, path := range paths {
		p, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return nil, err
		}
		h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			return nil, fmt.Errorf("pin service path: %w", err)
		}
		id, attr, err := fileIdentity(h)
		g.objects = append(g.objects, servicePathObject{h, path, id})
		if err != nil {
			return nil, err
		}
		if attr&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return nil, errors.New("service ancestor or executable is a reparse point")
		}
		isDir := attr&windows.FILE_ATTRIBUTE_DIRECTORY != 0
		if isDir != (i != len(paths)-1) {
			return nil, errors.New("service executable must be a regular file beneath directories")
		}
	}
	if err := g.Verify(); err != nil {
		return nil, err
	}
	token, err := ordinaryUserToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	for _, obj := range g.objects {
		sd, err := windows.GetSecurityInfo(obj.handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return nil, err
		}
		writable, err := ordinaryUserCanMutate(sd, token)
		if err != nil {
			return nil, err
		}
		if writable {
			return nil, errors.New("ordinary user can mutate service executable or ancestor")
		}
		if err := rejectForeignServiceWriters(sd); err != nil {
			return nil, err
		}
	}
	return g, nil
}

func trustedServiceBinary(path string) error {
	g, err := openTrustedServiceBinary(path)
	if err != nil {
		return err
	}
	defer g.Close()
	return nil
}

// ensureProtectedServiceBinary copies a per-user CLI into Program Files when
// the user asks to install the machine-wide Hub service. The copy itself
// requires administrator rights; the resulting path is validated by the same
// protected-directory/ACL checks used by the service installer.
func ensureProtectedServiceBinary(path string) (string, error) {
	if err := trustedServiceBinary(path); err == nil {
		return path, nil
	}
	programFiles := os.Getenv("ProgramFiles")
	if programFiles == "" {
		return "", errors.New("ProgramFiles is not configured")
	}
	targetDir := filepath.Join(programFiles, "AgentChat")
	target := filepath.Join(targetDir, "agentchat.exe")
	if strings.EqualFold(filepath.Clean(path), filepath.Clean(target)) {
		return "", errors.New("protected service binary is not trusted")
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", errors.New("run the Console as administrator to create the protected service directory")
	}
	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(targetDir, ".agentchat-*.exe")
	if err != nil {
		return "", errors.New("run the Console as administrator to install the protected service binary")
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return "", errors.New("run the Console as administrator to replace the protected service binary")
	}
	ok = true
	if err := trustedServiceBinary(target); err != nil {
		return "", err
	}
	return target, nil
}
