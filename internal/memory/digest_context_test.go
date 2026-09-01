package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
)

func TestDigestContextCancelsInsideEachCanonicalPhase(t *testing.T) {
	mapValue := make(map[string]string, 4096)
	setValues := make([]string, 4096)
	for index := range setValues {
		value := fmt.Sprintf("value-%08d", len(setValues)-index)
		mapValue[value] = value
		setValues[index] = value
	}
	tests := []struct {
		name  string
		phase digestPhase
		value any
	}{
		{name: "traversal", phase: digestPhaseTraversal, value: []any{mapValue, setValues}},
		{name: "string key sort", phase: digestPhaseMapKeySort, value: mapValue},
		{name: "unordered array sort", phase: digestPhaseUnorderedArraySort, value: struct {
			ProjectIDs []string `json:"project_ids"`
		}{ProjectIDs: setValues}},
		{name: "encoding", phase: digestPhaseEncoding, value: []any{mapValue, setValues}},
		{name: "hashing", phase: digestPhaseHashing, value: []any{mapValue, setValues}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("cancel during " + test.name)
			ctx, cancel := context.WithCancelCause(context.Background())
			calls := 0
			ctx = withDigestPhaseHook(ctx, func(phase digestPhase) {
				if phase == test.phase && calls < 16 {
					calls++
					if calls == 16 {
						cancel(cause)
					}
				}
			})
			if _, err := DigestContext(ctx, test.value); !errors.Is(err, cause) {
				t.Fatalf("DigestContext error=%v want cancellation cause", err)
			}
			if calls != 16 {
				t.Fatalf("phase checkpoints=%d want 16", calls)
			}
		})
	}
}

func TestDigestContextCancelsInsideLongCanonicalComparisons(t *testing.T) {
	prefix := strings.Repeat("共同前缀", 256*1024)
	tests := []struct {
		name  string
		phase digestPhase
		value any
	}{
		{
			name:  "map key comparison",
			phase: digestPhaseMapKeySort,
			value: map[string]string{prefix + "b": "second", prefix + "a": "first"},
		},
		{
			name:  "unordered element comparison",
			phase: digestPhaseUnorderedArraySort,
			value: struct {
				ProjectIDs []string `json:"project_ids"`
			}{ProjectIDs: []string{prefix + "b", prefix + "a"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("cancel inside " + test.name)
			ctx, cancel := context.WithCancelCause(context.Background())
			calls := 0
			ctx = withDigestPhaseHook(ctx, func(phase digestPhase) {
				if phase == test.phase {
					calls++
					if calls == 10 {
						cancel(cause)
					}
				}
			})
			if _, err := DigestContext(ctx, test.value); !errors.Is(err, cause) {
				t.Fatalf("DigestContext error=%v want cancellation cause", err)
			}
			if calls != 10 {
				t.Fatalf("comparison checkpoints=%d want 10", calls)
			}
		})
	}
}

func TestStreamingCanonicalMatchesLegacyDigestAndJSONAcrossMemoryContracts(t *testing.T) {
	key := validObservationKey()
	observation := validObservation(key, "v1", map[string]string{"status": "passed"})
	source := SourceRecord{
		SchemaVersion: MemorySchemaVersion, Provider: "codex", SessionID: "s1", SourceIdentity: "src1",
		StartedAt: "2026-08-31T10:00:00Z", EndedAt: "2026-08-31T10:00:01Z",
		FrozenBoundary: FrozenBoundary{Location: SourceLocation{Kind: SourceLocationJSONL, JSONL: &JSONLSourceLocation{Line: 7, ByteOffset: 99}}, SourceHash: strings.Repeat("a", 64)},
		Availability:   SourceAvailable,
		Usage: accounting.SessionUsage{
			StartedAt: "2026-08-31T10:00:00Z", EndedAt: "2026-08-31T10:00:01Z", DurationMS: 1000,
			Models: []accounting.ModelUsage{{Model: "gpt-test", TokenUsage: accounting.TokenUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}}}, TotalTokens: 5,
		},
		ProjectIDs: []string{"project-b", "project-a"},
	}
	values := []any{
		JSONLSourceLocation{}, SourceLocation{}, FrozenBoundary{}, SourceRecord{}, SourceRef{}, ObservationKey{}, ObservationRevision{},
		Diagnostic{}, DerivedRecord{}, ObservationSummary{}, SessionView{}, ProbeFile{}, ProjectProbeState{}, ProbeCheck{}, TerminalCounts{},
		SessionViewDependency{}, StateSnapshot{}, AssociatedUsage{}, ProjectView{}, GenerationManifest{},
		SessionViewIdentity{}, ProjectProbeStateIdentity{}, ProjectViewIdentity{},
		source, key, observation, validObservationSummary(), validSessionView(), validProbeState(), validProbeCheck(), validProjectView(), validGenerationManifest(),
		map[string]any{
			"多字节<&":   "值\u2028\u2029<&>",
			"control": "\x00\b\f\n\r\t\\\"",
			"numbers": []any{int64(-9), uint64(17)},
			"bytes":   []byte{0, 1, 2, 253, 254, 255},
			"nested":  map[string]string{"z": "last", "a": "first"},
		},
		&struct {
			Visible string  `json:"visible"`
			Empty   string  `json:"empty,omitempty"`
			Value   *string `json:"value,omitempty"`
		}{Visible: "yes"},
	}
	for index, value := range values {
		t.Run(fmt.Sprintf("value-%02d-%T", index, value), func(t *testing.T) {
			wantDigest, err := legacyDigestForTest(value)
			if err != nil {
				t.Fatal(err)
			}
			gotDigest, err := DigestContext(context.Background(), value)
			if err != nil || gotDigest != wantDigest {
				t.Fatalf("DigestContext=%q,%v want %q", gotDigest, err, wantDigest)
			}
			wantJSON, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var gotJSON bytes.Buffer
			if err := WriteCanonicalJSONContext(context.Background(), &gotJSON, value); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotJSON.Bytes(), wantJSON) {
				t.Fatalf("canonical JSON mismatch\n got: %s\nwant: %s", gotJSON.Bytes(), wantJSON)
			}
		})
	}
}

