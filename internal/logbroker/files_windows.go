package logbroker

import (
	"golang.org/x/sys/windows"
	"os"
	"time"
)

func acquire(path string) (func(), error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(time.Second)
	for {
		h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			return func() { windows.CloseHandle(h) }, nil
		}
		if err != windows.ERROR_SHARING_VIOLATION || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func openRead(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// PreparePermissions is installation-time only. The named user and LocalSystem
// can both write; arbitrary local users receive no access through this ACL.
func PreparePermissions(dir, userSID string) error {
	paths, err := permissionPaths(dir)
	if err != nil {
		return err
	}
	if userSID == "" {
		u, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			return err
		}
		userSID = u.User.Sid.String()
	}
	sid, err := windows.StringToSid(userSID)
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + sid.String() + ")")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
			return err
		}
	}
	return nil
}
