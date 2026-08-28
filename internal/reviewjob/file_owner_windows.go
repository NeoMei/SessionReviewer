//go:build windows

package reviewjob

import "os"

// The authenticated 0700-equivalent job root and 0600-equivalent regular
// leaf checks are the portable ownership boundary on Windows.
func sameFileOwner(_, _ os.FileInfo) bool { return true }
