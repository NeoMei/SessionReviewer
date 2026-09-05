package presentation

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestBaselineHashIsCanonicalAndContractAware(t *testing.T) {
	first := NewScalarBaseline("project-overview", "status", "same")
	second := NewScalarBaseline("project-overview", "status", "same")
	if first.GeneratedHash == "" || first.GeneratedHash != second.GeneratedHash {
		t.Fatalf("baseline hash is not deterministic: %+v %+v", first, second)
	}
	changed := NewScalarBaseline("project-overview", "status", "changed")
	if changed.GeneratedHash == first.GeneratedHash {
		t.Fatal("baseline hash ignores value")
	}
	list := NewListBaseline("event-a", "changes", []string{"a", "b"})
	reordered := NewListBaseline("event-a", "changes", []string{"b", "a"})
	if list.GeneratedHash == reordered.GeneratedHash {
		t.Fatal("ordered list baseline hash ignores order")
	}
	body, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "\"values\":[\"a\",\"b\"]") {
		t.Fatalf("baseline wire is not deterministic: %s", body)
	}
}

func TestBaselineHashCanonicalBytesRemainPinned(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"empty scalar", baselineHash(Baseline{EntityID: "decision:id", Field: "title", Kind: ScalarField}), "4b824643062f3cde4ed01998c9b420d1fe4b4d191d62bb44be7e3eb3f27ed9d1"},
		{"escaped scalar", baselineHash(Baseline{EntityID: "decision:id", Field: "title", Kind: ScalarField, Value: "line\n中文"}), "6bf2d1302a88ecff8e50e8a5e8f4ef3f03f40f5625f4e5a511d149fa51b5efe0"},
		{"nil list", baselineHash(Baseline{EntityID: "decision:id", Field: "tags", Kind: ListField}), "6f039a1de7e3de0cfad12f19134623d696b3a14e750f0e0822fad063471cd489"},
		{"empty list", baselineHash(Baseline{EntityID: "decision:id", Field: "tags", Kind: ListField, Values: []string{}}), "5a4520b41be588a3dd9e8ccfa69730daa22360907e29b42b6c691cc5cb6cc5d9"},
		{"ordered list", baselineHash(Baseline{EntityID: "decision:id", Field: "tags", Kind: ListField, Values: []string{"二", "one"}}), "66e34e9ff1e021802214cd47572dd43795379f7097b687621a51fa6185772dec"},
		{"reordered list", baselineHash(Baseline{EntityID: "decision:id", Field: "tags", Kind: ListField, Values: []string{"one", "二"}}), "c1d66c2f2608e936b0254783855da3dbbadc581b167a3cd5078e3108b05ec359"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("canonical hash = %s, want %s", tc.got, tc.want)
			}
		})
	}
}

func TestBaselineRejectsForgedGeneratedHash(t *testing.T) {
	value := NewScalarBaseline("project-overview", "status", "value")
	value.GeneratedHash = strings.Repeat("0", 64)
	if err := validateBaseline(value); err == nil {
		t.Fatal("forged generated baseline hash accepted")
	}
	list := NewListBaseline("event-a", "changes", []string{"a"})
	list.Values = append(list.Values, "forged")
	if err := validateBaseline(list); err == nil {
		t.Fatal("baseline hash did not bind ordered list values")
	}
}

func TestEmptyListPatchesAndBaselinesSurviveJSONRoundTrip(t *testing.T) {
	baseline := NewListBaseline("event-a", "changes", nil)
	encoded, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "\"values\":[]") {
		t.Fatalf("empty baseline list was omitted: %s", encoded)
	}
	var decodedBaseline Baseline
	if err := json.Unmarshal(encoded, &decodedBaseline); err != nil {
		t.Fatal(err)
	}
	if err := validateBaseline(decodedBaseline); err != nil || decodedBaseline.Values == nil || len(decodedBaseline.Values) != 0 {
		t.Fatalf("empty baseline did not round trip: value=%+v err=%v", decodedBaseline, err)
	}

	patch := Patch{
		EntityID: baseline.EntityID, Field: baseline.Field, Operation: Set,
		Values: []string{}, BaseGeneratedHash: baseline.GeneratedHash,
	}
	encoded, err = json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "\"values\":[]") {
		t.Fatalf("empty patch list was omitted: %s", encoded)
	}
	var decodedPatch Patch
	if err := json.Unmarshal(encoded, &decodedPatch); err != nil {
		t.Fatal(err)
	}
	if decodedPatch.Values == nil || len(decodedPatch.Values) != 0 {
		t.Fatalf("empty patch list did not round trip: %+v", decodedPatch)
	}
}

func TestClonePreservesBaselineAndUnknownByteSlices(t *testing.T) {
	base := NewListBaseline("event-a", "changes", []string{"a"})
	cloned := base.Clone()
	cloned.Values[0] = "changed"
	if base.Values[0] != "a" {
		t.Fatalf("baseline clone shared backing storage: %+v", base.Values)
	}
	unknown := map[string][]byte{"custom": []byte("bytes")}
	preserved := cloneUnknownBlocks(unknown)
	preserved["custom"][0] = 'X'
	if unknown["custom"][0] != 'b' {
		t.Fatalf("unknown preservation shared backing storage")
	}
	if !reflect.DeepEqual(sortedUnknownKeys(unknown), []string{"custom"}) {
		t.Fatal("unknown ordering is not deterministic")
	}
}
