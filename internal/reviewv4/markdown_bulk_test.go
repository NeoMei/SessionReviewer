package reviewv4

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/neomei/SessionReviewer/internal/baselinehash"
)

func TestApplyMarkdownEditsAtMaximumFieldCapacity(t *testing.T) {
	base, edits := bulkMarkdownPresentation(65536)
	before := clonePresentation(base)

	next, err := ApplyMarkdownEdits(base, edits)
	if err != nil {
		t.Fatal(err)
	}
	if next.Revision != base.Revision+1 || len(next.GeneratedBaselines) != 65536 || len(next.HumanPatches) != 65536 {
		t.Fatalf("maximum edit incomplete: revision=%d baselines=%d patches=%d", next.Revision, len(next.GeneratedBaselines), len(next.HumanPatches))
	}
	for _, index := range []int{0, 32768, 65535} {
		baseline := next.GeneratedBaselines[index]
		patch := next.HumanPatches[index]
		if baseline.Value == nil || patch.Value == nil {
			t.Fatalf("edited field %d lost scalar metadata: baseline=%+v patch=%+v", index, baseline, patch)
		}
		wantHash := baselinehash.SHA256(baseline.EntityID, baseline.Field, baseline.Kind, *baseline.Value, nil)
		if next.Decisions[index].Title != edits[index].After || next.Decisions[index].Revision != 2 || *patch.Value != edits[index].After || baseline.GeneratedHash != wantHash || patch.BaseGeneratedHash != wantHash {
			t.Fatalf("edited field %d not preserved: decision=%+v patch=%+v", index, next.Decisions[index], next.HumanPatches[index])
		}
	}
	if !reflect.DeepEqual(base, before) {
		t.Fatal("maximum edit mutated its input")
	}
	carried := clonePresentation(next)
	if err := CarryMarkdownGeneratedBaselines(&carried, "generation-next"); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{0, 32768, 65535} {
		if carried.GeneratedBaselines[index].GenerationID != "generation-next" || carried.GeneratedBaselines[index].GeneratedHash != next.GeneratedBaselines[index].GeneratedHash || !reflect.DeepEqual(carried.HumanPatches[index], next.HumanPatches[index]) {
			t.Fatalf("maximum carry changed metadata binding at %d: baseline=%+v patch=%+v", index, carried.GeneratedBaselines[index], carried.HumanPatches[index])
		}
	}
}

func TestApplyMarkdownEditsMaintainsMetadataIndexAcrossRemoveUpdateAndAppend(t *testing.T) {
	base, _ := bulkMarkdownPresentation(3)
	before := clonePresentation(base)
	edits := []FieldEdit{
		{Key: FieldKey{Entity: "decision:decision-000000", Name: "title"}, Before: "human title 0", After: "generated title 0"},
		{Key: FieldKey{Entity: "decision:decision-000002", Name: "title"}, Before: "human title 2", After: "updated title 2"},
		{Key: FieldKey{Entity: "decision:decision-000001", Name: "rationale"}, Before: "rationale", After: "updated rationale"},
	}

	next, err := ApplyMarkdownEdits(base, edits)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.HumanPatches) != 3 || len(next.GeneratedBaselines) != 4 {
		t.Fatalf("metadata mutation incomplete: patches=%+v baselines=%+v", next.HumanPatches, next.GeneratedBaselines)
	}
	want := map[string]string{
		"decision:decision-000001\x00rationale": "updated rationale",
		"decision:decision-000001\x00title":     "human title 1",
		"decision:decision-000002\x00title":     "updated title 2",
	}
	for _, patch := range next.HumanPatches {
		if patch.Value == nil || want[patch.EntityID+"\x00"+patch.Field] != *patch.Value {
			t.Fatalf("unexpected patch after indexed mutations: %+v", patch)
		}
		delete(want, patch.EntityID+"\x00"+patch.Field)
	}
	if len(want) != 0 {
		t.Fatalf("missing patches after indexed mutations: %v", want)
	}
	if !reflect.DeepEqual(base, before) {
		t.Fatal("metadata mutations changed input")
	}
}

func BenchmarkApplyMarkdownEditsBulk(b *testing.B) {
	for _, size := range []int{128, 256, 512, 1024, 4096} {
		b.Run(fmt.Sprintf("fields=%d", size), func(b *testing.B) {
			base, edits := bulkMarkdownPresentation(size)
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				if _, err := ApplyMarkdownEdits(base, edits); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCarryMarkdownGeneratedBaselinesBulk(b *testing.B) {
	for _, size := range []int{128, 256, 512, 1024, 4096} {
		b.Run(fmt.Sprintf("fields=%d", size), func(b *testing.B) {
			base, _ := bulkMarkdownPresentation(size)
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				next := clonePresentation(base)
				if err := CarryMarkdownGeneratedBaselines(&next, "generation-next"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func bulkMarkdownPresentation(size int) (Presentation, []FieldEdit) {
	presentation := minimumPresentation()
	presentation.Decisions = make([]Decision, size)
	presentation.GeneratedBaselines = make([]Baseline, size)
	presentation.HumanPatches = make([]Patch, size)
	edits := make([]FieldEdit, size)
	for index := 0; index < size; index++ {
		id := fmt.Sprintf("decision-%06d", index)
		entityID := "decision:" + id
		generated := fmt.Sprintf("generated title %d", index)
		human := fmt.Sprintf("human title %d", index)
		hash := baselinehash.SHA256(entityID, "title", "scalar", generated, nil)
		presentation.Decisions[index] = Decision{
			ID: id, Kind: "decision", OccurredAt: "2026-09-05", Title: human,
			Rationale: "rationale", Impact: "impact", Status: DecisionActive,
			ReevaluateWhen: "later", Supersedes: []string{}, MilestoneIDs: []string{},
			SessionRefs: []SessionRef{}, Provenance: "human_created", Revision: 1,
		}
		presentation.GeneratedBaselines[index] = Baseline{
			GenerationID: presentation.GenerationID, EntityID: entityID, Field: "title",
			Kind: "scalar", Value: &generated, GeneratedHash: hash,
		}
		presentation.HumanPatches[index] = Patch{
			EntityID: entityID, Field: "title", Operation: "set", Value: &human,
			BaseGeneratedHash: hash,
		}
		edits[index] = FieldEdit{
			Key: FieldKey{Entity: entityID, Name: "title"}, Before: human,
			After: fmt.Sprintf("edited title %d", index),
		}
	}
	return presentation, edits
}
