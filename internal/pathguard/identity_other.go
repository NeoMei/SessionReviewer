//go:build !windows

package pathguard

import (
	"errors"
	"os"
	"reflect"
	"strconv"
)

func physicalIdentityFromFile(file *os.File) (IdentityToken, error) {
	info, err := file.Stat()
	if err != nil {
		return IdentityToken{}, err
	}
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !value.IsValid() {
		return IdentityToken{}, errors.New("physical file identity is unavailable")
	}
	device := value.FieldByName("Dev")
	inode := value.FieldByName("Ino")
	deviceValue, deviceOK := identityInteger(device)
	inodeValue, inodeOK := identityInteger(inode)
	if !deviceOK || !inodeOK {
		return IdentityToken{}, errors.New("physical file identity is unavailable")
	}
	return IdentityToken{
		Kind:   "posix-dev-inode",
		Volume: strconv.FormatUint(deviceValue, 10),
		File:   strconv.FormatUint(inodeValue, 10),
	}, nil
}

func identityInteger(value reflect.Value) (uint64, bool) {
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(value.Int()), true
	default:
		return 0, false
	}
}
