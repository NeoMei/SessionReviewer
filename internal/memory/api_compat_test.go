package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

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
	names := []string{"review-presentation-v4", "machine-ledger-v4", "session-index-v1", "session-summary-v1", "session-event-page-v1", "agent-annotation-v1", "pricing-snapshot-v1", "pricing-supplement-v1", "conversation-chain-v1", "problem-map-candidate-v1"}
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

func TestConversationPageSchemaAcceptsOnlyLegacyAndSnapshotQualifiedCapabilities(t *testing.T) {
	schema := readContractJSON(t, filepath.Join("..", "..", "schemas", "conversation-page-v1.schema.json"))
	for _, readerVersion := range []string{"0.4.0", "0.4.3"} {
		page := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "conversation-page-v1.valid.json")).(map[string]any)
		page["minimum_reader_version"] = readerVersion
		if err := validateContractSchema(schema, page, "$", schema); err != nil {
			t.Fatalf("conversation page schema rejected reader %s: %v", readerVersion, err)
		}
	}
	future := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "conversation-page-v1.valid.json")).(map[string]any)
	future["minimum_reader_version"] = "0.4.4"
	if err := validateContractSchema(schema, future, "$", schema); err == nil {
		t.Fatal("conversation page schema accepted unsupported future capability")
	}
}

func TestSessionEventPageSchemaRejectsEmptyCursorStrings(t *testing.T) {
	schema := readContractJSON(t, filepath.Join("..", "..", "schemas", "session-event-page-v1.schema.json"))
	valid := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "session-event-page-v1.valid.json"))
	if err := validateContractSchema(schema, valid, "$", schema); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
	for _, key := range []string{"previous_cursor", "next_cursor", "first_cursor", "last_cursor"} {
		t.Run(key, func(t *testing.T) {
			invalid := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "session-event-page-v1.valid.json")).(map[string]any)
			invalid[key] = ""
			if err := validateContractSchema(schema, invalid, "$", schema); err == nil {
				t.Fatalf("schema accepted empty %s", key)
			}
		})
	}
}

func TestExpandedV4SchemasEnforceRevisionAndSafeIntegerBoundaries(t *testing.T) {
	reviewSchema := readContractJSON(t, filepath.Join("..", "..", "schemas", "review-presentation-v4.schema.json"))
	review := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "review-presentation-v4.valid.json")).(map[string]any)
	review["problem_nodes"] = []any{map[string]any{
		"id": "problem-1", "question": "Why?", "primary_parent_id": nil, "related_node_ids": []any{},
		"workflow_state": "not_started", "answer_state": "no_answer", "completion_criterion": "", "current_conclusion": "",
		"source_turn_refs": []any{}, "provenance": "human_created", "first_proposed_at": "2026-09-04T00:00:00Z",
		"sibling_order": json.Number("0"), "confirmed_at": nil, "revision": json.Number("1"),
	}}
	review["problem_root_ids"] = []any{"problem-1"}
	if err := validateContractSchema(reviewSchema, review, "$", reviewSchema); err == nil {
		t.Fatal("review schema accepted a non-empty problem map at revision zero")
	}

	chainSchema := readContractJSON(t, filepath.Join("..", "..", "schemas", "conversation-chain-v1.schema.json"))
	chain := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "conversation-chain-v1.valid.json")).(map[string]any)
	chain["coverage"].(map[string]any)["source_messages"] = json.Number("9007199254740992")
	if err := validateContractSchema(chainSchema, chain, "$", chainSchema); err == nil {
		t.Fatal("conversation schema accepted an integer above the JavaScript safe maximum")
	}
	proofChain := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "conversation-chain-v1.proof.valid.json")).(map[string]any)
	if err := validateContractSchema(chainSchema, proofChain, "$", chainSchema); err != nil {
		t.Fatalf("conversation schema rejected valid dependency proof: %v", err)
	}
	proofChain["dependency_proof_v1"].(map[string]any)["visible_records"].([]any)[0].(map[string]any)["record_ordinal"] = json.Number("0")
	if err := validateContractSchema(chainSchema, proofChain, "$", chainSchema); err == nil {
		t.Fatal("conversation schema accepted a zero dependency proof ordinal")
	}
	coverageChain := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "conversation-chain-v1.coverage.valid.json")).(map[string]any)
	if err := validateContractSchema(chainSchema, coverageChain, "$", chainSchema); err != nil {
		t.Fatalf("conversation schema rejected valid materialization coverage: %v", err)
	}
	coverageChain["materialization_coverage_v1"].(map[string]any)["raw_source"] = "forbidden"
	if err := validateContractSchema(chainSchema, coverageChain, "$", chainSchema); err == nil {
		t.Fatal("conversation schema accepted raw source material in coverage")
	}

	candidateSchema := readContractJSON(t, filepath.Join("..", "..", "schemas", "problem-map-candidate-v1.schema.json"))
	candidates := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "problem-map-candidate-v1.valid.json")).(map[string]any)
	candidates["candidates"].([]any)[0].(map[string]any)["revision"] = json.Number("9007199254740992")
	if err := validateContractSchema(candidateSchema, candidates, "$", candidateSchema); err == nil {
		t.Fatal("candidate schema accepted a revision above the JavaScript safe maximum")
	}
}

