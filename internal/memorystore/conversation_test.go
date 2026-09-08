package memorystore

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
)

func TestPutConversationChainIsCanonicalImmutableCAS(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixture := buildStoredFixture(t, store, "generation-chain-cas")
	chain := emptyConversationChain(t, fixture.session)
	want, err := conversationchain.Render(chain)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		got, err := store.PutConversationChain(chain)
		if err != nil || got != chain.Digest {
			t.Fatalf("chain CAS: digest=%s err=%v", got, err)
		}
	}
	got, err := store.LoadObject(ObjectConversationChain, chain.Digest)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("canonical chain load changed bytes: got=%q want=%q err=%v", got, want, err)
	}

	changed := chain
	changed.DependencyDigest = prefixedDigest("changed")
	if _, err := store.PutConversationChain(changed); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("changed chain body retained stale digest: %v", err)
	}
	foreign := chain
	foreign.ProjectID = "project-2"
	foreign.Digest = conversationchain.CanonicalDigest(foreign)
	if _, err := store.PutConversationChain(foreign); err == nil || !strings.Contains(err.Error(), "different project") {
		t.Fatalf("foreign chain accepted: %v", err)
	}
	for name, mutate := range map[string]func(*conversationchain.Document){
		"provider": func(value *conversationchain.Document) { value.Provider = "claude" },
		"session":  func(value *conversationchain.Document) { value.SessionID = "other" },
		"view":     func(value *conversationchain.Document) { value.SessionViewDigest = prefixedDigest("foreign-view") },
	} {
		t.Run("foreign "+name, func(t *testing.T) {
			value := chain
			mutate(&value)
			value.Digest = conversationchain.CanonicalDigest(value)
			if _, err := store.PutConversationChain(value); err == nil || !strings.Contains(err.Error(), "SessionView") {
				t.Fatalf("foreign %s accepted: %v", name, err)
			}
		})
	}
}

