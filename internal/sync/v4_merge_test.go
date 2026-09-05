package sync

import (
	"bytes"
	"os"
	"testing"

	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

func TestV4MergeUsesCommonAncestor(t *testing.T) {
	key := syncdoc.UnitKey{Kind: syncdoc.UnitSection, Name: "session-reviewer/v4/project-overview/goal"}
	set := func(value string) syncdoc.UnitSet {
		return syncdoc.UnitSet{key: {Present: true, Value: []byte(value)}}
	}

	got := MergeV4Units(V4MergeInput{Base: set("base"), Project: set("project"), Vault: set("vault"), HasBase: true})
	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts=%v", got.Conflicts)
	}
	same := MergeV4Units(V4MergeInput{Base: set("base"), Project: set("same"), Vault: set("same"), HasBase: true})
	if len(same.Conflicts) != 0 || string(same.Units[key].Value) != "same" {
		t.Fatal("equal edits did not converge")
	}
}

func TestV4MergeCombinesDifferentFields(t *testing.T) {
	goal := syncdoc.UnitKey{Kind: syncdoc.UnitSection, Name: "session-reviewer/v4/project-overview/goal"}
	status := syncdoc.UnitKey{Kind: syncdoc.UnitSection, Name: "session-reviewer/v4/project-overview/status"}
	base := syncdoc.UnitSet{
		goal:   {Present: true, Value: []byte("base goal")},
		status: {Present: true, Value: []byte("base status")},
	}
	project := syncdoc.UnitSet{
		goal:   {Present: true, Value: []byte("project goal")},
		status: {Present: true, Value: []byte("base status")},
	}
	vault := syncdoc.UnitSet{
		goal:   {Present: true, Value: []byte("base goal")},
		status: {Present: true, Value: []byte("vault status")},
	}

	got := MergeV4Units(V4MergeInput{Base: base, Project: project, Vault: vault, HasBase: true})
	if len(got.Conflicts) != 0 || string(got.Units[goal].Value) != "project goal" || string(got.Units[status].Value) != "vault status" {
		t.Fatalf("different-field merge=%+v", got)
	}
}

func TestV4MergeWithoutBaseRequiresBothSidesToAgree(t *testing.T) {
	key := syncdoc.UnitKey{Kind: syncdoc.UnitSection, Name: "session-reviewer/v4/project-overview/status"}
	set := func(value string) syncdoc.UnitSet {
		return syncdoc.UnitSet{key: {Present: true, Value: []byte(value)}}
	}

	tests := []struct {
		name      string
		project   syncdoc.UnitSet
		vault     syncdoc.UnitSet
		wantValue string
		conflicts int
	}{
		{name: "same", project: set("ready"), vault: set("ready"), wantValue: "ready"},
		{name: "different", project: set("project"), vault: set("vault"), conflicts: 1},
		{name: "project only", project: set("project"), vault: syncdoc.UnitSet{}, conflicts: 1},
		{name: "vault only", project: syncdoc.UnitSet{}, vault: set("vault"), conflicts: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := MergeV4Units(V4MergeInput{Project: test.project, Vault: test.vault})
			if len(got.Conflicts) != test.conflicts {
				t.Fatalf("conflicts=%v", got.Conflicts)
			}
			if test.wantValue != "" && string(got.Units[key].Value) != test.wantValue {
				t.Fatalf("value=%q", got.Units[key].Value)
			}
		})
	}
}

func TestV4MergeNormalizesOnlyValueLineEndings(t *testing.T) {
	key := syncdoc.UnitKey{Kind: syncdoc.UnitSection, Name: "session-reviewer/v4/project-overview/goal"}
	project := syncdoc.UnitSet{key: {Present: true, Value: []byte("same\r\ntext"), HeadingPresentation: []byte("project heading")}}
	vault := syncdoc.UnitSet{key: {Present: true, Value: []byte("same\ntext"), HeadingPresentation: []byte("project heading")}}

	got := MergeV4Units(V4MergeInput{Project: project, Vault: vault})
	if len(got.Conflicts) != 0 {
		t.Fatalf("semantic line-ending conflict=%v", got.Conflicts)
	}
	if string(got.Units[key].Value) != "same\r\ntext" {
		t.Fatalf("physical value=%q", got.Units[key].Value)
	}

	vault[key] = syncdoc.Unit{Present: true, Value: []byte("same\ntext"), HeadingPresentation: []byte("vault heading")}
	got = MergeV4Units(V4MergeInput{Project: project, Vault: vault})
	if len(got.Conflicts) != 1 {
		t.Fatalf("presentation mismatch conflicts=%v", got.Conflicts)
	}
}

func TestV4CandidateSensitiveTrustsOnlyAuthenticatedMarkerBoundaries(t *testing.T) {
	ledgerBody, err := os.ReadFile("../../testdata/contracts/v4/markdown/ledger.json")
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := reviewv4.DecodeLedger(ledgerBody)
	if err != nil {
		t.Fatal(err)
	}
	review, err := os.ReadFile("../../testdata/contracts/v4/markdown/review.md")
	if err != nil {
		t.Fatal(err)
	}
	document, err := syncdoc.ParseV4("项目回顾.md", review, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if candidateSensitive(Candidate{Present: true, RelativePath: "项目回顾.md", Document: document}) {
		t.Fatal("authenticated v4 marker identities made the accepted fixture sensitive")
	}

	const knownAnchor = "milestone-x6d696c6573746f6e653a616c706861"
	withKnownHumanID := append(bytes.Clone(review), []byte("\n```markdown\n[human example](项目历史.md#"+knownAnchor+")\n```\n")...)
	document, err = syncdoc.ParseV4("项目回顾.md", withKnownHumanID, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if !candidateSensitive(Candidate{Present: true, RelativePath: "项目回顾.md", Document: document}) {
		t.Fatal("known authenticated ID in fenced human content was exempted globally")
	}

	const longID = "decision-codegraph-source-transaction-finally-approved"
	fake := []byte("\n```markdown\n<!-- session-reviewer:v4-field entity=\"decision:" + longID + "\" name=\"title\" -->\n```\n")
	withFake := append(bytes.Clone(review), fake...)
	document, err = syncdoc.ParseV4("项目回顾.md", withFake, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if !candidateSensitive(Candidate{Present: true, RelativePath: "项目回顾.md", Document: document}) {
		t.Fatal("high-entropy marker-looking fenced human content was trusted as a machine identity")
	}
}
