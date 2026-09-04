package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
)

import "context"

// These assignments are source-compatibility probes. A variadic parameter is
// not assignable to the original function type even when ordinary calls still
// compile, so each public function altered by the retention cancellation work
// is pinned explicitly here.
var (
	_ func(any) (string, error)                                = Digest
	_ func(context.Context, any) (string, error)               = DigestContext
	_ func(ObservationRevision) string                         = ObservationRevisionID
	_ func(context.Context, ObservationRevision) string        = ObservationRevisionIDContext
	_ func(SessionView) (string, error)                        = SessionViewDigest
	_ func(context.Context, SessionView) (string, error)       = SessionViewDigestContext
	_ func(ProjectProbeState) (string, error)                  = ProjectProbeStateDigest
	_ func(context.Context, ProjectProbeState) (string, error) = ProjectProbeStateDigestContext
	_ func(ProjectView) (string, error)                        = ProjectViewDigest
	_ func(context.Context, ProjectView) (string, error)       = ProjectViewDigestContext

	_ func(ObservationRevision) error                  = ValidateObservationRevision
	_ func(context.Context, ObservationRevision) error = ValidateObservationRevisionContext
	_ func(SessionView) error                          = ValidateSessionView
	_ func(context.Context, SessionView) error         = ValidateSessionViewContext
	_ func(ProjectProbeState) error                    = ValidateProjectProbeState
	_ func(context.Context, ProjectProbeState) error   = ValidateProjectProbeStateContext
	_ func(ProbeCheck) error                           = ValidateProbeCheck
	_ func(context.Context, ProbeCheck) error          = ValidateProbeCheckContext
	_ func(ProjectView) error                          = ValidateProjectView
	_ func(context.Context, ProjectView) error         = ValidateProjectViewContext
	_ func(GenerationManifest) error                   = ValidateGenerationManifest
	_ func(context.Context, GenerationManifest) error  = ValidateGenerationManifestContext
)

// TestV4ContractFixtures is intentionally kept at the contract boundary. It
// exercises the JSON-Schema subset used by these wire contracts and checks
// the one cross-document invariant that JSON Schema cannot express: index
// coverage counts must reconcile with the entries array.
func TestV4ContractFixtures(t *testing.T) {
	names := []string{"review-presentation-v4", "machine-ledger-v4", "session-index-v1", "session-summary-v1", "session-event-page-v1", "agent-annotation-v1", "pricing-snapshot-v1", "pricing-supplement-v1"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			schema := readContractJSON(t, filepath.Join("..", "..", "schemas", name+".schema.json"))
			if err := validateClosedSchemaObjects(schema, "$"); err != nil {
				t.Fatalf("schema leaves an object boundary open: %v", err)
			}
			valid := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", name+".valid.json"))
			invalid := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", name+".invalid.json"))
			if err := validateContractSchema(schema, valid, "$", schema); err != nil {
				t.Fatalf("valid fixture rejected: %v", err)
			}
			if name == "session-index-v1" {
				// Arithmetic reconciliation is deliberately a runtime invariant,
				// not a structural JSON-Schema keyword.
				if err := validateSessionIndexCoverage(valid); err != nil {
					t.Fatalf("valid coverage rejected: %v", err)
				}
				if err := validateSessionIndexCoverage(invalid); err == nil {
					t.Fatal("invalid coverage accepted")
				}
			} else if name == "session-event-page-v1" {
				if err := validateEventPageCursors(valid); err != nil {
					t.Fatalf("valid cursors rejected: %v", err)
				}
				if err := validateEventPageCursors(invalid); err == nil {
					t.Fatal("invalid empty-page cursors accepted")
				}
			} else if err := validateContractSchema(schema, invalid, "$", schema); err == nil {
				t.Fatal("invalid fixture accepted")
			}
		})
	}
}

