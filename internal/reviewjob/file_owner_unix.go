//go:build !windows

package reviewjob

import (
	"os"
	"syscall"
)

func sameFileOwner(parent, file os.FileInfo) bool {
	parentStat, parentOK := parent.Sys().(*syscall.Stat_t)
	fileStat, fileOK := file.Sys().(*syscall.Stat_t)
	return parentOK && fileOK && parentStat.Uid == fileStat.Uid
}
