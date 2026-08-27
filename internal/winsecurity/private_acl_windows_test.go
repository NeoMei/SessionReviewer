//go:build windows

package winsecurity

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestProtectedDACLMatchesEquivalentControlFlagsAndRejectsBroadening(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	userSID := user.User.Sid.String()
	want := "D:P(A;;FA;;;" + userSID + ")(A;;FA;;;SY)"

	equivalent, err := windows.SecurityDescriptorFromString("D:PAI(A;;FA;;;SY)(A;;FA;;;" + userSID + ")")
	if err != nil {
		t.Fatal(err)
	}
	if !ProtectedDACLMatches(equivalent, want) {
		t.Fatal("equivalent protected DACL with reordered ACEs and AI control was rejected")
	}

	broadened, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + userSID + ")(A;;FA;;;SY)(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	if ProtectedDACLMatches(broadened, want) {
		t.Fatal("DACL granting Everyone read access was accepted")
	}

	unprotected, err := windows.SecurityDescriptorFromString("D:(A;;FA;;;" + userSID + ")(A;;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	if ProtectedDACLMatches(unprotected, want) {
		t.Fatal("unprotected DACL was accepted")
	}
}
