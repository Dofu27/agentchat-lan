//go:build windows

package cli

import (
	"errors"
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
	sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;;FA;;;SY)(A;;FA;;;" + user.User.Sid.String() + ")")
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

// Validate the security descriptor of the opened object before reading any secret.
func openResumeBootstrapFile(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	var info windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(h, &info)
	if err == nil && info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		err = errors.New("resume credential must be a regular non-reparse file")
	}
	if err == nil {
		var sd *windows.SECURITY_DESCRIPTOR
		sd, err = windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err == nil {
			err = validateResumeCredentialSecurity(sd)
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func validateResumeCredentialSecurity(sd *windows.SECURITY_DESCRIPTOR) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	allowed := func(sid *windows.SID) bool {
		return sid != nil && (sid.Equals(user.User.Sid) || sid.String() == "S-1-5-18")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if !allowed(owner) {
		return errors.New("resume credential owner is not the initiating user or SYSTEM")
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("resume credential DACL must be protected")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil {
		return errors.New("resume credential requires a restrictive DACL")
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			return errors.New("resume credential contains inherited permissions")
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("resume credential contains unsupported permissions")
		}
		if !allowed((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
			return errors.New("resume credential grants access to another account")
		}
	}
	return nil
}
