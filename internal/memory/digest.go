package memory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"unicode/utf8"
)

var unorderedJSONArrays = map[string]struct{}{
	"active_revision_ids":       {},
	"associated_usage":          {},
	"dependency_revision_ids":   {},
	"observation_revision_ids":  {},
	"project_ids":               {},
	"remote_identity_hashes":    {},
	"required_projection_files": {},
	"version_files":             {},
}

// Digest returns a deterministic digest of a normalized defensive JSON copy.
// Ordered arrays, including SessionView dependencies, retain caller order.
func Digest(value any) (string, error) {
	if err := validateDigestValue(reflect.ValueOf(value), make(map[digestVisit]bool)); err != nil {
		return "", err
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode digest input: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var copyValue any
	if err := decoder.Decode(&copyValue); err != nil {
		return "", fmt.Errorf("decode defensive digest copy: %w", err)
	}
	normalized, err := normalizeJSONValue(copyValue, "")
	if err != nil {
		return "", err
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode normalized digest input: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ObservationRevisionID deliberately excludes the stored revision_id. It
// combines the stable key with the normalized observed payload, source hash,
// and adapter version so adapter re-decoding creates an immutable successor.
func ObservationRevisionID(value ObservationRevision) string {
	identity := struct {
		Key            ObservationKey    `json:"key"`
		Timestamp      string            `json:"timestamp"`
		Operation      string            `json:"operation,omitempty"`
		Object         string            `json:"object,omitempty"`
		Outcome        string            `json:"outcome,omitempty"`
		Fields         map[string]string `json:"fields,omitempty"`
		Excerpt        string            `json:"excerpt,omitempty"`
		SourceHash     string            `json:"source_hash"`
		AdapterVersion string            `json:"adapter_version"`
	}{
		Key:            value.Key,
		Timestamp:      value.Timestamp,
		Operation:      value.Operation,
		Object:         value.Object,
		Outcome:        value.Outcome,
		Fields:         value.Fields,
		Excerpt:        value.Excerpt,
		SourceHash:     value.Ref.SourceHash,
		AdapterVersion: value.AdapterVersion,
	}
	digest, err := Digest(identity)
	if err != nil {
		return ""
	}
	return digest
}

type digestVisit struct {
	typeOf reflect.Type
	ptr    uintptr
}

func validateDigestValue(value reflect.Value, visiting map[digestVisit]bool) error {
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
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		visit := digestVisit{typeOf: value.Type(), ptr: value.Pointer()}
		if visiting[visit] {
			return errors.New("digest input contains a cycle")
		}
		visiting[visit] = true
		defer delete(visiting, visit)
		return validateDigestValue(value.Elem(), visiting)
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return errors.New("digest input contains invalid UTF-8")
		}
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return errors.New("digest input contains NaN or Inf")
		}
	case reflect.Struct:
		typeOf := value.Type()
		for index := 0; index < value.NumField(); index++ {
			if typeOf.Field(index).PkgPath != "" {
				continue
			}
			if err := validateDigestValue(value.Field(index), visiting); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		visit := digestVisit{typeOf: value.Type(), ptr: value.Pointer()}
		if visiting[visit] {
			return errors.New("digest input contains a cycle")
		}
		visiting[visit] = true
		defer delete(visiting, visit)
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validateDigestValue(iterator.Key(), visiting); err != nil {
				return err
			}
			if err := validateDigestValue(iterator.Value(), visiting); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.IsNil() || value.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		visit := digestVisit{typeOf: value.Type(), ptr: value.Pointer()}
		if visiting[visit] {
			return errors.New("digest input contains a cycle")
		}
		visiting[visit] = true
		defer delete(visiting, visit)
		for index := 0; index < value.Len(); index++ {
			if err := validateDigestValue(value.Index(index), visiting); err != nil {
				return err
			}
		}
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateDigestValue(value.Index(index), visiting); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeJSONValue(value any, field string) (any, error) {
	switch current := value.(type) {
	case map[string]any:
		copyMap := make(map[string]any, len(current))
		for name, child := range current {
			if !utf8.ValidString(name) {
				return nil, errors.New("digest input contains invalid UTF-8 map key")
			}
			normalized, err := normalizeJSONValue(child, name)
			if err != nil {
				return nil, err
			}
			copyMap[name] = normalized
		}
		return copyMap, nil
	case []any:
		copyArray := make([]any, len(current))
		for index, child := range current {
			normalized, err := normalizeJSONValue(child, "")
			if err != nil {
				return nil, err
			}
			copyArray[index] = normalized
		}
		if _, unordered := unorderedJSONArrays[field]; unordered {
			sort.Slice(copyArray, func(i, j int) bool {
				left, _ := json.Marshal(copyArray[i])
				right, _ := json.Marshal(copyArray[j])
				return bytes.Compare(left, right) < 0
			})
		}
		return copyArray, nil
	case string:
		if !utf8.ValidString(current) {
			return nil, errors.New("digest input contains invalid UTF-8")
		}
		return current, nil
	default:
		return current, nil
	}
}
