package source

import (
	"errors"
	"fmt"
)

// LocalErrorCode identifies an expected failure confined to one authenticated
// source. Integrity, identity, catalog, visitor, and adapter-contract failures
// must never use these codes.
type LocalErrorCode string

const (
	LocalUnavailable LocalErrorCode = "source_unavailable"
	LocalChanged     LocalErrorCode = "source_changed"
	LocalDecode      LocalErrorCode = "source_decode"
)

// LocalError permits scan orchestration to terminate one source without
// weakening project-wide fail-closed behavior.
type LocalError struct {
	Code LocalErrorCode
	Err  error
}

func (e *LocalError) Error() string {
	if e == nil {
		return "source-local failure"
	}
	return fmt.Sprintf("source-local %s failure: %v", e.Code, e.Err)
}

func (e *LocalError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewLocalError(code LocalErrorCode, cause error) error {
	if cause == nil {
		return errors.New("source-local failure requires a cause")
	}
	switch code {
	case LocalUnavailable, LocalChanged, LocalDecode:
		return &LocalError{Code: code, Err: cause}
	default:
		return fmt.Errorf("invalid source-local error code %q", code)
	}
}
