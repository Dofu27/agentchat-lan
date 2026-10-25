//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

var securityDLL = windows.NewLazySystemDLL("advapi32.dll")
var accessCheckProc = securityDLL.NewProc("AccessCheck")
var restrictTokenProc = securityDLL.NewProc("CreateRestrictedToken")

type serviceFileIdentity struct{ volume, high, low uint32 }
type servicePathObject struct {
	handle windows.Handle
	path   string
	id     serviceFileIdentity
	isDir  bool
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
func ordinaryUserCanMutate(sd *windows.SECURITY_DESCRIPTOR, token windows.Token, isDir bool) (bool, error) {
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
	mutation := uint32(windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | 0x40 /* FILE_DELETE_CHILD */ | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER)
	if isDir {
		// On a directory FILE_WRITE_DATA/FILE_APPEND_DATA mean FILE_ADD_FILE/
		// FILE_ADD_SUBDIRECTORY. Creating unrelated children does not let an
		// ordinary user replace an already pinned component; deletion, rename,
		// DACL, and owner changes still do. The data directory itself gets an
		// explicit protected DACL before the database is created.
		mutation &^= windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA
	}
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
		if err != nil {
			return nil, err
		}
		if attr&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return nil, errors.New("service ancestor or executable is a reparse point")
		}
		isDir := attr&windows.FILE_ATTRIBUTE_DIRECTORY != 0
		g.objects = append(g.objects, servicePathObject{h, path, id, isDir})
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
		writable, err := ordinaryUserCanMutate(sd, token, obj.isDir)
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

func openVerifiedFile(path string) (*os.File, string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, "", err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, "", err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		windows.CloseHandle(h)
		return nil, "", errors.New("service source must be a regular non-reparse file")
	}
	f := os.NewFile(uintptr(h), path)
	hsh := sha256.New()
	if _, err := io.Copy(hsh, f); err != nil {
		f.Close()
		return nil, "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, "", err
	}
	return f, hex.EncodeToString(hsh.Sum(nil)), nil
}

func openProtectedDirectory(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		windows.CloseHandle(h)
		return 0, errors.New("protected service directory is a reparse point or not a directory")
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	token, err := ordinaryUserToken()
	if err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	defer token.Close()
	writable, err := ordinaryUserCanMutate(sd, token, true)
	if err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	if writable {
		windows.CloseHandle(h)
		return 0, errors.New("ordinary user can mutate protected service directory")
	}
	if err := rejectForeignServiceWriters(sd); err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}

type pinnedDirectoryChain struct {
	handles []windows.Handle
}

func (p *pinnedDirectoryChain) Close() {
	for i := len(p.handles) - 1; i >= 0; i-- {
		windows.CloseHandle(p.handles[i])
	}
	p.handles = nil
}

func ordinaryUserCanReplacePath(sd *windows.SECURITY_DESCRIPTOR, token windows.Token) (bool, error) {
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
		return false, fmt.Errorf("AccessCheck data path: %w", callErr)
	}
	const replacement = 0x40 /* FILE_DELETE_CHILD */ | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER
	return allowed != 0 && granted&replacement != 0, nil
}

func openDataDirectory(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		windows.CloseHandle(h)
		return 0, errors.New("data ancestor is a reparse point or not a directory")
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	token, err := ordinaryUserToken()
	if err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	defer token.Close()
	replaceable, err := ordinaryUserCanReplacePath(sd, token)
	if err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	if replaceable {
		windows.CloseHandle(h)
		return 0, errors.New("ordinary user can replace a data ancestor")
	}
	if err := rejectForeignServiceWriters(sd); err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}