func TestHistoricalSourceTurnSchemaCapabilities(t *testing.T) {
	qualified := map[string]any{
		"provider": "codex", "session_id": "session-1", "turn_unit_id": "turn-1",
		"session_view_digest": "sha256:" + strings.Repeat("1", 64),
	}

	reviewSchema := readContractJSON(t, filepath.Join("..", "..", "schemas", "review-presentation-v4.schema.json"))
	review := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "review-presentation-v4.valid.json")).(map[string]any)
	review["minimum_reader_version"], review["minimum_writer_version"] = "0.4.3", "0.4.3"
	review["problem_map_revision"], review["problem_root_ids"] = json.Number("1"), []any{"problem-1"}
	review["problem_nodes"] = []any{map[string]any{
		"id": "problem-1", "question": "Why?", "primary_parent_id": nil, "related_node_ids": []any{},
		"workflow_state": "not_started", "answer_state": "no_answer", "completion_criterion": "", "current_conclusion": "",
		"source_turn_refs": []any{qualified}, "provenance": "human_created", "first_proposed_at": "2026-09-09T00:00:00Z",
		"sibling_order": json.Number("0"), "confirmed_at": nil, "revision": json.Number("1"),
	}}
	if err := validateContractSchema(reviewSchema, review, "$", reviewSchema); err != nil {
		t.Fatalf("review schema rejected qualified 0.4.3 reference: %v", err)
	}
	review["minimum_reader_version"], review["minimum_writer_version"] = "0.4.0", "0.4.0"
	if err := validateContractSchema(reviewSchema, review, "$", reviewSchema); err == nil {
		t.Fatal("review schema accepted qualified reference at 0.4.0")
	}

	candidateSchema := readContractJSON(t, filepath.Join("..", "..", "schemas", "problem-map-candidate-v1.schema.json"))
	candidate := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "problem-map-candidate-v1.valid.json")).(map[string]any)
	candidate["minimum_reader_version"] = "0.4.3"
	candidate["candidates"].([]any)[0].(map[string]any)["source_turn_refs"] = []any{qualified}
	if err := validateContractSchema(candidateSchema, candidate, "$", candidateSchema); err != nil {
		t.Fatalf("candidate schema rejected qualified 0.4.3 reference: %v", err)
	}
	candidate["minimum_reader_version"] = "0.4.0"
	if err := validateContractSchema(candidateSchema, candidate, "$", candidateSchema); err == nil {
		t.Fatal("candidate schema accepted qualified reference at 0.4.0")
	}
}

