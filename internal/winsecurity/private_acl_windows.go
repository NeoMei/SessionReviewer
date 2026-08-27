//go:build windows

package winsecurity

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProtectedDACLMatches reports whether descriptor has a protected DACL with
// exactly the same ACEs as expectedSDDL. Windows may render equivalent SIDs
// using aliases (for example LA) and may add descriptor control markers such
// as AI, so comparing rendered SDDL strings is not a semantic ACL check.
func ProtectedDACLMatches(descriptor *windows.SECURITY_DESCRIPTOR, expectedSDDL string) bool {
	if descriptor == nil {
		return false
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	expected, err := windows.SecurityDescriptorFromString(expectedSDDL)
	if err != nil {
		return false
	}
	gotACL, _, err := descriptor.DACL()
	if err != nil || gotACL == nil {
		return false
	}
	wantACL, _, err := expected.DACL()
	if err != nil || wantACL == nil || gotACL.AceCount != wantACL.AceCount {
		return false
	}
	matched := make([]bool, gotACL.AceCount)
	for wantIndex := uint32(0); wantIndex < uint32(wantACL.AceCount); wantIndex++ {
		wantACE, err := allowedACE(wantACL, wantIndex)
		if err != nil {
			return false
		}
		found := false
		for gotIndex := uint32(0); gotIndex < uint32(gotACL.AceCount); gotIndex++ {
			if matched[gotIndex] {
				continue
			}
			gotACE, err := allowedACE(gotACL, gotIndex)
			if err != nil {
				return false
			}
			if gotACE.Header.AceFlags == wantACE.Header.AceFlags && gotACE.Mask == wantACE.Mask && aceSID(gotACE).Equals(aceSID(wantACE)) {
				matched[gotIndex] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func allowedACE(acl *windows.ACL, index uint32) (*windows.ACCESS_ALLOWED_ACE, error) {
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, index, &ace); err != nil {
		return nil, err
	}
	if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		return nil, windows.ERROR_INVALID_ACL
	}
	return ace, nil
}

func aceSID(ace *windows.ACCESS_ALLOWED_ACE) *windows.SID {
	return (*windows.SID)(unsafe.Pointer(&ace.SidStart))
}