func TestConversationChainLoadRejectsUnsafeOrNoncanonicalObjects(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "truncated", mutate: func(body []byte) []byte { return body[:len(body)/2] }},
		{name: "duplicate field", mutate: func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"schema_version":`), []byte(`"schema_version":1,"schema_version":`), 1)
		}},
		{name: "unknown field", mutate: func(body []byte) []byte {
			return bytes.Replace(body, []byte(`{"schema_version"`), []byte(`{"unknown":true,"schema_version"`), 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataRoot := t.TempDir()
			store, err := Open(dataRoot, testProjectID)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			fixture := buildStoredFixture(t, store, "generation-chain-corrupt-"+strings.ReplaceAll(test.name, " ", "-"))
			chain := emptyConversationChain(t, fixture.session)
			if _, err := store.PutConversationChain(chain); err != nil {
				t.Fatal(err)
			}
			path := conversationChainPath(dataRoot, chain.Digest)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, test.mutate(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LoadObject(ObjectConversationChain, chain.Digest); err == nil {
				t.Fatal("malformed conversation chain loaded")
			}
		})
	}

	if runtime.GOOS != "windows" {
		t.Run("weak permissions", func(t *testing.T) {
			dataRoot := t.TempDir()
			store, err := Open(dataRoot, testProjectID)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			fixture := buildStoredFixture(t, store, "generation-chain-mode")
			chain := emptyConversationChain(t, fixture.session)
			if _, err := store.PutConversationChain(chain); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(conversationChainPath(dataRoot, chain.Digest), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LoadObject(ObjectConversationChain, chain.Digest); err == nil {
				t.Fatal("weakly protected conversation chain loaded")
			}
		})
	}
}

func TestOpenReadOnlyLoadsConversationChainButCannotCreateOne(t *testing.T) {
	dataRoot := t.TempDir()
	writable, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	fixture := buildStoredFixture(t, writable, "generation-chain-read-only")
	chain := emptyConversationChain(t, fixture.session)
	if _, err := writable.PutConversationChain(chain); err != nil {
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenReadOnly(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if _, err := readOnly.LoadObject(ObjectConversationChain, chain.Digest); err != nil {
		t.Fatalf("read-only chain load: %v", err)
	}
	newChain := chain
	newChain.DependencyDigest = prefixedDigest("new-chain")
	newChain.Digest = conversationchain.CanonicalDigest(newChain)
	if _, err := readOnly.PutConversationChain(newChain); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("read-only store created chain: %v", err)
	}
}

func TestPrepareAndReloadAuthenticateCurrentAndHistoricalConversationChains(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := buildStoredFixture(t, store, "generation-chain-graph")
	current := materializedConversationChain(t, fixture.session, fixture.observation)
	if _, err := store.PutConversationChain(current); err != nil {
		t.Fatal(err)
	}
	historicalView, historicalRevision := putHistoricalSessionView(t, store, fixture.session)
	historical := materializedConversationChain(t, historicalView, historicalRevision)
	if _, err := store.PutConversationChain(historical); err != nil {
		t.Fatal(err)
	}
	fixture.manifest.ConversationChains = []memory.ConversationChainDependency{{Provider: current.Provider, SessionID: current.SessionID, SessionViewDigest: current.SessionViewDigest, Digest: current.Digest}}
	fixture.manifest.RetainedConversationChains = []memory.ConversationChainDependency{{Provider: historical.Provider, SessionID: historical.SessionID, SessionViewDigest: historical.SessionViewDigest, Digest: historical.Digest}}
	if _, err := store.PrepareGeneration(fixture.manifest); err != nil {
		t.Fatalf("prepare chain graph: %v", err)
	}
	if _, manifest, err := store.LoadPrepared(); err != nil || len(manifest.ConversationChains) != 1 || len(manifest.RetainedConversationChains) != 1 {
		t.Fatalf("reload chain graph: manifest=%+v err=%v", manifest, err)
	}
}

func TestPrepareGenerationRejectsSemanticallyForgedConversationEvidence(t *testing.T) {
	mutations := []struct {
		name string
		edit func(*conversationchain.Document)
	}{
		{name: "verification state", edit: func(value *conversationchain.Document) { value.TurnUnits[0].Results[0].VerificationState = "failed" }},
		{name: "kind", edit: func(value *conversationchain.Document) { value.TurnUnits[0].Results[0].Kind = "deployment" }},
		{name: "excerpt", edit: func(value *conversationchain.Document) { value.TurnUnits[0].Results[0].Excerpt = "passed=0" }},
		{name: "source coordinate", edit: func(value *conversationchain.Document) { value.TurnUnits[0].Results[0].SourceRef.RecordOrdinal++ }},
		{name: "inactive revision", edit: func(value *conversationchain.Document) {
			value.TurnUnits[0].Results[0].RevisionID = prefixedDigest("inactive")
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			dataRoot := t.TempDir()
			store, err := Open(dataRoot, testProjectID)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			fixture := buildStoredFixture(t, store, "generation-forged-chain-"+strings.ReplaceAll(test.name, " ", "-"))
			chain := materializedConversationChain(t, fixture.session, fixture.observation)
			test.edit(&chain)
			chain.Digest = conversationchain.CanonicalDigest(chain)
			if _, err := store.PutConversationChain(chain); err != nil {
				t.Fatalf("store structurally valid forgery: %v", err)
			}
			fixture.manifest.ConversationChains = []memory.ConversationChainDependency{{Provider: chain.Provider, SessionID: chain.SessionID, SessionViewDigest: chain.SessionViewDigest, Digest: chain.Digest}}
			if _, err := store.PrepareGeneration(fixture.manifest); err == nil || !strings.Contains(err.Error(), "retained evidence") {
				t.Fatalf("semantic forgery accepted or misclassified: %v", err)
			}
		})
	}
}

func TestCurrentAndHistoricalSemanticForgeryFailsPrepareAndReload(t *testing.T) {
	for _, historical := range []bool{false, true} {
		for _, reload := range []bool{false, true} {
			name := "current-prepare"
			if historical {
				name = "historical-prepare"
			}
			if reload {
				name = strings.TrimSuffix(name, "prepare") + "reload"
			}
			t.Run(name, func(t *testing.T) {
				dataRoot := t.TempDir()
				store, err := Open(dataRoot, testProjectID)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				fixture := buildStoredFixture(t, store, "generation-chain-"+name)
				view, revision := fixture.session, fixture.observation
				if historical {
					view, revision = putHistoricalSessionView(t, store, fixture.session)
				}
				chain := materializedConversationChain(t, view, revision)
				if chain.TurnUnits[0].Results[0].VerificationState == "failed" {
					chain.TurnUnits[0].Results[0].VerificationState = "passed"
				} else {
					chain.TurnUnits[0].Results[0].VerificationState = "failed"
				}
				chain.Digest = conversationchain.CanonicalDigest(chain)
				if _, err := store.PutConversationChain(chain); err != nil {
					t.Fatal(err)
				}
				dependency := memory.ConversationChainDependency{Provider: chain.Provider, SessionID: chain.SessionID, SessionViewDigest: chain.SessionViewDigest, Digest: chain.Digest}
				if historical {
					fixture.manifest.RetainedConversationChains = []memory.ConversationChainDependency{dependency}
				} else {
					fixture.manifest.ConversationChains = []memory.ConversationChainDependency{dependency}
				}
				if reload {
					artifacts, artifactErr := store.prepareArtifacts(fixture.manifest)
					if artifactErr != nil {
						t.Fatal(artifactErr)
					}
					root := filepath.Join(dataRoot, "projects", testProjectID, "memory-v1")
					if err := os.WriteFile(filepath.Join(root, "generations", fixture.manifest.GenerationID+".json"), artifacts.manifestBody, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, "manifest.json"), artifacts.pointerBody, 0o600); err != nil {
						t.Fatal(err)
					}
					_, _, err = store.LoadPrepared()
				} else {
					_, err = store.PrepareGeneration(fixture.manifest)
				}
				if err == nil || !strings.Contains(err.Error(), "retained evidence") {
					t.Fatalf("canonical semantic forgery crossed %s: %v", name, err)
				}
			})
		}
	}
}

func TestPrepareGenerationRejectsMissingConversationChain(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := Open(dataRoot, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := buildStoredFixture(t, store, "generation-chain-missing")
	fixture.manifest.ConversationChains = []memory.ConversationChainDependency{{Provider: fixture.session.Provider, SessionID: fixture.session.SessionID, SessionViewDigest: fixture.session.Digest, Digest: prefixedDigest("missing-chain")}}
	if _, err := store.PrepareGeneration(fixture.manifest); err == nil || !strings.Contains(err.Error(), "conversation chain") {
		t.Fatalf("missing chain accepted or misclassified: %v", err)
	}
}

func emptyConversationChain(t *testing.T, view memory.SessionView) conversationchain.Document {
	t.Helper()
	value := conversationchain.Document{
		SchemaVersion: 1, MinimumReaderVersion: "0.4.0", Digest: "sha256:" + strings.Repeat("0", 64),
		ProjectID: view.ProjectID, Provider: view.Provider, SessionID: view.SessionID,
		SessionViewDigest: view.Digest, DependencyDigest: prefixedDigest("chain-dependency"), SegmentationRuleVersion: "visible-turn-v1",
		Coverage: conversationchain.Coverage{}, TurnUnits: []conversationchain.TurnUnit{},
	}
	body, err := conversationchain.Render(value)
	if err != nil {
		t.Fatal(err)
	}
	value, err = conversationchain.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func materializedConversationChain(t *testing.T, view memory.SessionView, revision memory.ObservationRevision) conversationchain.Document {
	t.Helper()
	document, _, err := conversationchain.Materialize(conversationchain.MaterializeInput{
		View: view,
		Messages: []conversationchain.SourceMessage{
			{Role: conversationchain.RoleUser, Text: "run tests", OccurredAt: testStartedAt, RecordHash: strings.Repeat("a", 64), RecordOrdinal: 10},
			{Role: conversationchain.RoleAssistant, Phase: "final_answer", Text: "tests passed", OccurredAt: testEndedAt, RecordHash: strings.Repeat("b", 64), RecordOrdinal: 13},
		},
		Revisions: []memory.ObservationRevision{revision},
		SourceCoverage: conversationchain.VisibleCoverage{
			SourceRecords: 13, VisibleMessages: 2, CapturedMessages: 2, Complete: true,
		},
		RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func putHistoricalSessionView(t *testing.T, store *Store, base memory.SessionView) (memory.SessionView, memory.ObservationRevision) {
	t.Helper()
	revision := memory.ObservationRevision{
		SchemaVersion: memory.MemorySchemaVersion,
		Key:           memory.ObservationKey{Provider: base.Provider, SessionID: base.SessionID, SourceIdentity: base.SourceIdentity, Sequence: 2, ProjectID: base.ProjectID, Kind: "verification", Subject: "historical-test"},
		Ref:           memory.SourceRef{Provider: base.Provider, SessionID: base.SessionID, SourceIdentity: base.SourceIdentity, Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 12, ByteOffset: 440}}, SourceHash: strings.Repeat("e", 64)},
		Timestamp:     testEndedAt, Operation: "verification", Object: "focused tests", Outcome: "failed", Fields: map[string]string{"passed": "0", "failed": "1"}, AdapterID: "codex-jsonl", AdapterVersion: "v1",
	}
	revision.RevisionID = memory.ObservationRevisionID(revision)
	chunk, err := store.PutObservationChunk([]memory.ObservationRevision{revision})
	if err != nil {
		t.Fatal(err)
	}
	view := base
	view.ActiveRevisionIDs = []string{revision.RevisionID}
	view.ObservationSummaries = []memory.ObservationSummary{{RevisionID: revision.RevisionID, Sequence: revision.Key.Sequence, Kind: revision.Key.Kind, Subject: revision.Key.Subject, OccurredAt: revision.Timestamp, Operation: revision.Operation, Object: revision.Object, Outcome: revision.Outcome, Fields: revision.Fields}}
	view.ObservationChunkDigests = []string{chunk}
	view.DependencyDigest = prefixedDigest("historical-view")
	view.Digest, err = memory.SessionViewDigest(view)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSessionView(view); err != nil {
		t.Fatal(err)
	}
	return view, revision
}

func conversationChainPath(dataRoot, digest string) string {
	return filepath.Join(dataRoot, "projects", testProjectID, "memory-v1", "conversation-chains", digestLeaf(digest, ".json"))
}