func validateClosedSchemaObjects(value any, path string) error {
	object, ok := value.(map[string]any)
	if ok {
		if object["type"] == "object" && object["additionalProperties"] != false {
			return fmt.Errorf("%s: additionalProperties must be false", path)
		}
		for key, child := range object {
			if key == "$schema" || key == "$id" || key == "title" || key == "$comment" {
				continue
			}
			if err := validateClosedSchemaObjects(child, path+"."+key); err != nil {
				return err
			}
		}
		return nil
	}
	array, ok := value.([]any)
	if ok {
		for index, child := range array {
			if err := validateClosedSchemaObjects(child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateEventPageCursors(value any) error {
	root, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("root is not an object")
	}
	total, ok := root["total"].(json.Number)
	if !ok {
		return fmt.Errorf("total is not an integer")
	}
	if numberInt(total) != 0 {
		return nil
	}
	for _, key := range []string{"previous_cursor", "next_cursor", "first_cursor", "last_cursor"} {
		if root[key] != nil {
			return fmt.Errorf("%s must be null for an empty page", key)
		}
	}
	if root["range_start"].(json.Number) != "0" || root["range_end"].(json.Number) != "0" {
		return fmt.Errorf("empty page range must be 0-0")
	}
	return nil
}

func readContractJSON(t *testing.T, path string) any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err == nil {
		t.Fatalf("%s contains trailing JSON", path)
	}
	return value
}

func validateContractSchema(schema, value any, path string, root any) error {
	s, ok := schema.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: schema is not an object", path)
	}
	if ref, ok := s["$ref"].(string); ok {
		const prefix = "#/$defs/"
		if len(ref) < len(prefix) || ref[:len(prefix)] != prefix {
			return fmt.Errorf("%s: unsupported ref %q", path, ref)
		}
		defs, ok := root.(map[string]any)["$defs"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s: missing definitions", path)
		}
		target, ok := defs[ref[len(prefix):]]
		if !ok {
			return fmt.Errorf("%s: missing ref %q", path, ref)
		}
		return validateContractSchema(target, value, path, root)
	}
	if constValue, ok := s["const"]; ok && !reflect.DeepEqual(constValue, value) {
		return fmt.Errorf("%s: want const %v", path, constValue)
	}
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, candidate := range enum {
			if reflect.DeepEqual(candidate, value) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s: value is not in enum", path)
		}
	}
	if types, ok := s["type"].([]any); ok {
		matched := false
		for _, typ := range types {
			if schemaTypeMatches(typ.(string), value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: wrong type", path)
		}
	} else if typ, ok := s["type"].(string); ok && !schemaTypeMatches(typ, value) {
		return fmt.Errorf("%s: wrong type", path)
	}
	if pattern, ok := s["pattern"].(string); ok {
		str, ok := value.(string)
		if value == nil {
			// Nullable fields may carry a format/pattern for their string arm.
		} else if !ok || !regexp.MustCompile(pattern).MatchString(str) {
			return fmt.Errorf("%s: pattern mismatch", path)
		}
	}
	if min, ok := s["minLength"].(json.Number); ok {
		str, isString := value.(string)
		if isString && len([]byte(str)) < int(numberInt(min)) {
			return fmt.Errorf("%s: too short", path)
		}
	}
	if max, ok := s["maxLength"].(json.Number); ok {
		str, isString := value.(string)
		if isString && len([]byte(str)) > int(numberInt(max)) {
			return fmt.Errorf("%s: too long", path)
		}
	}
	if min, ok := s["minimum"].(json.Number); ok {
		if numberFloat(value) < numberFloat(min) {
			return fmt.Errorf("%s: below minimum", path)
		}
	}
	if max, ok := s["maximum"].(json.Number); ok {
		if numberFloat(value) > numberFloat(max) {
			return fmt.Errorf("%s: above maximum", path)
		}
	}
	if object, ok := value.(map[string]any); ok {
		if required, ok := s["required"].([]any); ok {
			for _, name := range required {
				if _, exists := object[name.(string)]; !exists {
					return fmt.Errorf("%s: missing %s", path, name)
				}
			}
		}
		properties, _ := s["properties"].(map[string]any)
		if additional, ok := s["additionalProperties"].(bool); ok && !additional {
			for name := range object {
				if _, exists := properties[name]; !exists {
					return fmt.Errorf("%s: unknown field %q", path, name)
				}
			}
		}
		for name, child := range properties {
			if field, exists := object[name]; exists {
				if err := validateContractSchema(child, field, path+"."+name, root); err != nil {
					return err
				}
			}
		}
	}
	if array, ok := value.([]any); ok {
		if max, ok := s["maxItems"].(json.Number); ok && len(array) > int(numberInt(max)) {
			return fmt.Errorf("%s: too many items", path)
		}
		if items, ok := s["items"]; ok {
			for index, child := range array {
				if err := validateContractSchema(items, child, fmt.Sprintf("%s[%d]", path, index), root); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func schemaTypeMatches(typ string, value any) bool {
	switch typ {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "integer", "number":
		n, ok := value.(json.Number)
		if !ok {
			return false
		}
		if typ == "number" {
			return true
		}
		return numberFloat(n) == float64(numberInt(n))
	case "null":
		return value == nil
	case "boolean":
		_, ok := value.(bool)
		return ok
	default:
		return false
	}
}

func numberInt(value json.Number) int64 {
	n, _ := value.Int64()
	return n
}

func numberFloat(value any) float64 {
	switch n := value.(type) {
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

func validateSessionIndexCoverage(value any) error {
	root, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("root is not an object")
	}
	coverage, ok := root["coverage"].(map[string]any)
	if !ok {
		return fmt.Errorf("coverage is not an object")
	}
	sessions, ok := root["sessions"].([]any)
	if !ok {
		return fmt.Errorf("sessions is not an array")
	}
	get := func(key string) (int64, error) {
		n, ok := coverage[key].(json.Number)
		if !ok {
			return 0, fmt.Errorf("coverage.%s is not an integer", key)
		}
		return numberInt(n), nil
	}
	total, err := get("total")
	if err != nil {
		return err
	}
	complete, err := get("complete")
	if err != nil {
		return err
	}
	partial, err := get("partial")
	if err != nil {
		return err
	}
	errCount, err := get("error")
	if err != nil {
		return err
	}
	unprocessed, err := get("unprocessed")
	if err != nil {
		return err
	}
	available, err := get("source_available")
	if err != nil {
		return err
	}
	unavailable, err := get("source_unavailable")
	if err != nil {
		return err
	}
	if complete+partial+errCount+unprocessed != total || available+unavailable != total || int64(len(sessions)) != total {
		return fmt.Errorf("coverage counts do not reconcile")
	}
	return nil
}
