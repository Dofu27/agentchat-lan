//go:build windows

package main

import (
	"errors"
	"golang.org/x/sys/windows"
	"unsafe"
)

func trustedMachineSID(sid *windows.SID) bool {
	if sid == nil {
		return false
	}
	switch sid.String() {
	case "S-1-5-18", "S-1-5-32-544", "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464":
		return true
	}
	return false
}

// AccessCheck for the current user cannot model another account's private SID.
// Refuse foreign write grants/owners rather than treating that as evidence of safety.
func rejectForeignServiceWriters(sd *windows.SECURITY_DESCRIPTOR) error {
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if !trustedMachineSID(owner) {
		return errors.New("service object owner is not a machine administrator")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil {
		return errors.New("service object has no restrictive DACL")
	}
	const mutations = windows.GENERIC_ALL | windows.GENERIC_WRITE | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | 0x40 | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("unsupported service ACL entry")
		}
		if uint32(ace.Mask)&mutations == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if trustedMachineSID(sid) {
			continue
		}
		switch sid.String() {
		case "S-1-1-0", "S-1-5-11", "S-1-5-32-545":
			continue
		} // Checked with the ordinary-user token.
		return errors.New("service ACL grants mutation to an unrelated account")
	}
	return nil
}
