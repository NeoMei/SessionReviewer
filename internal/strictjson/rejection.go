package strictjson

import "errors"

// RejectionCode is the closed compatibility taxonomy for rejecting untrusted
// v4 wire input. Additions or value changes require a coordinated contract
// revision; human-readable error messages are deliberately not stable API.
type RejectionCode string

const (
	// CodeInputOverflow means the bounded input byte limit was exceeded.
	CodeInputOverflow RejectionCode = "wire_input_overflow"
	// CodeInvalidUTF8 means the input bytes are not valid UTF-8.
	CodeInvalidUTF8 RejectionCode = "wire_invalid_utf8"
	// CodeJSONInvalid covers malformed JSON, duplicate keys, and trailing data.
	CodeJSONInvalid RejectionCode = "wire_json_invalid"
	// CodeShapeInvalid covers exact-field, required-field, and JSON type failures.
	CodeShapeInvalid RejectionCode = "wire_shape_invalid"
	// CodeContractInvalid covers post-decode semantic and binding invariants.
	CodeContractInvalid RejectionCode = "wire_contract_invalid"
)

// RejectionError attaches a stable rejection code while retaining the
// original diagnostic as its error text and unwrap target.
type RejectionError struct {
	code  RejectionCode
	cause error
}

func (e *RejectionError) Error() string { return e.cause.Error() }
func (e *RejectionError) Unwrap() error { return e.cause }
func (e *RejectionError) Code() string  { return string(e.code) }

// NewRejection attaches code to cause. An existing rejection is preserved so
// callers wrapping a lower-level parser do not erase the most specific code.
func NewRejection(code RejectionCode, cause error) error {
	if cause == nil {
		return nil
	}
	var existing *RejectionError
	if errors.As(cause, &existing) {
		return cause
	}
	if !code.valid() {
		panic("strictjson: unknown rejection code")
	}
	return &RejectionError{code: code, cause: cause}
}

// CodeOf returns the stable wire rejection code carried by err, including
// through errors-compatible wrapping. It returns an empty string for errors
// outside the untrusted v4 wire boundary.
func CodeOf(err error) string {
	var rejection *RejectionError
	if errors.As(err, &rejection) {
		return rejection.Code()
	}
	return ""
}

func (code RejectionCode) valid() bool {
	switch code {
	case CodeInputOverflow, CodeInvalidUTF8, CodeJSONInvalid, CodeShapeInvalid, CodeContractInvalid:
		return true
	default:
		return false
	}
}
