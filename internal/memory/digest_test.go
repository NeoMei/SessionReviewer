package memory

import (
	"math"
	"reflect"
	"testing"
)

func TestDigestNormalizesUnorderedDataWithoutMutatingCaller(t *testing.T) {
	first := validSessionView()
	first.ActiveRevisionIDs = []string{revisionDigest("2"), revisionDigest("1")}
	first.DerivedRecords[0].DependencyRevisionIDs = []string{revisionDigest("3"), revisionDigest("1")}
	second := first
	second.ActiveRevisionIDs = []string{revisionDigest("1"), revisionDigest("2")}
	second.DerivedRecords = append([]DerivedRecord(nil), first.DerivedRecords...)
	second.DerivedRecords[0].DependencyRevisionIDs = []string{revisionDigest("1"), revisionDigest("3")}
	original := append([]string(nil), first.ActiveRevisionIDs...)

	firstDigest, err := Digest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := Digest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("equivalent values differ: %s != %s", firstDigest, secondDigest)
	}
	if !reflect.DeepEqual(first.ActiveRevisionIDs, original) {
		t.Fatalf("Digest mutated caller: %v", first.ActiveRevisionIDs)
	}
}

func TestDigestPreservesOrderedSessionViewDependencies(t *testing.T) {
	first := validProjectView()
	first.SessionViewDependencies = append(first.SessionViewDependencies,
		SessionViewDependency{Provider: "codex", SessionID: "s2", Digest: objectDigest("9")},
	)
	second := first
	second.SessionViewDependencies = append([]SessionViewDependency(nil), first.SessionViewDependencies...)
	second.SessionViewDependencies[0], second.SessionViewDependencies[1] = second.SessionViewDependencies[1], second.SessionViewDependencies[0]

	firstDigest, err := Digest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := Digest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest == secondDigest {
		t.Fatal("ordered SessionView dependencies were normalized as a set")
	}
}

func TestDigestNormalizesSemanticallyUnorderedProjectCollections(t *testing.T) {
	first := validProjectView()
	first.AssociatedUsage = []AssociatedUsage{
		{Provider: "codex", SessionID: "s2", UsageRecordDigest: objectDigest("9"), TotalTokens: 2},
		{Provider: "codex", SessionID: "s1", UsageRecordDigest: objectDigest("2"), TotalTokens: 10},
	}
	second := first
	second.AssociatedUsage = append([]AssociatedUsage(nil), first.AssociatedUsage...)
	second.AssociatedUsage[0], second.AssociatedUsage[1] = second.AssociatedUsage[1], second.AssociatedUsage[0]

	firstDigest, err := Digest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := Digest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("unordered associated usage changed digest: %s != %s", firstDigest, secondDigest)
	}
}

func TestDigestRejectsInvalidUTF8AndNonFiniteNumbers(t *testing.T) {
	for name, value := range map[string]any{
		"invalid utf8": map[string]string{"value": string([]byte{0xff})},
		"nan":          map[string]float64{"value": math.NaN()},
		"positive inf": map[string]float64{"value": math.Inf(1)},
		"negative inf": map[string]float64{"value": math.Inf(-1)},
	} {
		t.Run(name, func(t *testing.T) {
			if digest, err := Digest(value); err == nil || digest != "" {
				t.Fatalf("Digest(%s) = %q, %v; want rejection", name, digest, err)
			}
		})
	}
}

func TestObservationRevisionIDExcludesStoredRevisionIDAndIncludesRequiredIdentityParts(t *testing.T) {
	base := validObservation(validObservationKey(), "adapter-1", map[string]string{"exit_code": "0"})
	want := ObservationRevisionID(base)
	base.RevisionID = revisionDigest("f")
	if got := ObservationRevisionID(base); got != want {
		t.Fatalf("stored revision_id influenced identity: %s != %s", got, want)
	}

	mutations := []func(*ObservationRevision){
		func(value *ObservationRevision) { value.Key.Sequence++ },
		func(value *ObservationRevision) { value.Outcome = "failure" },
		func(value *ObservationRevision) {
			value.Ref.SourceHash = string([]byte("b")) + value.Ref.SourceHash[1:]
		},
		func(value *ObservationRevision) { value.AdapterVersion = "adapter-2" },
	}
	for index, mutate := range mutations {
		value := base
		mutate(&value)
		if got := ObservationRevisionID(value); got == want {
			t.Fatalf("mutation %d did not change revision identity", index)
		}
	}
}
