package source

import (
	"errors"
	"testing"
)

func TestLocalErrorCarriesOnlyRecognizedSourceLocalCodes(t *testing.T) {
	cause := errors.New("source disappeared")
	for _, code := range []LocalErrorCode{LocalUnavailable, LocalChanged, LocalDecode} {
		err := NewLocalError(code, cause)
		var local *LocalError
		if !errors.As(err, &local) || local.Code != code || !errors.Is(err, cause) {
			t.Fatalf("local error code=%q did not preserve typed cause: %#v", code, err)
		}
	}
	for _, code := range []LocalErrorCode{"", "catalog_failure", "path_identity"} {
		if err := NewLocalError(code, cause); err == nil {
			t.Fatalf("accepted unrecognized local error code %q", code)
		}
	}
}

func TestLocalErrorDoesNotClassifyContextOrUnknownFailures(t *testing.T) {
	for _, err := range []error{errors.New("adapter contract"), nil} {
		var local *LocalError
		if errors.As(err, &local) {
			t.Fatalf("unknown error classified source-local: %#v", err)
		}
	}
}
