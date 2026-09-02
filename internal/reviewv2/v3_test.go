package reviewv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchemaV3RequiresMinimumWriterAndGeneration(t *testing.T) {
	root := writeV3Fixture(t)
	accepted, err := LoadV3(root)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.State.Machine.MinimumWriterVersion != MinimumWriterVersion ||
		accepted.State.Machine.GenerationID != "generation-3f00000000000001" ||
		accepted.State.Machine.ProjectViewDigest != strings.Repeat("a", 64) {
		t.Fatalf("machine identity=%+v", accepted.State.Machine)
	}
	if accepted.State.Review.GenerationID != accepted.State.Machine.GenerationID ||
		accepted.State.Review.MinimumWriterVersion != MinimumWriterVersion {
		t.Fatalf("review identity=%+v", accepted.State.Review)
	}
	if accepted.State.Events[0].GenerationID != accepted.State.Machine.GenerationID {
		t.Fatalf("history identity=%+v", accepted.State.Events[0])
	}
}

func TestV2WriterFailsClosedBeforeMutatingV3(t *testing.T) {
	root := writeV3Fixture(t)
	before := snapshotV3Tree(t, root)
	_, err := Load(root)
	if err == nil {
		t.Fatal("v2 write loader accepted schema 3")
	}
	var upgrade *ErrWriterUpgradeRequired
	if !errors.As(err, &upgrade) || upgrade.ProjectRoot != root {
		t.Fatalf("upgrade error=%T %v", err, err)
	}
	assertV3TreeEqual(t, before, snapshotV3Tree(t, root))
}

