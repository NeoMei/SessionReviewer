// Package strictjson contains the single bounded JSON boundary used by v4
// wire contracts. It deliberately does not know any package-specific schema.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

const MaxBytes = 64 << 20

func Decode(data []byte, dst any) error {
	if len(data) > MaxBytes {
		return fmt.Errorf("json exceeds %d bytes", MaxBytes)
	}
	if !utf8.Valid(data) {
		return errors.New("json is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := scanValue(dec); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := expectEOF(dec); err != nil {
		return err
	}
	var raw any
	rawDecoder := json.NewDecoder(bytes.NewReader(data))
	rawDecoder.UseNumber()
	if err := rawDecoder.Decode(&raw); err != nil {
		return fmt.Errorf("decode JSON shape: %w", err)
	}
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	return validateWireShape(raw, reflect.ValueOf(dst))
}

func Encode(v any) ([]byte, error) {
	if err := validateUTF8Strings(reflect.ValueOf(v), make(map[visit]bool)); err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxBytes {
		return nil, fmt.Errorf("json exceeds %d bytes", MaxBytes)
	}
	return b, nil
}

type visit struct {
	kind reflect.Kind
	ptr  uintptr
}

func validateUTF8Strings(value reflect.Value, seen map[visit]bool) error {
	if !value.IsValid() {
		return nil
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return errors.New("JSON value contains invalid UTF-8")
		}
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		key := visit{kind: value.Kind(), ptr: value.Pointer()}
		if seen[key] {
			return nil
		}
		seen[key] = true
		return validateUTF8Strings(value.Elem(), seen)
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if err := validateUTF8Strings(value.Field(index), seen); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		key := visit{kind: value.Kind(), ptr: value.Pointer()}
		if seen[key] {
			return nil
		}
		seen[key] = true
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validateUTF8Strings(iterator.Key(), seen); err != nil {
				return err
			}
			if err := validateUTF8Strings(iterator.Value(), seen); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.IsNil() {
			return nil
		}
		key := visit{kind: value.Kind(), ptr: value.Pointer()}
		if seen[key] {
			return nil
		}
		seen[key] = true
		fallthrough
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateUTF8Strings(value.Index(index), seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func expectEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON: %w", err)
	}
	return nil
}

func scanValue(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); ok {
		switch d {
		case '{':
			seen := map[string]struct{}{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				ks, ok := key.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[ks]; exists {
					return fmt.Errorf("duplicate object key %q", ks)
				}
				seen[ks] = struct{}{}
				if err := scanValue(dec); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil {
				return err
			}
			if end != json.Delim('}') {
				return errors.New("malformed object")
			}
		case '[':
			for dec.More() {
				if err := scanValue(dec); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil {
				return err
			}
			if end != json.Delim(']') {
				return errors.New("malformed array")
			}
		default:
			return fmt.Errorf("unexpected delimiter %q", d)
		}
	}
	return nil
}

func validateWireShape(raw any, destination reflect.Value) error {
	if destination.Kind() != reflect.Pointer || destination.IsNil() {
		return errors.New("decode destination must be a non-nil pointer")
	}
	return validateShapeValue(raw, destination.Elem(), "$", false)
}

func validateShapeValue(raw any, destination reflect.Value, path string, nullable bool) error {
	if raw == nil {
		if nullable {
			return nil
		}
		return fmt.Errorf("%s must not be null", path)
	}
	for destination.Kind() == reflect.Pointer {
		if destination.IsNil() {
			return nil
		}
		destination = destination.Elem()
	}
	switch destination.Kind() {
	case reflect.Struct:
		object, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		fields := collectJSONFields(destination.Type(), nil)
		byName := make(map[string]jsonField, len(fields))
		for _, field := range fields {
			byName[field.name] = field
		}
		for name := range object {
			if _, allowed := byName[name]; !allowed {
				return fmt.Errorf("%s.%s is not an exact JSON field", path, name)
			}
		}
		for _, field := range fields {
			value, exists := object[field.name]
			if !exists {
				if field.required {
					return fmt.Errorf("%s.%s is required", path, field.name)
				}
				continue
			}
			if !field.required && field.typ.Kind() == reflect.Pointer && value == nil {
				return fmt.Errorf("%s.%s must be omitted instead of null", path, field.name)
			}
			if err := validateShapeValue(value, fieldByIndex(destination, field.index), path+"."+field.name, field.nullable); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		array, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		for i, value := range array {
			var element reflect.Value
			if i < destination.Len() {
				element = destination.Index(i)
			} else {
				element = reflect.New(destination.Type().Elem()).Elem()
			}
			if err := validateShapeValue(value, element, fmt.Sprintf("%s[%d]", path, i), false); err != nil {
				return err
			}
		}
	case reflect.Map:
		object, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		if destination.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("%s must use string map keys", path)
		}
		for key, value := range object {
			element := reflect.New(destination.Type().Elem()).Elem()
			if current := destination.MapIndex(reflect.ValueOf(key).Convert(destination.Type().Key())); current.IsValid() {
				element = current
			}
			if err := validateShapeValue(value, element, path+"."+key, false); err != nil {
				return err
			}
		}
	}
	return nil
}

type jsonField struct {
	name               string
	index              []int
	typ                reflect.Type
	required, nullable bool
}

func collectJSONFields(t reflect.Type, prefix []int) []jsonField {
	fields := make([]jsonField, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}
		tag := field.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "-" {
			continue
		}
		index := append(append([]int(nil), prefix...), i)
		embeddedType := field.Type
		if embeddedType.Kind() == reflect.Pointer {
			embeddedType = embeddedType.Elem()
		}
		if field.Anonymous && name == "" && embeddedType.Kind() == reflect.Struct {
			fields = append(fields, collectJSONFields(embeddedType, index)...)
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields = append(fields, jsonField{
			name: name, index: index, typ: field.Type,
			required: field.Tag.Get("required") == "true",
			nullable: field.Tag.Get("nullable") == "true",
		})
	}
	return fields
}

func fieldByIndex(value reflect.Value, index []int) reflect.Value {
	for offset, fieldIndex := range index {
		for value.Kind() == reflect.Pointer {
			if value.IsNil() {
				return reflect.New(fieldTypeAt(value.Type(), index[offset:])).Elem()
			}
			value = value.Elem()
		}
		value = value.Field(fieldIndex)
	}
	return value
}

func fieldTypeAt(t reflect.Type, index []int) reflect.Type {
	for _, fieldIndex := range index {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		t = t.Field(fieldIndex).Type
	}
	return t
}
