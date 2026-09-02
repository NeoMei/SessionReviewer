package presentation

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRebasePreservesHumanSetWhenGeneratedValueChanges(t *testing.T) {
	old := NewScalarBaseline("project-overview", "status", "old")
	patch := Patch{
		EntityID: "project-overview", Field: "status", Operation: Set, Value: "人工结论",
		BaseGeneratedHash: old.GeneratedHash,
	}
	next := NewScalarBaseline("project-overview", "status", "new")
	result, err := Rebase([]Patch{patch}, []Baseline{next})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Active) != 1 || result.Active[0].Value != "人工结论" ||
		result.Active[0].BaseGeneratedHash != old.GeneratedHash {
		t.Fatalf("human set was not preserved exactly: %+v", result.Active)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != UnderlayChanged {
		t.Fatalf("underlay diagnostic=%+v", result.Diagnostics)
	}
}

func TestDeletedEntityRetainsUnattachedOrphan(t *testing.T) {
	patch := Patch{
		EntityID: "decision-old", Field: "title", Operation: Set, Value: "保留",
		BaseGeneratedHash: strings.Repeat("3", 64),
	}
	result, err := Rebase([]Patch{patch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Orphans) != 1 || !reflect.DeepEqual(result.Orphans[0], patch) || len(result.Active) != 0 {
		t.Fatalf("orphan result=%+v", result)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != OrphanPatch {
		t.Fatalf("orphan diagnostic=%+v", result.Diagnostics)
	}
}

func TestRebaseRejectsDuplicatesUnsupportedFieldsAndChangedContracts(t *testing.T) {
	base := NewScalarBaseline("project-overview", "status", "value")
	patch := Patch{EntityID: "project-overview", Field: "status", Operation: Set, Value: "value", BaseGeneratedHash: base.GeneratedHash}
	if _, err := Rebase([]Patch{patch, patch}, []Baseline{base}); err == nil {
		t.Fatal("duplicate patch identity accepted")
	}
	unsupported := Baseline{EntityID: "project-overview", Field: "custom", Kind: UnsupportedField}
	unsupported.GeneratedHash = baselineHash(unsupported)
	if _, err := Rebase([]Patch{{EntityID: "project-overview", Field: "custom", Operation: Set, BaseGeneratedHash: unsupported.GeneratedHash}}, []Baseline{unsupported}); err == nil {
		t.Fatal("unsupported field contract accepted")
	}
	list := NewListBaseline("project-overview", "status", []string{"value"})
	if _, err := Rebase([]Patch{{EntityID: "project-overview", Field: "status", Operation: Set, Value: "scalar", BaseGeneratedHash: list.GeneratedHash}}, []Baseline{list}); err == nil {
		t.Fatal("changed scalar/list field contract accepted")
	}
}

func TestApplyHumanPrecedenceSuppressRestoreDefaultAndIntentionalEmptySet(t *testing.T) {
	base := NewScalarBaseline("project-overview", "status", "自动状态")
	set := Patch{EntityID: "project-overview", Field: "status", Operation: Set, Value: "人工状态", BaseGeneratedHash: base.GeneratedHash}
	applied, err := Apply([]Patch{set}, []Baseline{base})
	if err != nil {
		t.Fatal(err)
	}
	if field := applied["project-overview\x00status"]; !field.Present || field.Value != "人工状态" {
		t.Fatalf("human set did not win: %+v", field)
	}

	empty := set
	empty.Value = ""
	applied, err = Apply([]Patch{empty}, []Baseline{base})
	if err != nil {
		t.Fatal(err)
	}
	if field := applied["project-overview\x00status"]; !field.Present || field.Value != "" {
		t.Fatalf("intentional empty set was not applied: %+v", field)
	}

	suppress := Patch{EntityID: "project-overview", Field: "status", Operation: Suppress, BaseGeneratedHash: base.GeneratedHash}
	applied, err = Apply([]Patch{suppress}, []Baseline{base})
	if err != nil {
		t.Fatal(err)
	}
	if field := applied["project-overview\x00status"]; field.Present {
		t.Fatalf("suppress did not hide field: %+v", field)
	}

	restore := Patch{EntityID: "project-overview", Field: "status", Operation: RestoreDefault, BaseGeneratedHash: base.GeneratedHash}
	applied, err = Apply([]Patch{restore}, []Baseline{base})
	if err != nil {
		t.Fatal(err)
	}
	if field := applied["project-overview\x00status"]; !field.Present || field.Value != base.Value || !field.RestoredDefault {
		t.Fatalf("restore default did not reveal generated value: %+v", field)
	}
}

func TestCaptureReconcilesCurrentHumanFieldsAndPreservesUnknownBytes(t *testing.T) {
	previousBase := NewScalarBaseline("project-overview", "status", "旧自动")
	prior := Patch{
		EntityID: "project-overview", Field: "status", Operation: Set, Value: "已确认",
		BaseGeneratedHash: previousBase.GeneratedHash,
	}
	unknown := map[string][]byte{
		"before": []byte("<!-- 前置自定义 -->\n"),
		"middle": []byte("自定义中间字节\n"),
		"after":  []byte("<!-- 后置自定义 -->\n"),
	}
	input := CaptureInput{
		PreviousPatches: []Patch{prior}, PreviousBaselines: []Baseline{previousBase},
		Fields: []FieldObservation{{
			EntityID: "project-overview", Field: "status", Present: true, Value: "已确认",
		}},
		UnknownBlocks: unknown,
	}
	result, err := Capture(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Patches) != 1 || !reflect.DeepEqual(result.Patches[0], prior) {
		t.Fatalf("unchanged human value lost prior patch identity: %+v", result.Patches)
	}
	for key, body := range unknown {
		if !bytes.Equal(body, result.UnknownBlocks[key]) {
			t.Fatalf("unknown block %q changed", key)
		}
	}
	if &unknown["before"][0] == &result.UnknownBlocks["before"][0] {
		t.Fatal("unknown preservation map was not copied")
	}

	input.Fields[0].Value = "新人工结论"
	result, err = Capture(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Patches) != 1 || result.Patches[0].Value != "新人工结论" ||
		result.Patches[0].BaseGeneratedHash != previousBase.GeneratedHash {
		t.Fatalf("changed human value was not captured: %+v", result.Patches)
	}

	input.Fields[0].Present = false
	result, err = Capture(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Patches) != 1 || result.Patches[0].Operation != Suppress {
		t.Fatalf("removed generated field was not suppressed: %+v", result.Patches)
	}

	input.Fields[0].Present = true
	input.Fields[0].Value = "自动恢复"
	input.Fields[0].Intent = RestoreDefault
	result, err = Capture(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Patches) != 1 || result.Patches[0].Operation != RestoreDefault {
		t.Fatalf("explicit restore was not captured: %+v", result.Patches)
	}
}

func TestCaptureRejectsUnknownSupportedFieldAndDuplicateInput(t *testing.T) {
	input := CaptureInput{
		Fields: []FieldObservation{{EntityID: "project-overview", Field: "custom", Present: true, Value: "x"}},
	}
	if _, err := Capture(input); err == nil {
		t.Fatal("field without prior baseline contract accepted")
	}
	base := NewScalarBaseline("project-overview", "status", "x")
	input = CaptureInput{
		PreviousBaselines: []Baseline{base, base},
		Fields:            []FieldObservation{{EntityID: "project-overview", Field: "status", Present: true, Value: "x"}},
	}
	if _, err := Capture(input); err == nil || !errors.Is(err, ErrDuplicateBaseline) {
		t.Fatalf("duplicate baseline accepted: %v", err)
	}
}

func TestCapturePreservesPriorOrphanAndRejectsInvalidMarkerIntent(t *testing.T) {
	orphan := Patch{
		EntityID: "decision-gone", Field: "title", Operation: Set, Value: "旧决策",
		BaseGeneratedHash: strings.Repeat("a", 64),
	}
	result, err := Capture(CaptureInput{PreviousPatches: []Patch{orphan}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Patches) != 1 || !reflect.DeepEqual(result.Patches[0], orphan) {
		t.Fatalf("prior orphan was discarded: %+v", result.Patches)
	}
	base := NewScalarBaseline("project-overview", "status", "value")
	_, err = Capture(CaptureInput{
		PreviousBaselines: []Baseline{base},
		Fields: []FieldObservation{{
			EntityID: "project-overview", Field: "status", Present: true, Value: "value", Intent: Set,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "marker intent") {
		t.Fatalf("invalid marker intent accepted: %v", err)
	}
}

func TestCaptureUnsuppressEqualBaselineDropsPatchButExplicitMarkersWin(t *testing.T) {
	base := NewScalarBaseline("project-overview", "status", "自动")
	prior := Patch{EntityID: base.EntityID, Field: base.Field, Operation: Suppress, BaseGeneratedHash: base.GeneratedHash}
	observation := FieldObservation{EntityID: base.EntityID, Field: base.Field, Present: true, Value: base.Value}
	result, err := Capture(CaptureInput{PreviousPatches: []Patch{prior}, PreviousBaselines: []Baseline{base}, Fields: []FieldObservation{observation}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Patches) != 0 {
		t.Fatalf("unsuppress equal to baseline froze a generated value: %+v", result.Patches)
	}

	observation.Intent = Suppress
	result, err = Capture(CaptureInput{PreviousPatches: []Patch{prior}, PreviousBaselines: []Baseline{base}, Fields: []FieldObservation{observation}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Patches) != 1 || result.Patches[0].Operation != Suppress {
		t.Fatalf("explicit suppress marker was not retained: %+v", result.Patches)
	}

	observation.Intent = RestoreDefault
	result, err = Capture(CaptureInput{PreviousPatches: []Patch{prior}, PreviousBaselines: []Baseline{base}, Fields: []FieldObservation{observation}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Patches) != 1 || result.Patches[0].Operation != RestoreDefault {
		t.Fatalf("restore equal to baseline was not retained: %+v", result.Patches)
	}
}

func TestCaptureRejectsObservationContractMismatchAtSource(t *testing.T) {
	base := NewListBaseline("event-a", "changes", []string{"a"})
	observation := FieldObservation{EntityID: base.EntityID, Field: base.Field, Present: true, Value: "scalar"}
	_, err := Capture(CaptureInput{PreviousBaselines: []Baseline{base}, Fields: []FieldObservation{observation}})
	if err == nil || !errors.Is(err, ErrChangedFieldContract) {
		t.Fatalf("observation contract mismatch was delayed: %v", err)
	}
}

func TestRebaseOutputOrderIsStableRegardlessOfInputOrder(t *testing.T) {
	first := NewScalarBaseline("project-a", "z", "1")
	second := NewScalarBaseline("project-a", "a", "2")
	firstPatch := Patch{EntityID: first.EntityID, Field: first.Field, Operation: Set, Value: "one", BaseGeneratedHash: first.GeneratedHash}
	secondPatch := Patch{EntityID: second.EntityID, Field: second.Field, Operation: Set, Value: "two", BaseGeneratedHash: second.GeneratedHash}
	left, err := Rebase([]Patch{firstPatch, secondPatch}, []Baseline{first, second})
	if err != nil {
		t.Fatal(err)
	}
	right, err := Rebase([]Patch{secondPatch, firstPatch}, []Baseline{second, first})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("rebase output depends on input order\nleft=%+v\nright=%+v", left, right)
	}
}
