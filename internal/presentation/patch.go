package presentation

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
)

type Operation string

const (
	Set            Operation = "set"
	Suppress       Operation = "suppress"
	RestoreDefault Operation = "restore_default"
)

const (
	UnderlayChanged = "underlay_changed"
	OrphanPatch     = "orphan_patch"
)

var (
	ErrDuplicatePatch       = errors.New("duplicate presentation patch")
	ErrDuplicateBaseline    = errors.New("duplicate presentation baseline")
	ErrUnsupportedField     = errors.New("unsupported presentation field")
	ErrChangedFieldContract = errors.New("presentation field contract changed")
)

var identityPattern = regexp.MustCompile("^[a-z0-9][a-z0-9._-]{0,127}$")
var hashPattern = regexp.MustCompile("^[0-9a-f]{64}$")

type Patch struct {
	EntityID          string    "json:\"entity_id\""
	Field             string    "json:\"field\""
	Operation         Operation "json:\"operation\""
	Value             string    "json:\"value,omitempty\""
	Values            []string  "json:\"values,omitempty\""
	BaseGeneratedHash string    "json:\"base_generated_hash\""
}

type Diagnostic struct {
	Code     string "json:\"code\""
	EntityID string "json:\"entity_id\""
	Field    string "json:\"field\""
}

type RebaseResult struct {
	Active      []Patch      "json:\"active\""
	Orphans     []Patch      "json:\"orphans\""
	Diagnostics []Diagnostic "json:\"diagnostics\""
}

type FieldObservation struct {
	EntityID string
	Field    string
	Present  bool
	Value    string
	Values   []string
	Intent   Operation
}

type CaptureInput struct {
	PreviousPatches   []Patch
	PreviousBaselines []Baseline
	Fields            []FieldObservation
	UnknownBlocks     map[string][]byte
}

type CaptureResult struct {
	Patches       []Patch
	UnknownBlocks map[string][]byte
	Diagnostics   []Diagnostic
}

type AppliedField struct {
	Kind            FieldKind
	Value           string
	Values          []string
	Present         bool
	RestoredDefault bool
}

func Rebase(patches []Patch, baselines []Baseline) (RebaseResult, error) {
	if err := validatePatchSet(patches); err != nil {
		return RebaseResult{}, err
	}
	next, err := baselineSet(baselines)
	if err != nil {
		return RebaseResult{}, err
	}
	result := RebaseResult{}
	for _, patch := range patches {
		baseline, exists := next[patch.key()]
		if !exists {
			result.Orphans = append(result.Orphans, patch.Clone())
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: OrphanPatch, EntityID: patch.EntityID, Field: patch.Field})
			continue
		}
		if baseline.Kind == UnsupportedField {
			return RebaseResult{}, fmt.Errorf("%w: %s/%s", ErrUnsupportedField, patch.EntityID, patch.Field)
		}
		if err := patchContractMatches(patch, baseline); err != nil {
			return RebaseResult{}, err
		}
		result.Active = append(result.Active, patch.Clone())
		if patch.BaseGeneratedHash != baseline.GeneratedHash {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: UnderlayChanged, EntityID: patch.EntityID, Field: patch.Field})
		}
	}
	sortPatches(result.Active)
	sortPatches(result.Orphans)
	sortDiagnostics(result.Diagnostics)
	return result, nil
}