func TestHistoricalSourceTurnSchemaCapabilityReciprocity(t *testing.T) {
	reviewSchema := readContractJSON(t, filepath.Join("..", "..", "schemas", "review-presentation-v4.schema.json"))
	legacyView := "sha256:" + strings.Repeat("1", 64)
	historicalView := "sha256:" + strings.Repeat("2", 64)

	newReview := func(reader, writer string, dependencies []any) map[string]any {
		review := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "review-presentation-v4.valid.json")).(map[string]any)
		review["minimum_reader_version"], review["minimum_writer_version"] = reader, writer
		review["chain_dependencies"] = dependencies
		return review
	}
	dependency := func(sessionID, view, dependency string) map[string]any {
		return map[string]any{
			"provider": "codex", "session_id": sessionID, "session_view_digest": view,
			"dependency_digest": dependency, "turn_unit_ids": []any{"turn-1"},
		}
	}
	legacyDependency := dependency("session-1", legacyView, "sha256:"+strings.Repeat("3", 64))
	historicalDependency := dependency("session-1", historicalView, "sha256:"+strings.Repeat("4", 64))

	t.Run("multi-view unqualified data requires 0.4.3", func(t *testing.T) {
		dependencies := []any{legacyDependency, historicalDependency}
		legacyFloor := newReview("0.4.0", "0.4.0", dependencies)
		if err := validateContractSchema(reviewSchema, legacyFloor, "$", reviewSchema); err != nil {
			t.Fatalf("structural schema rejected sound multi-view shape at its structural 0.4.0 lower bound: %v", err)
		}
		historicalFloor := newReview("0.4.3", "0.4.3", dependencies)
		if err := validateContractSchema(reviewSchema, historicalFloor, "$", reviewSchema); err != nil {
			t.Fatalf("schema rejected valid same-provider/session multi-view data at 0.4.3: %v", err)
		}
	})

	t.Run("zero and single-view unqualified data keep 0.4.0", func(t *testing.T) {
		for name, dependencies := range map[string][]any{"zero": {}, "single": {legacyDependency}} {
			t.Run(name, func(t *testing.T) {
				legacyFloor := newReview("0.4.0", "0.4.0", dependencies)
				if err := validateContractSchema(reviewSchema, legacyFloor, "$", reviewSchema); err != nil {
					t.Fatalf("legacy data rejected at 0.4.0: schema=%v", err)
				}
				uplift := newReview("0.4.3", "0.4.3", dependencies)
				if err := validateContractSchema(reviewSchema, uplift, "$", reviewSchema); err == nil {
					t.Fatal("review schema accepted unqualified 0.4.3 with fewer than two dependencies")
				}
			})
		}
	})

	t.Run("distinct sessions remain legacy despite two structural dependencies", func(t *testing.T) {
		dependencies := []any{legacyDependency, dependency("session-2", historicalView, "sha256:"+strings.Repeat("4", 64))}
		legacyFloor := newReview("0.4.0", "0.4.0", dependencies)
		if err := validateContractSchema(reviewSchema, legacyFloor, "$", reviewSchema); err != nil {
			t.Fatalf("distinct-session legacy data rejected at 0.4.0: schema=%v", err)
		}
		uplift := newReview("0.4.3", "0.4.3", dependencies)
		if err := validateContractSchema(reviewSchema, uplift, "$", reviewSchema); err != nil {
			t.Fatalf("structural schema must leave arbitrary cross-item grouping to runtime: %v", err)
		}
	})

	ledgerSchema := readContractJSON(t, filepath.Join("..", "..", "schemas", "machine-ledger-v4.schema.json"))
	newLedger := func(review map[string]any, reader, writer string) map[string]any {
		ledger := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "machine-ledger-v4.valid.json")).(map[string]any)
		ledger["minimum_reader_version"], ledger["minimum_writer_version"] = reader, writer
		ledger["document_projection"] = map[string]any{
			"schema_version": json.Number("1"), "format": "review-markdown-v1", "presentation_base": review,
		}
		return ledger
	}

	t.Run("ledger propagates multi-view requirement", func(t *testing.T) {
		dependencies := []any{legacyDependency, historicalDependency}
		legacyReview := newReview("0.4.0", "0.4.0", dependencies)
		legacyLedger := newLedger(legacyReview, "0.4.1", "0.4.1")
		if err := validateContractSchema(ledgerSchema, legacyLedger, "$", ledgerSchema); err != nil {
			t.Fatalf("ledger structural schema rejected sound nested lower-bound shape: %v", err)
		}
		historicalReview := newReview("0.4.3", "0.4.3", dependencies)
		historicalLedger := newLedger(historicalReview, "0.4.3", "0.4.3")
		if err := validateContractSchema(ledgerSchema, historicalLedger, "$", ledgerSchema); err != nil {
			t.Fatalf("valid nested historical ledger rejected: schema=%v", err)
		}
	})

	t.Run("ledger keeps zero and single dependency projections at 0.4.1", func(t *testing.T) {
		for name, dependencies := range map[string][]any{"zero": {}, "single": {legacyDependency}} {
			t.Run(name, func(t *testing.T) {
				legacyReview := newReview("0.4.0", "0.4.0", dependencies)
				legacyLedger := newLedger(legacyReview, "0.4.1", "0.4.1")
				if err := validateContractSchema(ledgerSchema, legacyLedger, "$", ledgerSchema); err != nil {
					t.Fatalf("legacy nested projection rejected: schema=%v", err)
				}
				upliftReview := newReview("0.4.3", "0.4.3", dependencies)
				upliftLedger := newLedger(upliftReview, "0.4.3", "0.4.3")
				if err := validateContractSchema(ledgerSchema, upliftLedger, "$", ledgerSchema); err == nil {
					t.Fatal("ledger schema accepted unqualified 0.4.3 projection with fewer than two dependencies")
				}
			})
		}
	})

	t.Run("ledger leaves distinct-session grouping to runtime", func(t *testing.T) {
		dependencies := []any{legacyDependency, dependency("session-2", historicalView, "sha256:"+strings.Repeat("4", 64))}
		legacyReview := newReview("0.4.0", "0.4.0", dependencies)
		legacyLedger := newLedger(legacyReview, "0.4.1", "0.4.1")
		if err := validateContractSchema(ledgerSchema, legacyLedger, "$", ledgerSchema); err != nil {
			t.Fatalf("distinct-session legacy ledger rejected: %v", err)
		}
		upliftReview := newReview("0.4.3", "0.4.3", dependencies)
		upliftLedger := newLedger(upliftReview, "0.4.3", "0.4.3")
		if err := validateContractSchema(ledgerSchema, upliftLedger, "$", ledgerSchema); err != nil {
			t.Fatalf("structural ledger schema must leave arbitrary cross-item grouping to runtime: %v", err)
		}
	})
}

