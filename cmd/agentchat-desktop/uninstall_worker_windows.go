//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type uninstallMarker struct {
	App           string   `json:"app"`
	Root          string   `json:"root"`
	VolumeSerial  uint32   `json:"volume_serial"`
	FileIndexHigh uint32   `json:"file_index_high"`
	FileIndexLow  uint32   `json:"file_index_low"`
	Files         []string `json:"files"`
	ConfigCreated bool     `json:"config_created"`
}

type fileDispositionInfo struct {
	DeleteFile uint8
}

type pinnedFile struct {
	path      string
	handle    windows.Handle
	canDelete bool
	volume    uint32
	indexHigh uint32
	indexLow  uint32
}

func pinFile(path string) (pinnedFile, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return pinnedFile{}, err
	}
	// Keep the exact file object pinned while the parent exits. A handle that
	// denies FILE_SHARE_DELETE prevents a same-path replacement until removal.
	h, err := windows.CreateFile(ptr, windows.GENERIC_READ|windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	canDelete := err == nil
	if err != nil {
		h, err = windows.CreateFile(ptr, windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			return pinnedFile{}, err
		}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return pinnedFile{}, err
	}
	return pinnedFile{
		path: path, handle: h, canDelete: canDelete,
		volume: info.VolumeSerialNumber, indexHigh: info.FileIndexHigh, indexLow: info.FileIndexLow,
	}, nil
}

func (p pinnedFile) delete() error {
	if p.handle != 0 {
		defer windows.CloseHandle(p.handle)
	}
	if p.canDelete {
		info := fileDispositionInfo{DeleteFile: 1}
		r, _, err := procSetFileInformationByHandle.Call(
			uintptr(p.handle), 4, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
		if r == 0 {
			return err
		}
		return nil
	}
	// The running executable cannot be opened for DELETE while the parent is
	// alive. It was pinned without FILE_SHARE_DELETE; reopen and verify FileId
	// before deleting the exact object.
	ptr, err := windows.UTF16PtrFromString(p.path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(ptr, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.VolumeSerialNumber != p.volume || info.FileIndexHigh != p.indexHigh || info.FileIndexLow != p.indexLow {
		return errors.New("file identity changed before deletion")
	}
	disp := fileDispositionInfo{DeleteFile: 1}
	r, _, callErr := procSetFileInformationByHandle.Call(
		uintptr(h), 4, uintptr(unsafe.Pointer(&disp)), unsafe.Sizeof(disp))
	if r == 0 {
		return callErr
	}
	return nil
}

var (
	kernel32DLL                    = windows.NewLazySystemDLL("kernel32.dll")
	procSetFileInformationByHandle = kernel32DLL.NewProc("SetFileInformationByHandle")
	procMoveFileExW                = kernel32DLL.NewProc("MoveFileExW")
)

func openPinnedDirectory(path string) (windows.Handle, windows.ByHandleFileInformation, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, windows.ByHandleFileInformation{}, err
	}
	h, err := windows.CreateFile(ptr, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, windows.ByHandleFileInformation{}, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return 0, info, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(h)
		return 0, info, errors.New("install path is a reparse point")
	}
	return h, info, nil
}

func managedUninstallFiles(marker uninstallMarker, root string, removeData bool) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(marker.Files)+3)
	add := func(name string) {
		name = filepath.Base(strings.TrimSpace(name))
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, filepath.Join(root, name))
	}
	for _, name := range marker.Files {
		add(name)
	}
	if removeData {
		add("desktop.json")
		add("agentchat.json")
		add(".agentchat-install.json")
	}
	return out
}

func waitForParent(pid int) {
	if pid <= 0 {
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	done := make(chan struct{})
	go func() { _, _ = p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = p.Kill()
		<-done
	}
}

func runUninstallWorker(args []string) int {
	if len(args) < 3 {
		return 2
	}
	rootArg := args[0]
	pid, err := strconv.Atoi(args[1])
	if err != nil {
		return 2
	}
	removeData := strings.EqualFold(args[2], "true")
	root, err := filepath.Abs(rootArg)
	if err != nil {
		return 2
	}
	rootHandle, rootInfo, err := openPinnedDirectory(root)
	if err != nil {
		return 2
	}
	defer windows.CloseHandle(rootHandle)
	parentHandle, _, err := openPinnedDirectory(filepath.Dir(root))
	if err != nil {
		return 2
	}
	defer windows.CloseHandle(parentHandle)
	markerData, err := os.ReadFile(filepath.Join(root, ".agentchat-install.json"))
	if err != nil {
		return 2
	}
	var marker uninstallMarker
	if json.Unmarshal(markerData, &marker) != nil || marker.App != "agentchat-desktop" ||
		!strings.EqualFold(filepath.Clean(marker.Root), filepath.Clean(root)) ||
		marker.VolumeSerial != rootInfo.VolumeSerialNumber ||
		marker.FileIndexHigh != rootInfo.FileIndexHigh ||
		marker.FileIndexLow != rootInfo.FileIndexLow {
		return 2
	}
	files := managedUninstallFiles(marker, root, removeData)
	if len(files) == 0 {
		return 2
	}
	pinned := make([]pinnedFile, 0, len(files))
	for _, path := range files {
		p, err := pinFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			// A locked file is still pinned read-only by pinFile; other failures
			// leave the unknown object untouched.
			continue
		}
		pinned = append(pinned, p)
	}
	waitForParent(pid)
	hadErr := false
	for _, p := range pinned {
		if err := p.delete(); err != nil {
			hadErr = true
		}
	}
	if hadErr {
		return 1
	}
	// The worker is a temporary copy; schedule its own removal after reboot.
	if self, err := os.Executable(); err == nil {
		if p, err := windows.UTF16PtrFromString(self); err == nil {
			_, _, _ = procMoveFileExW.Call(uintptr(unsafe.Pointer(p)), 0, 4)
		}
	}
	return 0
}

func startUninstallWorker(root string, removeData bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	src, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp("", "AgentChatUninstall-*.exe")
	if err != nil {
		return err
	}
	path := tmp.Name()
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		os.Remove(path)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(path)
		return err
	}
	cmd := exec.Command(path, "--uninstall-worker", root, fmt.Sprint(os.Getpid()), fmt.Sprint(removeData))
	if err := cmd.Start(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}
