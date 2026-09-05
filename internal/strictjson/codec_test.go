package strictjson

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestDecodeReturnsStableRejectionCodes(t *testing.T) {
	type wire struct {
		Required string `json:"required" required:"true"`
	}
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{name: "bounded input overflow", body: bytes.Repeat([]byte{' '}, MaxBytes+1), want: "wire_input_overflow"},
		{name: "invalid UTF-8", body: []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}, want: "wire_invalid_utf8"},
		{name: "malformed JSON", body: []byte(`{"required":`), want: "wire_json_invalid"},
		{name: "duplicate key", body: []byte(`{"required":"first","required":"second"}`), want: "wire_json_invalid"},
		{name: "trailing JSON value", body: []byte(`{"required":"ok"} {"required":"extra"}`), want: "wire_json_invalid"},
		{name: "trailing garbage", body: []byte(`{"required":"ok"} garbage`), want: "wire_json_invalid"},
		{name: "unknown field", body: []byte(`{"required":"ok","unknown":true}`), want: "wire_shape_invalid"},
		{name: "missing field", body: []byte(`{}`), want: "wire_shape_invalid"},
		{name: "type mismatch", body: []byte(`{"required":1}`), want: "wire_shape_invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got wire
			err := Decode(tc.body, &got)
			if code := CodeOf(err); code != tc.want {
				t.Fatalf("rejection code = %q, want %q: %v", code, tc.want, err)
			}
			var rejection *RejectionError
			if !errors.As(err, &rejection) {
				t.Fatalf("error is not a typed rejection: %T %v", err, err)
			}
		})
	}
}

func TestRejectionErrorPreservesCause(t *testing.T) {
	cause := errors.New("semantic detail")
	err := NewRejection(CodeContractInvalid, cause)
	if got := CodeOf(err); got != "wire_contract_invalid" {
		t.Fatalf("rejection code = %q", got)
	}
	if err.Error() != cause.Error() {
		t.Fatalf("human message changed: %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("typed rejection does not unwrap to its cause")
	}
}

func TestDecodeRejectsDuplicateNestedKeys(t *testing.T) {
	var v map[string]any
	if err := Decode([]byte(`{"a":{"x":1,"x":2}}`), &v); err == nil {
		t.Fatal("accepted duplicate key")
	}
}

func TestDecodeRejectsTrailingAndUnknown(t *testing.T) {
	var v struct {
		A int `json:"a"`
	}
	if err := Decode([]byte(`{"a":1} {"a":2}`), &v); err == nil {
		t.Fatal("accepted trailing value")
	}
	if err := Decode([]byte(`{"a":1,"b":2}`), &v); err == nil {
		t.Fatal("accepted unknown field")
	}
	if err := Decode([]byte(`{"a":1} garbage`), &v); err == nil {
		t.Fatal("accepted trailing garbage")
	}
}

func TestDecodeRejectsInvalidUTF8(t *testing.T) {
	var v map[string]any
	if err := Decode([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}, &v); err == nil {
		t.Fatal("accepted invalid utf8")
	}
}

func TestEncodeDeterministic(t *testing.T) {
	a, err := Encode(struct {
		Z int    `json:"z"`
		A string `json:"a"`
	}{Z: 1, A: "x"})
	if err != nil || !strings.EqualFold(string(a), `{"z":1,"a":"x"}`) {
		t.Fatalf("%s %v", a, err)
	}
}

func TestEncodeRejectsInvalidUTF8(t *testing.T) {
	if _, err := Encode(struct {
		Value string `json:"value"`
	}{Value: string([]byte{0xff})}); err == nil {
		t.Fatal("encoded invalid UTF-8 by silently replacing it")
	}
}

func TestDecodeRejectsPayloadAbove64MiB(t *testing.T) {
	var v any
	if err := Decode(bytes.Repeat([]byte{' '}, MaxBytes+1), &v); err == nil {
		t.Fatal("accepted oversized payload")
	}
}

func TestDecodeEnforcesRequiredAndNullableWireShape(t *testing.T) {
	type wire struct {
		Required string  `json:"required" required:"true"`
		Nullable *string `json:"nullable" required:"true" nullable:"true"`
		Optional *string `json:"optional,omitempty"`
	}
	for _, body := range []string{
		`{"nullable":null}`,
		`{"required":"ok"}`,
		`{"required":"ok","nullable":null,"optional":null}`,
		`{"required":null,"nullable":null}`,
	} {
		var got wire
		if err := Decode([]byte(body), &got); err == nil {
			t.Fatalf("accepted invalid wire shape %s", body)
		}
	}
	var got wire
	if err := Decode([]byte(`{"required":"ok","nullable":null}`), &got); err != nil {
		t.Fatalf("rejected required nullable field: %v", err)
	}
}

func TestDecodeRejectsCaseFoldedAliasesAtEveryStructBoundary(t *testing.T) {
	type EmbeddedFields struct {
		Exact string `json:"exact" required:"true"`
	}
	type Child struct {
		ProjectID string `json:"project_id" required:"true"`
	}
	type Wire struct {
		EmbeddedFields
		Child          Child             `json:"child" required:"true"`
		Children       []Child           `json:"children" required:"true"`
		Pointer        *Child            `json:"pointer" required:"true"`
		ChildrenByName map[string]Child  `json:"children_by_name" required:"true"`
		Labels         map[string]string `json:"labels" required:"true"`
	}
	valid := `{"exact":"ok","child":{"project_id":"child"},"children":[{"project_id":"slice"}],"pointer":{"project_id":"pointer"},"children_by_name":{"map-key":{"project_id":"map-value"}},"labels":{"Arbitrary-Key":"value"}}`
	var got Wire
	if err := Decode([]byte(valid), &got); err != nil {
		t.Fatalf("valid embedded fields or explicit map rejected: %v", err)
	}
	for _, body := range []string{
		`{"exact":"ok","EXACT":"overwrite","child":{"project_id":"child"},"children":[{"project_id":"slice"}],"pointer":{"project_id":"pointer"},"children_by_name":{},"labels":{}}`,
		`{"exact":"ok","child":{"project_id":"child","PROJECT_ID":"overwrite"},"children":[{"project_id":"slice"}],"pointer":{"project_id":"pointer"},"children_by_name":{},"labels":{}}`,
		`{"exact":"ok","child":{"project_id":"child"},"children":[{"project_id":"slice","PROJECT_ID":"overwrite"}],"pointer":{"project_id":"pointer"},"children_by_name":{},"labels":{}}`,
		`{"exact":"ok","child":{"project_id":"child"},"children":[{"project_id":"slice"}],"pointer":{"project_id":"pointer","PROJECT_ID":"overwrite"},"children_by_name":{},"labels":{}}`,
		`{"exact":"ok","child":{"project_id":"child"},"children":[{"project_id":"slice"}],"pointer":{"project_id":"pointer"},"children_by_name":{"map-key":{"project_id":"map-value","PROJECT_ID":"overwrite"}},"labels":{}}`,
	} {
		var decoded Wire
		if err := Decode([]byte(body), &decoded); err == nil {
			t.Fatalf("accepted case-folded alias: %s", body)
		}
	}
}
