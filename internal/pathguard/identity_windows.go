package pathguard

import (
	"os"
	"strconv"
	"syscall"
)

func physicalIdentityFromFile(file *os.File) (IdentityToken, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return IdentityToken{}, err
	}
	fileID := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	return IdentityToken{
		Kind:   "windows-volume-file-id",
		Volume: strconv.FormatUint(uint64(info.VolumeSerialNumber), 10),
		File:   strconv.FormatUint(fileID, 10),
	}, nil
}