func TestStreamingDigestDifferentialProperty(t *testing.T) {
	random := rand.New(rand.NewSource(20260901))
	for iteration := 0; iteration < 128; iteration++ {
		values := make([]string, random.Intn(96))
		mappingCount := random.Intn(96)
		mapping := make(map[string]any, mappingCount)
		for index := range values {
			values[index] = fmt.Sprintf("值-%d-%d-\u2028-<&", iteration, random.Int63())
		}
		for index := 0; index < mappingCount; index++ {
			mapping[fmt.Sprintf("键-%03d-%d", index, random.Int63())] = []any{random.Int63(), random.Uint64(), values}
		}
		value := struct {
			ProjectIDs []string       `json:"project_ids"`
			Mapping    map[string]any `json:"mapping"`
		}{ProjectIDs: values, Mapping: mapping}
		want, err := legacyDigestForTest(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DigestContext(context.Background(), value)
		if err != nil || got != want {
			t.Fatalf("iteration %d digest=%q,%v want %q", iteration, got, err, want)
		}
	}
}

func legacyDigestForTest(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var copied any
	if err := decoder.Decode(&copied); err != nil {
		return "", err
	}
	normalized := legacyNormalizeForTest(copied, "")
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func legacyNormalizeForTest(value any, field string) any {
	switch current := value.(type) {
	case nil:
		if _, unordered := unorderedJSONArrays[field]; unordered {
			return []any{}
		}
		if _, unordered := unorderedJSONObjects[field]; unordered {
			return map[string]any{}
		}
		return nil
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, child := range current {
			result[key] = legacyNormalizeForTest(child, key)
		}
		return result
	case []any:
		result := make([]any, len(current))
		for index := range current {
			result[index] = legacyNormalizeForTest(current[index], "")
		}
		if _, unordered := unorderedJSONArrays[field]; unordered {
			sort.Slice(result, func(i, j int) bool {
				left, _ := json.Marshal(result[i])
				right, _ := json.Marshal(result[j])
				return bytes.Compare(left, right) < 0
			})
		}
		return result
	default:
		return current
	}
}

func TestContextValidatorsKeepSemanticErrorsUntilCancellationOccurs(t *testing.T) {
	value := validSessionView()
	value.SchemaVersion = 0
	want := "schema_version must be exactly 1"
	if err := ValidateSessionViewContext(context.Background(), value); err == nil || err.Error() != want {
		t.Fatalf("semantic error=%v want %q", err, want)
	}

	cause := errors.New("cancel invalid validation")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	if err := ValidateSessionViewContext(ctx, value); !errors.Is(err, cause) {
		t.Fatalf("cancelled validation error=%v want cause", err)
	}
}