func TestV3SupportedHumanEditAndUnknownBlockArePresentationInput(t *testing.T) {
	root := writeV3Fixture(t)
	reviewPath := filepath.Join(root, filepath.FromSlash(ReviewRelativePath))
	source, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	baselineHash := hashV3ForTest(source)
	edited, err := PatchReviewUnit(source, EditUnit{
		Document: ReviewRelativePath, UnitID: "project-overview", Field: "status",
		Value: "人工状态", ExpectedSHA256: markdownSHA256(source),
	})
	if err != nil {
		t.Fatal(err)
	}
	edited = append(edited, []byte("\n<!-- custom-human-block -->\n自定义内容。\n")...)
	if err := os.WriteFile(reviewPath, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	accepted, err := LoadV3(root)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.State.Review.Status != "人工状态" || !bytes.Contains(edited, []byte("自定义内容。")) {
		t.Fatalf("human presentation input was rejected or lost: %+v", accepted.State.Review)
	}
	if hashV3ForTest(edited) == baselineHash || accepted.State.Machine.ReviewSHA256 != baselineHash {
		t.Fatalf("current human bytes and generated baseline hash were conflated: current=%s baseline=%s ledger=%s",
			hashV3ForTest(edited), baselineHash, accepted.State.Machine.ReviewSHA256)
	}
}

func TestV3FrontmatterRejectsDuplicateMinimumWriterAndRenderReparses(t *testing.T) {
	root := writeV3Fixture(t)
	reviewPath := filepath.Join(root, filepath.FromSlash(ReviewRelativePath))
	source, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	duplicated := bytes.Replace(source, []byte("minimum_writer_version: "+MinimumWriterVersion+"\n"),
		[]byte("minimum_writer_version: "+MinimumWriterVersion+"\nminimum_writer_version: 0.2.0\n"), 1)
	if bytes.Equal(duplicated, source) {
		t.Fatal("duplicate-writer mutation did not change fixture")
	}
	if err := os.WriteFile(reviewPath, duplicated, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadV3(root); err == nil || !strings.Contains(err.Error(), "duplicate YAML frontmatter key") {
		t.Fatalf("duplicate minimum writer accepted: %v", err)
	}

	state := v3FixtureState(t)
	review, err := RenderReviewV3(state.Review)
	if err != nil {
		t.Fatal(err)
	}
	reviewDoc, err := ParseReview(review)
	if err != nil || reviewDoc.Model.GenerationID != state.Review.GenerationID || reviewDoc.Model.MinimumWriterVersion != MinimumWriterVersion {
		t.Fatalf("rendered review did not reparse with v3 identity: %+v err=%v", reviewDoc.Model, err)
	}
	history, err := RenderHistoryV3(state.Review.ProjectID, state.Review.Revision, state.Review.GenerationID, state.Events)
	if err != nil {
		t.Fatal(err)
	}
	historyDoc, err := ParseHistory(history)
	if err != nil || historyDoc.GenerationID != state.Review.GenerationID || historyDoc.MinimumWriterVersion != MinimumWriterVersion {
		t.Fatalf("rendered history did not reparse with v3 identity: %+v err=%v", historyDoc, err)
	}
}

func TestMachineLedgerV3RejectsForgottenAndForgedIdentity(t *testing.T) {
	root := writeV3Fixture(t)
	body := readV3Ledger(t, root)
	valid, err := ParseMachineLedgerV3(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseMachineLedgerV3([]byte("{\"schema_version\":4}")); err == nil {
		t.Fatal("schema 4 accepted")
	}
	replacements := map[string]string{
		"missing writer":     "\"minimum_writer_version\": \"0.3.0\",",
		"missing generation": "\"generation_id\": \"generation-3f00000000000001\",",
		"unknown machine":    "\"schema_version\": 3,",
	}
	for name, old := range replacements {
		t.Run(name, func(t *testing.T) {
			forged := bytes.Replace(body, []byte(old), nil, 1)
			if bytes.Equal(forged, body) {
				t.Fatalf("test mutation %s did not change fixture", name)
			}
			if _, err := ParseMachineLedgerV3(forged); err == nil {
				t.Fatalf("%s accepted", name)
			}
		})
	}
	if valid.MinimumWriterVersion != "0.3.0" {
		t.Fatalf("writer=%q", valid.MinimumWriterVersion)
	}
}

func TestMachineLedgerV3RejectsInvalidPatchesAndBaselines(t *testing.T) {
	root := writeV3Fixture(t)
	body := readV3Ledger(t, root)
	mutations := []func(map[string]any){
		func(value map[string]any) {
			value["human_patches"] = []map[string]any{validPatchMap(), validPatchMap()}
		},
		func(value map[string]any) {
			patch := validPatchMap()
			patch["operation"] = "replace"
			value["human_patches"] = []map[string]any{patch}
		},
		func(value map[string]any) {
			patch := validPatchMap()
			patch["base_generated_hash"] = "nothash"
			value["human_patches"] = []map[string]any{patch}
		},
		func(value map[string]any) {
			baseline := validBaselineMap()
			baseline["generated_hash"] = 3
			value["generated_baselines"] = []map[string]any{baseline}
		},
	}
	for index, mutate := range mutations {
		t.Run(fmt.Sprintf("mutation %d", index), func(t *testing.T) {
			value := decodeV3Map(t, body)
			mutate(value)
			forged := encodeV3Map(t, value)
			if _, err := ParseMachineLedgerV3(forged); err == nil {
				t.Fatalf("invalid patch/baseline set %d accepted", index)
			}
		})
	}
}

func TestMachineLedgerV3RejectsUnknownFieldsLowWritersAndForgedBaselines(t *testing.T) {
	root := writeV3Fixture(t)
	body := readV3Ledger(t, root)
	value := decodeV3Map(t, body)
	value["unknown_machine_field"] = "forbidden"
	if _, err := ParseMachineLedgerV3(encodeV3Map(t, value)); err == nil {
		t.Fatal("unknown machine field accepted")
	}
	value = decodeV3Map(t, body)
	value["minimum_writer_version"] = "0.2.9"
	if _, err := ParseMachineLedgerV3(encodeV3Map(t, value)); err == nil {
		t.Fatal("writer below 0.3.0 accepted")
	}
	value = decodeV3Map(t, body)
	baseline := validBaselineMap()
	baseline["generated_hash"] = strings.Repeat("d", 64)
	value["generated_baselines"] = []map[string]any{baseline}
	if _, err := ParseMachineLedgerV3(encodeV3Map(t, value)); err == nil {
		t.Fatal("forged baseline hash accepted")
	}
	baseline["generation_id"] = "generation-other0000000001"
	baseline["generated_hash"] = strings.Repeat("e", 64)
	value["generated_baselines"] = []map[string]any{baseline}
	if _, err := ParseMachineLedgerV3(encodeV3Map(t, value)); err == nil {
		t.Fatal("baseline generation mismatch accepted")
	}
}

func TestMachineLedgerV3RequiresEveryPublicField(t *testing.T) {
	root := writeV3Fixture(t)
	body := readV3Ledger(t, root)
	for _, field := range []string{
		"schema_version", "minimum_writer_version", "project_id", "generation_id", "project_view_digest",
		"accepted_revision", "review_sha256", "history_sha256", "accounting", "sessions",
		"human_patches", "generated_baselines", "legacy_compatibility",
	} {
		t.Run(field, func(t *testing.T) {
			value := decodeV3Map(t, body)
			delete(value, field)
			if _, err := ParseMachineLedgerV3(encodeV3Map(t, value)); err == nil {
				t.Fatalf("missing required field %s accepted", field)
			}
		})
	}
}

func TestMachineLedgerV3SchemaMatchesContract(t *testing.T) {
	body := mustFixture(t, "../../schemas/review-projection-v3.schema.json")
	var schema map[string]any
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatal(err)
	}
	properties, _ := schema["properties"].(map[string]any)
	required := map[string]bool{}
	for _, raw := range schema["required"].([]any) {
		required[raw.(string)] = true
	}
	for _, name := range []string{
		"schema_version", "minimum_writer_version", "project_id", "generation_id", "project_view_digest",
		"accepted_revision", "review_sha256", "history_sha256", "accounting", "sessions",
		"human_patches", "generated_baselines", "legacy_compatibility",
	} {
		if properties[name] == nil || !required[name] {
			t.Fatalf("schema omits required %s", name)
		}
	}
	version, _ := properties["schema_version"].(map[string]any)
	writer, _ := properties["minimum_writer_version"].(map[string]any)
	if version["const"] != float64(SchemaVersion) || writer["const"] != MinimumWriterVersion {
		t.Fatalf("schema identity version=%+v writer=%+v", version, writer)
	}
	if strings.Contains(string(body), "review-ledger-v2.schema.json#") {
		t.Fatal("public v3 schema is not self-contained")
	}
}

func writeV3Fixture(t *testing.T) string {
	t.Helper()
	state := v3FixtureState(t)
	review, err := RenderReviewV3(state.Review)
	if err != nil {
		t.Fatal(err)
	}
	history, err := RenderHistoryV3(state.Review.ProjectID, state.Review.Revision, state.Review.GenerationID, state.Events)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := RenderMachineLedgerV3(MachineLedgerV3{
		SchemaVersion: SchemaVersion, MinimumWriterVersion: MinimumWriterVersion,
		ProjectID: state.Review.ProjectID, GenerationID: state.Review.GenerationID,
		ProjectViewDigest: strings.Repeat("a", 64), AcceptedRevision: state.Review.Revision,
		ReviewSHA256: hashV3ForTest(review), HistorySHA256: hashV3ForTest(history),
		Accounting: state.Machine.Accounting, Sessions: state.Machine.Sessions,
		HumanPatches: []HumanPatchWire{}, GeneratedBaselines: []GeneratedBaselineWire{},
		LegacyCompatibility: state.Machine.LegacyCompatibility,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for relative, body := range map[string][]byte{
		ReviewRelativePath: review, HistoryRelativePath: history, MachineLedgerRelativePath: ledger,
	} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func v3FixtureState(t *testing.T) State {
	t.Helper()
	state := validState(t)
	state.Review.Name = "Schema V3 Fixture"
	state.Review.Status = "生成状态"
	state.Review.Risks[0].Title = "Fixture risk"
	state.Review.Risks[0].Status = "open"
	state.Review.Risks[0].Detail = "Fixture detail"
	state.Review.Decisions[0].Title = "Fixture decision"
	state.Review.Decisions[0].Rationale = "Fixture rationale"
	state.Review.Decisions[0].Impact = "Fixture impact"
	state.Events[0].Title = "Fixture event"
	state.Events[0].OccurredAt = "2026-09-02T00:00:00Z"
	state.Events[0].Kind = "verification"
	state.Events[0].Meaning = "Fixture meaning"
	state.Events[0].Summary = "Fixture summary"
	state.Events[0].Why = "Fixture why"
	state.Events[0].Changes = []string{"Fixture change"}
	state.Events[0].Results = []string{"Fixture result"}
	state.Events[0].Next = "Fixture next"
	state.Review.GenerationID = "generation-3f00000000000001"
	state.Review.MinimumWriterVersion = MinimumWriterVersion
	for index := range state.Events {
		state.Events[index].GenerationID = state.Review.GenerationID
	}
	return state
}

func validPatchMap() map[string]any {
	return map[string]any{"entity_id": "project-overview", "field": "status", "operation": "set", "value": "human", "base_generated_hash": strings.Repeat("b", 64)}
}

func validBaselineMap() map[string]any {
	generationID := "generation-3f00000000000001"
	baseline := GeneratedBaselineWire{GenerationID: generationID, EntityID: "project-overview", Field: "status", Value: "generated"}
	return map[string]any{
		"generation_id": generationID, "entity_id": baseline.EntityID, "field": baseline.Field,
		"value": baseline.Value, "generated_hash": generatedBaselineHash(generationID, baseline),
	}
}

func readV3Ledger(t *testing.T, root string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(MachineLedgerRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func decodeV3Map(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func encodeV3Map(t *testing.T, value map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func hashV3ForTest(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%x", sum)
}

func snapshotV3Tree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	for _, relative := range []string{ReviewRelativePath, HistoryRelativePath, MachineLedgerRelativePath} {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		result[relative] = body
	}
	return result
}

func assertV3TreeEqual(t *testing.T, left, right map[string][]byte) {
	t.Helper()
	if len(left) != len(right) {
		t.Fatalf("tree size left=%d right=%d", len(left), len(right))
	}
	for name, body := range left {
		if !bytes.Equal(body, right[name]) {
			t.Fatalf("%s changed", name)
		}
	}
}
