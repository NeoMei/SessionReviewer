package pathguard

import (
	"errors"
	"strconv"
)

// IdentityToken is a restart-stable physical directory identity. Volume and
// File are decimal strings so JSON round trips cannot lose 64-bit precision.
type IdentityToken struct {
	Kind   string `json:"kind"`
	Volume string `json:"volume"`
	File   string `json:"file"`
}

func (token IdentityToken) Valid() bool {
	if token.Kind != "posix-dev-inode" && token.Kind != "windows-volume-file-id" {
		return false
	}
	volume, volumeErr := strconv.ParseUint(token.Volume, 10, 64)
	file, fileErr := strconv.ParseUint(token.File, 10, 64)
	return volumeErr == nil && fileErr == nil && strconv.FormatUint(volume, 10) == token.Volume && strconv.FormatUint(file, 10) == token.File
}

func (directory *Directory) PhysicalIdentity() (IdentityToken, error) {
	if directory == nil || directory.Root == nil {
		return IdentityToken{}, errors.New("directory root is required")
	}
	file, err := directory.Root.Open(".")
	if err != nil {
		return IdentityToken{}, err
	}
	defer file.Close()
	return physicalIdentityFromFile(file)
}
