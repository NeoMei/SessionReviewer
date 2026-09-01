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

var unorderedJSONObjects = map[string]struct{}{
	"active_revisions":     {},
	"superseded_revisions": {},
	"withdrawn_revisions":  {},
}

// Digest returns a deterministic digest of a normalized defensive JSON copy.
// Ordered arrays, including SessionView dependencies, retain caller order.
func Digest(value any, checkpoints ...func() error) (string, error) {
	if err := validateDigestValue(reflect.ValueOf(value), make(map[digestVisit]bool), checkpoints...); err != nil {
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
	if err := digestCheckpoint(checkpoints); err != nil {
		return "", err
	}
	normalized, err := normalizeJSONValue(copyValue, "", checkpoints...)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	if err := writeNormalizedDigest(hash, normalized, checkpoints...); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// ObservationRevisionID deliberately excludes the stored revision_id. It
// combines the stable key with the normalized observed payload, source hash,
// and adapter version so adapter re-decoding creates an immutable successor.
func ObservationRevisionID(value ObservationRevision, checkpoints ...func() error) string {
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
	digest, err := Digest(identity, checkpoints...)
	if err != nil {
		return ""
	}
	return digest
}

type SessionViewIdentity struct {
	SchemaVersion           int                  `json:"schema_version"`
	ProjectID               string               `json:"project_id"`
	Provider                string               `json:"provider"`
	SessionID               string               `json:"session_id"`
	SourceIdentity          string               `json:"source_identity"`
	SourceRecordDigest      string               `json:"source_record_digest"`
	UsageRecordDigest       string               `json:"usage_record_digest"`
	StartedAt               string               `json:"started_at"`
	EndedAt                 string               `json:"ended_at"`
	TerminalState           TerminalState        `json:"terminal_state"`
	SourceAvailability      SourceAvailability   `json:"source_availability"`
	ActiveRevisionIDs       []string             `json:"active_revision_ids"`
	ObservationSummaries    []ObservationSummary `json:"observation_summaries"`
	ObservationChunkDigests []string             `json:"observation_chunk_digests"`
	DerivedRecords          []DerivedRecord      `json:"derived_records"`
	Diagnostics             []Diagnostic         `json:"diagnostics"`
	DependencyDigest        string               `json:"dependency_digest"`
	MaterializerVersion     string               `json:"materializer_version"`
}

type ProjectProbeStateIdentity struct {
	SchemaVersion           int          `json:"schema_version"`
	ProjectID               string       `json:"project_id"`
	CanonicalRoot           string       `json:"canonical_root"`
	Branch                  string       `json:"branch,omitempty"`
	Head                    string       `json:"head,omitempty"`
	DirtyPathCount          int          `json:"dirty_path_count"`
	RemoteIdentityHashes    []string     `json:"remote_identity_hashes"`
	VersionFiles            []ProbeFile  `json:"version_files"`
	RequiredProjectionFiles []ProbeFile  `json:"required_projection_files"`
	ProbeVersion            string       `json:"probe_version"`
	Diagnostics             []Diagnostic `json:"diagnostics"`
}

type ProjectViewIdentity struct {
	SchemaVersion           int                     `json:"schema_version"`
	ProjectID               string                  `json:"project_id"`
	Generation              int                     `json:"generation"`
	StartedAt               string                  `json:"started_at"`
	EndedAt                 string                  `json:"ended_at"`
	SourceSessions          int                     `json:"source_sessions"`
	TerminalCounts          TerminalCounts          `json:"terminal_counts"`
	SessionViewDependencies []SessionViewDependency `json:"session_view_dependencies"`
	ObservationRevisionIDs  []string                `json:"observation_revision_ids"`
	ProbeStateDigest        string                  `json:"probe_state_digest"`
	LiveState               StateSnapshot           `json:"live_state"`
	WitnessedState          []DerivedRecord         `json:"witnessed_state"`
	DerivedRecords          []DerivedRecord         `json:"derived_records"`
	AssociatedUsage         []AssociatedUsage       `json:"associated_usage"`
	PreviousViewDigest      string                  `json:"previous_view_digest,omitempty"`
	DependencyDigest        string                  `json:"dependency_digest"`
	ReducerVersion          string                  `json:"reducer_version"`
}

func SessionViewDigest(value SessionView, checkpoints ...func() error) (string, error) {
	return Digest(SessionViewIdentity{
		SchemaVersion:           value.SchemaVersion,
		ProjectID:               value.ProjectID,
		Provider:                value.Provider,
		SessionID:               value.SessionID,
		SourceIdentity:          value.SourceIdentity,
		SourceRecordDigest:      value.SourceRecordDigest,
		UsageRecordDigest:       value.UsageRecordDigest,
		StartedAt:               value.StartedAt,
		EndedAt:                 value.EndedAt,
		TerminalState:           value.TerminalState,
		SourceAvailability:      value.SourceAvailability,
		ActiveRevisionIDs:       value.ActiveRevisionIDs,
		ObservationSummaries:    value.ObservationSummaries,
		ObservationChunkDigests: value.ObservationChunkDigests,
		DerivedRecords:          value.DerivedRecords,
		Diagnostics:             value.Diagnostics,
		DependencyDigest:        value.DependencyDigest,
		MaterializerVersion:     value.MaterializerVersion,
	}, checkpoints...)
}

func ProjectProbeStateDigest(value ProjectProbeState, checkpoints ...func() error) (string, error) {
	return Digest(ProjectProbeStateIdentity{
		SchemaVersion:           value.SchemaVersion,
		ProjectID:               value.ProjectID,
		CanonicalRoot:           value.CanonicalRoot,
		Branch:                  value.Branch,
		Head:                    value.Head,
		DirtyPathCount:          value.DirtyPathCount,
		RemoteIdentityHashes:    value.RemoteIdentityHashes,
		VersionFiles:            value.VersionFiles,
		RequiredProjectionFiles: value.RequiredProjectionFiles,
		ProbeVersion:            value.ProbeVersion,
		Diagnostics:             value.Diagnostics,
	}, checkpoints...)
}

func ProjectViewDigest(value ProjectView, checkpoints ...func() error) (string, error) {
	return Digest(ProjectViewIdentity{
		SchemaVersion:           value.SchemaVersion,
		ProjectID:               value.ProjectID,
		Generation:              value.Generation,
		StartedAt:               value.StartedAt,
		EndedAt:                 value.EndedAt,
		SourceSessions:          value.SourceSessions,
		TerminalCounts:          value.TerminalCounts,
		SessionViewDependencies: value.SessionViewDependencies,
		ObservationRevisionIDs:  value.ObservationRevisionIDs,
		ProbeStateDigest:        value.ProbeStateDigest,
		LiveState:               value.LiveState,
		WitnessedState:          value.WitnessedState,
		DerivedRecords:          value.DerivedRecords,
		AssociatedUsage:         value.AssociatedUsage,
		PreviousViewDigest:      value.PreviousViewDigest,
		DependencyDigest:        value.DependencyDigest,
		ReducerVersion:          value.ReducerVersion,
	}, checkpoints...)
}

type digestVisit struct {
	typeOf reflect.Type
	ptr    uintptr
}

func validateDigestValue(value reflect.Value, visiting map[digestVisit]bool, checkpoints ...func() error) error {
	if err := digestCheckpoint(checkpoints); err != nil {
		return err
	}
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
		return validateDigestValue(value.Elem(), visiting, checkpoints...)
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
			if err := validateDigestValue(value.Field(index), visiting, checkpoints...); err != nil {
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
			if err := validateDigestValue(iterator.Key(), visiting, checkpoints...); err != nil {
				return err
			}
			if err := validateDigestValue(iterator.Value(), visiting, checkpoints...); err != nil {
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
			if err := validateDigestValue(value.Index(index), visiting, checkpoints...); err != nil {
				return err
			}
		}
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateDigestValue(value.Index(index), visiting, checkpoints...); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeJSONValue(value any, field string, checkpoints ...func() error) (any, error) {
	if err := digestCheckpoint(checkpoints); err != nil {
		return nil, err
	}
	switch current := value.(type) {
	case nil:
		if _, unordered := unorderedJSONArrays[field]; unordered {
			return []any{}, nil
		}
		if _, unordered := unorderedJSONObjects[field]; unordered {
			return map[string]any{}, nil
		}
		return nil, nil
	case map[string]any:
		copyMap := make(map[string]any, len(current))
		for name, child := range current {
			if !utf8.ValidString(name) {
				return nil, errors.New("digest input contains invalid UTF-8 map key")
			}
			normalized, err := normalizeJSONValue(child, name, checkpoints...)
			if err != nil {
				return nil, err
			}
			copyMap[name] = normalized
		}
		return copyMap, nil
	case []any:
		copyArray := make([]any, len(current))
		for index, child := range current {
			normalized, err := normalizeJSONValue(child, "", checkpoints...)
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

func digestCheckpoint(checkpoints []func() error) error {
	if len(checkpoints) == 0 || checkpoints[0] == nil {
		return nil
	}
	return checkpoints[0]()
}

type digestWriter interface {
	Write([]byte) (int, error)
}

func writeNormalizedDigest(writer digestWriter, value any, checkpoints ...func() error) error {
	if err := digestCheckpoint(checkpoints); err != nil {
		return err
	}
	write := func(body []byte) error {
		_, err := writer.Write(body)
		return err
	}
	switch current := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if err := write([]byte{'{'}); err != nil {
			return err
		}
		for index, key := range keys {
			if index > 0 {
				if err := write([]byte{','}); err != nil {
					return err
				}
			}
			encoded, err := json.Marshal(key)
			if err != nil {
				return fmt.Errorf("encode normalized digest key: %w", err)
			}
			if err := write(encoded); err != nil {
				return err
			}
			if err := write([]byte{':'}); err != nil {
				return err
			}
			if err := writeNormalizedDigest(writer, current[key], checkpoints...); err != nil {
				return err
			}
		}
		return write([]byte{'}'})
	case []any:
		if err := write([]byte{'['}); err != nil {
			return err
		}
		for index := range current {
			if index > 0 {
				if err := write([]byte{','}); err != nil {
					return err
				}
			}
			if err := writeNormalizedDigest(writer, current[index], checkpoints...); err != nil {
				return err
			}
		}
		return write([]byte{']'})
	default:
		encoded, err := json.Marshal(current)
		if err != nil {
			return fmt.Errorf("encode normalized digest input: %w", err)
		}
		for offset := 0; offset < len(encoded); offset += 64 << 10 {
			if err := digestCheckpoint(checkpoints); err != nil {
				return err
			}
			end := min(len(encoded), offset+(64<<10))
			if err := write(encoded[offset:end]); err != nil {
				return err
			}
		}
		return nil
	}
}
