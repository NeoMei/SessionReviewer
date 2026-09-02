// Package presentation reconciles deterministic generated content with the
// human-authored Markdown surface without treating unknown custom blocks as
// machine-controlled fields.
package presentation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type FieldKind string

const (
	ScalarField      FieldKind = "scalar"
	ListField        FieldKind = "list"
	UnsupportedField FieldKind = "unsupported"
)

// Baseline is one immutable generated field value from a rendered generation.
// GeneratedHash binds EntityID, Field, Kind, and the exact ordered value.
type Baseline struct {
	EntityID      string    "json:\"entity_id\""
	Field         string    "json:\"field\""
	Kind          FieldKind "json:\"kind\""
	Value         string    "json:\"value,omitempty\""
	Values        []string  "json:\"values,omitempty\""
	GeneratedHash string    "json:\"generated_hash\""
}

func NewScalarBaseline(entityID, field, value string) Baseline {
	result := Baseline{EntityID: entityID, Field: field, Kind: ScalarField, Value: value}
	result.GeneratedHash = baselineHash(result)
	return result
}

func NewListBaseline(entityID, field string, values []string) Baseline {
	copied := append([]string(nil), values...)
	if copied == nil {
		copied = []string{}
	}
	result := Baseline{EntityID: entityID, Field: field, Kind: ListField, Values: copied}
	result.GeneratedHash = baselineHash(result)
	return result
}

func (value Baseline) Clone() Baseline {
	value.Values = append([]string(nil), value.Values...)
	if value.Kind == ListField && value.Values == nil {
		value.Values = []string{}
	}
	return value
}

func (value Baseline) key() string {
	return value.EntityID + "\x00" + value.Field
}

func baselineHash(value Baseline) string {
	identity := struct {
		SchemaVersion int       "json:\"schema_version\""
		EntityID      string    "json:\"entity_id\""
		Field         string    "json:\"field\""
		Kind          FieldKind "json:\"kind\""
		Value         string    "json:\"value\""
		Values        []string  "json:\"values\""
	}{1, value.EntityID, value.Field, value.Kind, value.Value, value.Values}
	body, err := json.Marshal(identity)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func validateBaseline(value Baseline) error {
	if !validIdentity(value.EntityID) || !validIdentity(value.Field) {
		return fmt.Errorf("invalid baseline identity %s/%s", value.EntityID, value.Field)
	}
	switch value.Kind {
	case ScalarField:
		if value.Values != nil {
			return fmt.Errorf("scalar baseline %s/%s carries list values", value.EntityID, value.Field)
		}
	case ListField:
		if value.Value != "" || value.Values == nil {
			return fmt.Errorf("list baseline %s/%s is not an ordered non-nil list", value.EntityID, value.Field)
		}
	case UnsupportedField:
		if value.Value != "" || value.Values != nil {
			return fmt.Errorf("unsupported baseline %s/%s carries a value", value.EntityID, value.Field)
		}
	default:
		return fmt.Errorf("unsupported baseline contract %q", value.Kind)
	}
	if !validHash(value.GeneratedHash) || value.GeneratedHash != baselineHash(value) {
		return fmt.Errorf("generated baseline hash does not match canonical value %s/%s", value.EntityID, value.Field)
	}
	return nil
}
