//go:build windows

package cli

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsDetachedReviewSysProcAttrUsesExplicitHandleList(t *testing.T) {
	policy, err := detachedReviewInheritancePolicy(uintptr(17))
	if err != nil {
		t.Fatal(err)
	}
	attributes := windowsDetachedReviewSysProcAttr(policy)
	if attributes == nil || attributes.NoInheritHandles ||
		attributes.CreationFlags != windows.CREATE_NEW_PROCESS_GROUP|windows.DETACHED_PROCESS ||
		len(attributes.AdditionalInheritedHandles) != 1 || uintptr(attributes.AdditionalInheritedHandles[0]) != 17 {
		t.Fatalf("Windows detached attributes=%#v", attributes)
	}
}