func pinReparseChain(path string) (*pinnedDirectoryChain, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)
	volume := filepath.VolumeName(abs)
	if len(volume) != 2 || volume[1] != ':' {
		return nil, errors.New("managed path must be on a local volume")
	}
	paths := []string{volume + `\`}
	current := paths[0]
	for _, part := range strings.Split(strings.TrimPrefix(abs, volume+`\`), `\`) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		paths = append(paths, current)
	}
	p := &pinnedDirectoryChain{}
	for _, dir := range paths {
		h, _, err := openPinnedDirectory(dir)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("pin managed directory %s: %w", dir, err)
		}
		p.handles = append(p.handles, h)
	}
	return p, nil
}

func pinDataDirectoryChain(path string) (*pinnedDirectoryChain, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)
	volume := filepath.VolumeName(abs)
	if len(volume) != 2 || volume[1] != ':' {
		return nil, errors.New("data directory must be on a local volume")
	}
	paths := []string{volume + `\`}
	current := paths[0]
	for _, part := range strings.Split(strings.TrimPrefix(abs, volume+`\`), `\`) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		paths = append(paths, current)
	}
	p := &pinnedDirectoryChain{}
	for _, dir := range paths {
		h, err := openDataDirectory(dir)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("pin data directory %s: %w", dir, err)
		}
		p.handles = append(p.handles, h)
	}
	return p, nil
}

func pinDirectoryChain(path string) (*pinnedDirectoryChain, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)
	volume := filepath.VolumeName(abs)
	if len(volume) != 2 || volume[1] != ':' {
		return nil, errors.New("protected service directory must be on a local volume")
	}
	paths := []string{volume + `\`}
	current := paths[0]
	for _, part := range strings.Split(strings.TrimPrefix(abs, volume+`\`), `\`) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		paths = append(paths, current)
	}
	p := &pinnedDirectoryChain{}
	for _, dir := range paths {
		h, err := openProtectedDirectory(dir)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("pin protected directory %s: %w", dir, err)
		}
		p.handles = append(p.handles, h)
	}
	return p, nil
}

var (
	knownFolderShell32 = windows.NewLazySystemDLL("shell32.dll")
	knownFolderOle32   = windows.NewLazySystemDLL("ole32.dll")
	procSHGetKnownFolderPath = knownFolderShell32.NewProc("SHGetKnownFolderPath")
	procCoTaskMemFree         = knownFolderOle32.NewProc("CoTaskMemFree")
	folderIDProgramFiles      = windows.GUID{
		Data1: 0x905e63b6, Data2: 0xc1bf, Data3: 0x494e,
		Data4: [8]byte{0xb2, 0x9c, 0x65, 0xb7, 0x32, 0xd3, 0xd2, 0x1a},
	}
)

func knownProgramFiles() (string, error) {
	var p *uint16
	r, _, _ := procSHGetKnownFolderPath.Call(uintptr(unsafe.Pointer(&folderIDProgramFiles)), 0, 0, uintptr(unsafe.Pointer(&p)))
	if r != 0 || p == nil {
		return "", errors.New("cannot resolve the Windows Program Files known folder")
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	chars := unsafe.Slice(p, 32768)
	n := 0
	for n < len(chars) && chars[n] != 0 {
		n++
	}
	if n == 0 || n == len(chars) {
		return "", errors.New("invalid Program Files known folder path")
	}
	return windows.UTF16ToString(chars[:n]), nil
}

func ensureServiceStoppedForReplace(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("cannot connect to service manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil
		}
		return fmt.Errorf("open service %s: %w", name, err)
	}
	defer s.Close()
	status, err := s.Query()
	if err != nil {
		return err
	}
	if status.State != svc.Stopped {
		return fmt.Errorf("service %s must be stopped before replacing its binary", name)
	}
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ensureProtectedServiceBinary installs the CLI from the exact build payload
// recorded in expectedCLISHA256. It pins the source without write/delete
// sharing, verifies the digest, and opens the target ancestors before writing
// so a junction cannot be used to overwrite an arbitrary directory.
func ensureProtectedServiceBinary(path, service string) (string, error) {
	expected := strings.ToLower(strings.TrimSpace(expectedCLISHA256))
	if len(expected) != 64 {
		return "", errors.New("service payload digest is not embedded in this Console build")
	}
	src, sourceHash, err := openVerifiedFile(path)
	if err != nil {
		return "", err
	}
	defer src.Close()
	if !strings.EqualFold(sourceHash, expected) {
		return "", errors.New("service binary does not match the verified release payload")
	}
	programFiles, err := knownProgramFiles()
	if err != nil {
		return "", err
	}
	pf, err := pinDirectoryChain(programFiles)
	if err != nil {
		return "", err
	}
	defer pf.Close()
	targetDir := filepath.Join(programFiles, "AgentChat")
	if _, err := os.Lstat(targetDir); err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		if err := os.Mkdir(targetDir, 0o755); err != nil {
			return "", fmt.Errorf("create protected service directory (run the Console as administrator): %w", err)
		}
	}
	td, err := pinDirectoryChain(targetDir)
	if err != nil {
		return "", err
	}
	defer td.Close()
	target := filepath.Join(targetDir, "agentchat.exe")
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("protected service binary path is not a regular file")
		}
		if hash, err := sha256File(target); err == nil && strings.EqualFold(hash, expected) {
			return target, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	backup := ""
	if _, err := os.Lstat(target); err == nil {
		service = strings.TrimSpace(service)
		if service == "" {
			service = "AgentChatHub"
		}
		names := []string{service}
		if !strings.EqualFold(service, "AgentChatHub") {
			names = append(names, "AgentChatHub")
		}
		for _, name := range names {
			if err := ensureServiceStoppedForReplace(name); err != nil {
				return "", err
			}
		}
		backup = target + ".old-" + fmt.Sprintf("%d", time.Now().UnixNano())
		if err := os.Rename(target, backup); err != nil {
			return "", fmt.Errorf("replace protected service binary (stop the service first): %w", err)
		}
	}
	rollback := func() error {
		var errs []error
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
		if backup != "" {
			if err := os.Rename(backup, target); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	failRollback := func(primary error) (string, error) {
		if rb := rollback(); rb != nil {
			return "", fmt.Errorf("%w; rollback failed, backup=%s: %v", primary, backup, rb)
		}
		return "", primary
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return failRollback(fmt.Errorf("create protected service binary (run the Console as administrator): %w", err))
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return failRollback(err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return failRollback(err)
	}
	if err := out.Close(); err != nil {
		return failRollback(err)
	}
	hash, err := sha256File(target)
	if err != nil || !strings.EqualFold(hash, expected) {
		return failRollback(errors.New("protected service binary failed payload verification"))
	}
	if err := trustedServiceBinary(target); err != nil {
		return failRollback(err)
	}
	if backup != "" {
		if err := os.Remove(backup); err != nil {
			return "", fmt.Errorf("new protected service binary installed but backup cleanup failed (%s): %w", backup, err)
		}
	}
	return target, nil
}
