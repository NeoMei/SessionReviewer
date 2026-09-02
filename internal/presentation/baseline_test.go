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
