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

func TestSessionViewDigestIncludesUsageAndObservationSummaryContentWithoutAliasing(t *testing.T) {
	base := validSessionView()
	originalFields := map[string]string{"component": base.ObservationSummaries[0].Fields["component"], "status": base.ObservationSummaries[0].Fields["status"]}
	baseDigest, err := SessionViewDigest(base)
	if err != nil {
		t.Fatal(err)
	}

	changedSummary := base
	changedSummary.ObservationSummaries = append([]ObservationSummary(nil), base.ObservationSummaries...)
	changedSummary.ObservationSummaries[0].Fields = map[string]string{"component": "package", "status": "build"}
	summaryDigest, err := SessionViewDigest(changedSummary)
	if err != nil {
		t.Fatal(err)
	}
	if summaryDigest == baseDigest {
		t.Fatal("observation summary content did not affect SessionView identity")
	}
	if !reflect.DeepEqual(base.ObservationSummaries[0].Fields, originalFields) {
		t.Fatalf("SessionViewDigest aliased or mutated caller fields: %v", base.ObservationSummaries[0].Fields)
	}

	changedUsage := base
	changedUsage.UsageRecordDigest = objectDigest("9")
	usageDigest, err := SessionViewDigest(changedUsage)
	if err != nil {
		t.Fatal(err)
	}
	if usageDigest == baseDigest {
		t.Fatal("usage record digest did not affect SessionView identity")
	}
}

func TestSessionViewDigestIncludesAuthenticatedSourceIdentity(t *testing.T) {
	first := validSessionView()
	second := first
	second.SourceIdentity = "src2"
	firstDigest, err := SessionViewDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := SessionViewDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest == secondDigest {
		t.Fatal("SourceIdentity did not affect SessionView canonical identity")
	}
}

func TestDigestNormalizesSemanticallyUnorderedProjectCollections(t *testing.T) {
	first := validProjectView()
	first.AssociatedUsage = []AssociatedUsage{
		{Provider: "codex", SessionID: "s2", UsageRecordDigest: objectDigest("9")},
		{Provider: "codex", SessionID: "s1", UsageRecordDigest: objectDigest("2")},
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

func TestTypedSelfDigestsExcludeStoredDigestAndNormalizeNilEmptySets(t *testing.T) {
	for name, typeOf := range map[string]reflect.Type{
		"SessionView":       reflect.TypeOf(SessionViewIdentity{}),
		"ProjectProbeState": reflect.TypeOf(ProjectProbeStateIdentity{}),
		"ProjectView":       reflect.TypeOf(ProjectViewIdentity{}),
	} {
		if _, exists := typeOf.FieldByName("Digest"); exists {
			t.Fatalf("%s identity DTO contains stored Digest", name)
		}
	}

	session := validSessionView()
	sessionDigest, err := SessionViewDigest(session)
	if err != nil {
		t.Fatal(err)
	}
	session.Digest = objectDigest("f")
	if got, err := SessionViewDigest(session); err != nil || got != sessionDigest {
		t.Fatalf("stored SessionView digest affected identity: %q %v", got, err)
	}
	session.ActiveRevisionIDs = nil
	nilDigest, err := SessionViewDigest(session)
	if err != nil {
		t.Fatal(err)
	}
	session.ActiveRevisionIDs = []string{}
	emptyDigest, err := SessionViewDigest(session)
	if err != nil || nilDigest != emptyDigest {
		t.Fatalf("nil and empty active revision sets differ: %q %q %v", nilDigest, emptyDigest, err)
	}

	probe := validProbeState()
	probeDigest, err := ProjectProbeStateDigest(probe)
	if err != nil {
		t.Fatal(err)
	}
	probe.Digest = objectDigest("f")
	if got, err := ProjectProbeStateDigest(probe); err != nil || got != probeDigest {
		t.Fatalf("stored ProjectProbeState digest affected identity: %q %v", got, err)
	}
	probe.RemoteIdentityHashes = nil
	nilDigest, err = ProjectProbeStateDigest(probe)
	if err != nil {
		t.Fatal(err)
	}
	probe.RemoteIdentityHashes = []string{}
	emptyDigest, err = ProjectProbeStateDigest(probe)
	if err != nil || nilDigest != emptyDigest {
		t.Fatalf("nil and empty remote identity sets differ: %q %q %v", nilDigest, emptyDigest, err)
	}

	project := validProjectView()
	projectDigest, err := ProjectViewDigest(project)
	if err != nil {
		t.Fatal(err)
	}
	project.Digest = objectDigest("f")
	if got, err := ProjectViewDigest(project); err != nil || got != projectDigest {
		t.Fatalf("stored ProjectView digest affected identity: %q %v", got, err)
	}
	project.AssociatedUsage = nil
	nilDigest, err = ProjectViewDigest(project)
	if err != nil {
		t.Fatal(err)
	}
	project.AssociatedUsage = []AssociatedUsage{}
	emptyDigest, err = ProjectViewDigest(project)
	if err != nil || nilDigest != emptyDigest {
		t.Fatalf("nil and empty associated usage sets differ: %q %q %v", nilDigest, emptyDigest, err)
	}

	manifest := validGenerationManifest()
	manifest.ActiveRevisions = nil
	manifest.SupersededRevisions = nil
	manifest.WithdrawnRevisions = nil
	nilDigest, err = Digest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ActiveRevisions = map[string]string{}
	manifest.SupersededRevisions = map[string]string{}
	manifest.WithdrawnRevisions = map[string]string{}
	emptyDigest, err = Digest(manifest)
	if err != nil || nilDigest != emptyDigest {
		t.Fatalf("nil and empty revision maps differ: %q %q %v", nilDigest, emptyDigest, err)
	}
}

func TestValidatorsRejectTamperedCanonicalSelfDigests(t *testing.T) {
	t.Run("SessionView", func(t *testing.T) {
		value := validSessionView()
		if err := ValidateSessionView(value); err != nil {
			t.Fatal(err)
		}
		value.MaterializerVersion = "session-view-v2"
		if err := ValidateSessionView(value); err == nil {
			t.Fatal("SessionView accepted content changed without a new self digest")
		}
	})

	t.Run("ProjectProbeState", func(t *testing.T) {
		value := validProbeState()
		if err := ValidateProjectProbeState(value); err != nil {
			t.Fatal(err)
		}
		value.Branch = "release"
		if err := ValidateProjectProbeState(value); err == nil {
			t.Fatal("ProjectProbeState accepted content changed without a new self digest")
		}
	})

	t.Run("ProjectView", func(t *testing.T) {
		value := validProjectView()
		if err := ValidateProjectView(value); err != nil {
			t.Fatal(err)
		}
		value.ReducerVersion = "project-view-v2"
		if err := ValidateProjectView(value); err == nil {
			t.Fatal("ProjectView accepted content changed without a new self digest")
		}
	})
}
