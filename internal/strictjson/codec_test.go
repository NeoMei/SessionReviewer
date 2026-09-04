package strictjson

import (
	"bytes"
	"strings"
	"testing"
)

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