func TestSessionSummarySchemaRequiresNonemptyUniqueSources(t *testing.T) {
	schema := readContractJSON(t, filepath.Join("..", "..", "schemas", "session-summary-v1.schema.json"))
	summary := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "session-summary-v1.valid.json")).(map[string]any)
	block := summary["key_operations"].(map[string]any)
	block["total"], block["shown"] = json.Number("1"), json.Number("1")
	block["coverage"] = map[string]any{
		"seen": json.Number("1"), "indexed": json.Number("1"), "collapsed": json.Number("0"),
		"unprojected": json.Number("0"), "undecodable": json.Number("0"), "truncated": json.Number("0"),
	}
	entry := map[string]any{
		"occurred_at": "2026-09-08T00:00:00Z", "sequence": json.Number("1"), "revision_id": "revision-1",
		"text": "safe", "source_revision_ids": []any{},
	}
	block["items"] = []any{entry}
	if err := validateContractSchema(schema, summary, "$", schema); err == nil {
		t.Fatal("summary schema accepted an empty source revision array")
	}
	entry["source_revision_ids"] = []any{"source-1", "source-1"}
	if err := validateContractSchema(schema, summary, "$", schema); err == nil {
		t.Fatal("summary schema accepted duplicate source revisions")
	}
}

func TestV4SchemasBoundEveryPersistedIntegerToJavaScriptSafeMaximum(t *testing.T) {
	names := []string{"review-presentation-v4", "machine-ledger-v4", "session-index-v1", "session-summary-v1", "session-event-page-v1", "agent-annotation-v1", "pricing-snapshot-v1", "pricing-supplement-v1", "conversation-chain-v1", "problem-map-candidate-v1"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			schema := readContractJSON(t, filepath.Join("..", "..", "schemas", name+".schema.json"))
			if err := validateIntegerSchemaMaximum(schema, "$"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func validateIntegerSchemaMaximum(value any, path string) error {
	object, ok := value.(map[string]any)
	if ok {
		integerType := object["type"] == "integer"
		if types, typesOK := object["type"].([]any); typesOK {
			for _, candidate := range types {
				integerType = integerType || candidate == "integer"
			}
		}
		if integerType {
			maximum, exists := object["maximum"].(json.Number)
			if !exists || numberFloat(maximum) > 9007199254740991 {
				return fmt.Errorf("%s: persisted integer is not bounded to the JavaScript safe maximum", path)
			}
		}
		for key, child := range object {
			if err := validateIntegerSchemaMaximum(child, path+"."+key); err != nil {
				return err
			}
		}
		return nil
	}
	if array, ok := value.([]any); ok {
		for index, child := range array {
			if err := validateIntegerSchemaMaximum(child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestV4SchemasAllowHonestUnknownSessionTimesAndLosslessLegacyDecisionStatus(t *testing.T) {
	indexSchema := readContractJSON(t, filepath.Join("..", "..", "schemas", "session-index-v1.schema.json"))
	index := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "session-index-v1.unknown.valid.json"))
	if err := validateContractSchema(indexSchema, index, "$", indexSchema); err != nil {
		t.Fatalf("schema rejected unknown session timestamps: %v", err)
	}
	if err := validateSessionIndexCoverage(index); err != nil {
		t.Fatalf("schema fixture coverage rejected unknown session timestamps: %v", err)
	}

	reviewSchema := readContractJSON(t, filepath.Join("..", "..", "schemas", "review-presentation-v4.schema.json"))
	review := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "review-presentation-v4.valid.json")).(map[string]any)
	legacy := map[string]any{
		"id": "legacy-decision", "kind": "decision", "occurred_at": "2026-08-25", "title": "Keep human status",
		"rationale": "preserve", "impact": "compatibility", "status": "legacy_unmapped", "legacy_status_text": "已采用",
		"reevaluate_when": "", "supersedes": []any{}, "milestone_ids": []any{}, "session_refs": []any{},
		"provenance": "migrated", "pinned": false, "revision": json.Number("1"),
	}
	review["decisions"] = []any{legacy}
	if err := validateContractSchema(reviewSchema, review, "$", reviewSchema); err != nil {
		t.Fatalf("schema rejected lossless legacy status: %v", err)
	}
	legacy["status"] = "active"
	if err := validateContractSchema(reviewSchema, review, "$", reviewSchema); err == nil {
		t.Fatal("schema accepted legacy status text on a native v4 decision")
	}
}

func TestV4ContractFixtureDecoderRejectsUnsafeBoundaries(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{name: "duplicate keys", body: []byte(`{"schema_version":1,"schema_version":1}`)},
		{name: "invalid UTF-8", body: []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}},
		{name: "oversized input", body: bytes.Repeat([]byte{' '}, maxContractInputBytes+1)},
		{name: "trailing JSON value", body: []byte(`{} {}`)},
		{name: "trailing garbage", body: []byte(`{} garbage`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseContractJSON(tc.body); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}

func TestPricingSnapshotCompleteRequiresResolvedAmounts(t *testing.T) {
	schema := readContractJSON(t, filepath.Join("..", "..", "schemas", "pricing-snapshot-v1.schema.json"))
	fixture := readContractJSON(t, filepath.Join("..", "..", "testdata", "contracts", "v4", "pricing-snapshot-v1.valid.json"))
	value := fixture.(map[string]any)
	value["pricing_complete"] = true
	if err := validateContractSchema(schema, value, "$", schema); err == nil {
		t.Fatal("complete snapshot with unknown amounts accepted")
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
	value, err := parseContractJSON(body)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return value
}

const maxContractInputBytes = 64 << 20

func parseContractJSON(body []byte) (any, error) {
	if len(body) > maxContractInputBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxContractInputBytes)
	}
	if !utf8.Valid(body) {
		return nil, fmt.Errorf("input is not valid UTF-8")
	}
	scan := json.NewDecoder(bytes.NewReader(body))
	if err := rejectDuplicateJSONKeys(scan); err != nil {
		return nil, err
	}
	return decodeContractJSON(body)
}

func rejectDuplicateJSONKeys(dec *json.Decoder) error {
	if err := scanJSONValue(dec, "$"); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON: %w", err)
	}
	return nil
}

func scanJSONValue(dec *json.Decoder, path string) error {
	token, err := dec.Token()
	if err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return fmt.Errorf("decode %s key: %w", path, err)
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("decode %s: object key is not a string", path)
				}
				if seen[key] {
					return fmt.Errorf("duplicate JSON key %q at %s", key, path)
				}
				seen[key] = true
				if err := scanJSONValue(dec, path+"."+key); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim('}') {
				return fmt.Errorf("decode %s: unterminated object", path)
			}
		case '[':
			index := 0
			for dec.More() {
				if err := scanJSONValue(dec, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
				index++
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim(']') {
				return fmt.Errorf("decode %s: unterminated array", path)
			}
		}
	}
	return nil
}

func decodeContractJSON(body []byte) (any, error) {
	if len(body) > maxContractInputBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxContractInputBytes)
	}
	if !utf8.Valid(body) {
		return nil, fmt.Errorf("input is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing JSON value")
		}
		return nil, fmt.Errorf("trailing JSON: %w", err)
	}
	return value, nil
}