func Capture(input CaptureInput) (CaptureResult, error) {
	if err := validatePatchSet(input.PreviousPatches); err != nil {
		return CaptureResult{}, err
	}
	_, err := baselineSet(input.PreviousBaselines)
	if err != nil {
		return CaptureResult{}, err
	}
	previous := make(map[string]Baseline, len(input.PreviousBaselines))
	for _, value := range input.PreviousBaselines {
		previous[value.key()] = value
	}
	patchByID := make(map[string]Patch, len(input.PreviousPatches))
	for _, patch := range input.PreviousPatches {
		patchByID[patch.key()] = patch
	}
	current, err := fieldObservationSet(input.Fields)
	if err != nil {
		return CaptureResult{}, err
	}
	for key := range current {
		if _, supported := previous[key]; !supported {
			return CaptureResult{}, fmt.Errorf("%w: %s/%s", ErrUnsupportedField, current[key].EntityID, current[key].Field)
		}
	}
	for _, patch := range input.PreviousPatches {
		if baseline, supported := previous[patch.key()]; supported && baseline.Kind == UnsupportedField {
			return CaptureResult{}, fmt.Errorf("%w: %s/%s", ErrUnsupportedField, patch.EntityID, patch.Field)
		}
	}
	for _, value := range input.Fields {
		switch value.Intent {
		case "", Suppress, RestoreDefault:
		default:
			return CaptureResult{}, fmt.Errorf("invalid presentation marker intent %q", value.Intent)
		}
	}
	result := CaptureResult{UnknownBlocks: cloneUnknownBlocks(input.UnknownBlocks)}
	for _, baseline := range sortedBaselines(input.PreviousBaselines) {
		if baseline.Kind == UnsupportedField {
			continue
		}
		observation := current[baseline.key()]
		if observation.Present {
			if err := observationContractMatches(observation, baseline); err != nil {
				return CaptureResult{}, err
			}
		}
		prior, hadPatch := patchByID[baseline.key()]
		var captured Patch
		switch {
		case !observation.Present:
			captured = Patch{EntityID: baseline.EntityID, Field: baseline.Field, Operation: Suppress, BaseGeneratedHash: baseline.GeneratedHash}
			if hadPatch && prior.Operation == Suppress {
				captured = prior.Clone()
			}
		case observation.Intent == RestoreDefault:
			captured = Patch{EntityID: baseline.EntityID, Field: baseline.Field, Operation: RestoreDefault, BaseGeneratedHash: baseline.GeneratedHash}
		case observation.Intent == Suppress:
			captured = Patch{EntityID: baseline.EntityID, Field: baseline.Field, Operation: Suppress, BaseGeneratedHash: baseline.GeneratedHash}
		case hadPatch && prior.Operation == Set && observationEqual(observation.Value, observation.Values, prior.Value, prior.Values):
			captured = prior.Clone()
		case hadPatch && prior.Operation == RestoreDefault && observationEqual(observation.Value, observation.Values, baseline.Value, baseline.Values):
			captured = prior.Clone()
		case hadPatch && prior.Operation == Suppress && observationEqual(observation.Value, observation.Values, baseline.Value, baseline.Values):
			continue
		case !observationEqual(observation.Value, observation.Values, baseline.Value, baseline.Values):
			captured = Patch{
				EntityID: baseline.EntityID, Field: baseline.Field, Operation: Set,
				Value: observation.Value, Values: append([]string(nil), observation.Values...),
				BaseGeneratedHash: baseline.GeneratedHash,
			}
		default:
			continue
		}
		result.Patches = append(result.Patches, captured)
	}
	for _, patch := range input.PreviousPatches {
		if _, supported := previous[patch.key()]; !supported {
			result.Patches = append(result.Patches, patch.Clone())
		}
	}
	sortPatches(result.Patches)
	sortDiagnostics(result.Diagnostics)
	return result, nil
}

type patchWire struct {
	EntityID          string          "json:\"entity_id\""
	Field             string          "json:\"field\""
	Operation         Operation       "json:\"operation\""
	Value             string          "json:\"value,omitempty\""
	Values            json.RawMessage "json:\"values,omitempty\""
	BaseGeneratedHash string          "json:\"base_generated_hash\""
}

func (value Patch) MarshalJSON() ([]byte, error) {
	var values json.RawMessage
	if value.Values != nil {
		encoded, err := json.Marshal(value.Values)
		if err != nil {
			return nil, err
		}
		values = encoded
	}
	return json.Marshal(patchWire{
		EntityID: value.EntityID, Field: value.Field, Operation: value.Operation,
		Value: value.Value, Values: values,
		BaseGeneratedHash: value.BaseGeneratedHash,
	})
}

func (value *Patch) UnmarshalJSON(body []byte) error {
	var wire patchWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return err
	}
	result := Patch{
		EntityID: wire.EntityID, Field: wire.Field, Operation: wire.Operation,
		Value: wire.Value, BaseGeneratedHash: wire.BaseGeneratedHash,
	}
	if len(wire.Values) != 0 && string(wire.Values) != "null" {
		if err := json.Unmarshal(wire.Values, &result.Values); err != nil {
			return err
		}
	}
	*value = result
	return nil
}

func Apply(patches []Patch, baselines []Baseline) (map[string]AppliedField, error) {
	if err := validatePatchSet(patches); err != nil {
		return nil, err
	}
	byID, err := baselineSet(baselines)
	if err != nil {
		return nil, err
	}
	result := make(map[string]AppliedField, len(byID))
	for key, baseline := range byID {
		result[key] = AppliedField{Kind: baseline.Kind, Value: baseline.Value, Values: append([]string(nil), baseline.Values...), Present: true}
	}
	for _, patch := range patches {
		baseline, exists := byID[patch.key()]
		if !exists {
			return nil, fmt.Errorf("active presentation patch has no baseline %s/%s", patch.EntityID, patch.Field)
		}
		if baseline.Kind == UnsupportedField {
			return nil, fmt.Errorf("%w: %s/%s", ErrUnsupportedField, patch.EntityID, patch.Field)
		}
		if err := patchContractMatches(patch, baseline); err != nil {
			return nil, err
		}
		switch patch.Operation {
		case Set:
			result[patch.key()] = AppliedField{Kind: baseline.Kind, Value: patch.Value, Values: append([]string(nil), patch.Values...), Present: true}
		case Suppress:
			field := result[patch.key()]
			field.Present, field.RestoredDefault = false, false
			result[patch.key()] = field
		case RestoreDefault:
			result[patch.key()] = AppliedField{Kind: baseline.Kind, Value: baseline.Value, Values: append([]string(nil), baseline.Values...), Present: true, RestoredDefault: true}
		}
	}
	return result, nil
}