func validateContractSchema(schema, value any, path string, root any) error {
	s, ok := schema.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: schema is not an object", path)
	}
	if ref, ok := s["$ref"].(string); ok {
		const prefix = "#/$defs/"
		if len(ref) >= len(prefix) && ref[:len(prefix)] == prefix {
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
		const externalPrefix = "https://sessionreviewer.local/schemas/"
		if len(ref) < len(externalPrefix) || ref[:len(externalPrefix)] != externalPrefix {
			return fmt.Errorf("%s: unsupported ref %q", path, ref)
		}
		externalBody, err := os.ReadFile(filepath.Join("..", "..", "schemas", filepath.Base(ref)))
		if err != nil {
			return fmt.Errorf("%s: read ref %q: %w", path, ref, err)
		}
		external, err := decodeContractJSON(externalBody)
		if err != nil {
			return fmt.Errorf("%s: decode ref %q: %w", path, ref, err)
		}
		return validateContractSchema(external, value, path, external)
	}
	if alternatives, ok := s["oneOf"].([]any); ok {
		matches := 0
		for _, alternative := range alternatives {
			if validateContractSchema(alternative, value, path, root) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s: oneOf matched %d alternatives", path, matches)
		}
	}
	if alternatives, ok := s["anyOf"].([]any); ok {
		matched := false
		for _, alternative := range alternatives {
			if validateContractSchema(alternative, value, path, root) == nil {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: anyOf did not match", path)
		}
	}
	if negated, ok := s["not"]; ok && validateContractSchema(negated, value, path, root) == nil {
		return fmt.Errorf("%s: forbidden schema matched", path)
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
		if min, ok := s["minItems"].(json.Number); ok && len(array) < int(numberInt(min)) {
			return fmt.Errorf("%s: too few items", path)
		}
		if max, ok := s["maxItems"].(json.Number); ok && len(array) > int(numberInt(max)) {
			return fmt.Errorf("%s: too many items", path)
		}
		if unique, ok := s["uniqueItems"].(bool); ok && unique {
			for index := range array {
				for previous := 0; previous < index; previous++ {
					if reflect.DeepEqual(array[previous], array[index]) {
						return fmt.Errorf("%s: duplicate item", path)
					}
				}
			}
		}
		if items, ok := s["items"]; ok {
			for index, child := range array {
				if err := validateContractSchema(items, child, fmt.Sprintf("%s[%d]", path, index), root); err != nil {
					return err
				}
			}
		}
		if contains, ok := s["contains"]; ok {
			matched := false
			for index, child := range array {
				if validateContractSchema(contains, child, fmt.Sprintf("%s[%d]", path, index), root) == nil {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("%s: contains did not match", path)
			}
		}
	}
	if conditions, ok := s["allOf"].([]any); ok {
		for _, condition := range conditions {
			branch, ok := condition.(map[string]any)
			if !ok {
				return fmt.Errorf("%s: allOf entry is not an object", path)
			}
			if ifSchema, ok := branch["if"]; ok {
				if validateContractSchema(ifSchema, value, path, root) == nil {
					if thenSchema, ok := branch["then"]; ok {
						if err := validateContractSchema(thenSchema, value, path, root); err != nil {
							return err
						}
					}
				} else if elseSchema, ok := branch["else"]; ok {
					if err := validateContractSchema(elseSchema, value, path, root); err != nil {
						return err
					}
				}
			} else if err := validateContractSchema(branch, value, path, root); err != nil {
				return err
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
	calculatedStates := map[string]int64{"complete": 0, "partial": 0, "error": 0, "unprocessed": 0}
	calculatedSources := map[string]int64{"available": 0, "unavailable": 0}
	var startedKnown, endedKnown, usageKnown int64
	for _, item := range sessions {
		session, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("session entry is not an object")
		}
		state, stateOK := session["processing_state"].(string)
		availability, availabilityOK := session["source_availability"].(string)
		if !stateOK || !availabilityOK {
			return fmt.Errorf("session state or availability is not a string")
		}
		calculatedStates[state]++
		calculatedSources[availability]++
		if session["started_at"] != nil {
			startedKnown++
		}
		if session["ended_at"] != nil {
			endedKnown++
		}
		if session["usage_record_digest"] != nil {
			usageKnown++
		}
	}
	claimedStarted, err := get("started_at_known")
	if err != nil {
		return err
	}
	claimedEnded, err := get("ended_at_known")
	if err != nil {
		return err
	}
	claimedUsage, err := get("usage_known")
	if err != nil {
		return err
	}
	if complete != calculatedStates["complete"] || partial != calculatedStates["partial"] || errCount != calculatedStates["error"] || unprocessed != calculatedStates["unprocessed"] ||
		available != calculatedSources["available"] || unavailable != calculatedSources["unavailable"] || claimedStarted != startedKnown || claimedEnded != endedKnown || claimedUsage != usageKnown ||
		complete+partial+errCount+unprocessed != total || available+unavailable != total || int64(len(sessions)) != total {
		return fmt.Errorf("coverage counts do not reconcile")
	}
	return nil
}