func (value Patch) Clone() Patch {
	value.Values = append([]string(nil), value.Values...)
	return value
}

func (value Patch) key() string {
	return value.EntityID + "\x00" + value.Field
}

func validatePatchSet(values []Patch) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validIdentity(value.EntityID) || !validIdentity(value.Field) {
			return fmt.Errorf("invalid presentation patch identity %s/%s", value.EntityID, value.Field)
		}
		if _, duplicate := seen[value.key()]; duplicate {
			return fmt.Errorf("%w: %s/%s", ErrDuplicatePatch, value.EntityID, value.Field)
		}
		seen[value.key()] = struct{}{}
		if !validHash(value.BaseGeneratedHash) {
			return fmt.Errorf("invalid presentation patch base hash %s/%s", value.EntityID, value.Field)
		}
		switch value.Operation {
		case Set:
			if value.Value != "" && value.Values != nil {
				return fmt.Errorf("presentation set %s/%s carries scalar and list values", value.EntityID, value.Field)
			}
		case Suppress, RestoreDefault:
			if value.Value != "" || value.Values != nil {
				return fmt.Errorf("presentation %s %s/%s carries a value", value.Operation, value.EntityID, value.Field)
			}
		default:
			return fmt.Errorf("invalid presentation patch operation %q", value.Operation)
		}
	}
	return nil
}

func baselineSet(values []Baseline) (map[string]Baseline, error) {
	result := make(map[string]Baseline, len(values))
	for _, value := range values {
		if err := validateBaseline(value); err != nil {
			return nil, err
		}
		if _, duplicate := result[value.key()]; duplicate {
			return nil, fmt.Errorf("%w: %s/%s", ErrDuplicateBaseline, value.EntityID, value.Field)
		}
		result[value.key()] = value.Clone()
	}
	return result, nil
}

func fieldObservationSet(values []FieldObservation) (map[string]FieldObservation, error) {
	result := make(map[string]FieldObservation, len(values))
	for _, value := range values {
		if !validIdentity(value.EntityID) || !validIdentity(value.Field) {
			return nil, fmt.Errorf("invalid presentation observation %s/%s", value.EntityID, value.Field)
		}
		if _, duplicate := result[value.key()]; duplicate {
			return nil, fmt.Errorf("duplicate presentation observation %s/%s", value.EntityID, value.Field)
		}
		result[value.key()] = value
	}
	return result, nil
}

func (value FieldObservation) key() string {
	return value.EntityID + "\x00" + value.Field
}

func patchContractMatches(patch Patch, baseline Baseline) error {
	if baseline.Kind == ScalarField && patch.Values != nil {
		return fmt.Errorf("%w: %s/%s", ErrChangedFieldContract, patch.EntityID, patch.Field)
	}
	if baseline.Kind == ListField && (patch.Value != "" || patch.Values == nil) {
		return fmt.Errorf("%w: %s/%s", ErrChangedFieldContract, patch.EntityID, patch.Field)
	}
	return nil
}

func observationContractMatches(observation FieldObservation, baseline Baseline) error {
	if baseline.Kind == ScalarField && observation.Values != nil {
		return fmt.Errorf("%w: %s/%s", ErrChangedFieldContract, observation.EntityID, observation.Field)
	}
	if baseline.Kind == ListField && (observation.Value != "" || observation.Values == nil) {
		return fmt.Errorf("%w: %s/%s", ErrChangedFieldContract, observation.EntityID, observation.Field)
	}
	return nil
}

func observationEqual(leftValue string, leftValues []string, rightValue string, rightValues []string) bool {
	if leftValue != rightValue || len(leftValues) != len(rightValues) {
		return false
	}
	for index := range leftValues {
		if leftValues[index] != rightValues[index] {
			return false
		}
	}
	return true
}

func sortPatches(values []Patch) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].EntityID != values[j].EntityID {
			return values[i].EntityID < values[j].EntityID
		}
		return values[i].Field < values[j].Field
	})
}

func sortDiagnostics(values []Diagnostic) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Code != values[j].Code {
			return values[i].Code < values[j].Code
		}
		if values[i].EntityID != values[j].EntityID {
			return values[i].EntityID < values[j].EntityID
		}
		return values[i].Field < values[j].Field
	})
}

func sortedBaselines(values []Baseline) []Baseline {
	result := append([]Baseline(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].EntityID != result[j].EntityID {
			return result[i].EntityID < result[j].EntityID
		}
		return result[i].Field < result[j].Field
	})
	return result
}

func cloneUnknownBlocks(values map[string][]byte) map[string][]byte {
	if values == nil {
		return nil
	}
	result := make(map[string][]byte, len(values))
	for key, value := range values {
		result[key] = append([]byte(nil), value...)
	}
	return result
}

func sortedUnknownKeys(values map[string][]byte) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func validIdentity(value string) bool {
	return identityPattern.MatchString(value)
}

func validHash(value string) bool {
	return hashPattern.MatchString(value)
}
