package publication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestPublicationRejectsMilestoneWhosePrivateChainIsNotRooted(t *testing.T) {
	const projectID = "project-unrooted-milestone"
	dataRoot, projectRoot, vaultRoot, mapping, manifest, legacy := setupPublishEnvWithIndex(t, projectID, true)
	plan := validMarkdownPublicationPlanForTest(t, dataRoot, manifest, legacy)
	files := filesFromPlan(plan)
	accepted, err := reviewv4.LoadProjection(files[reviewv2.ReviewRelativePath], files[reviewv2.HistoryRelativePath], files[reviewv2.MachineLedgerRelativePath], files[sessionIndexRelativePath])
	if err != nil {
		t.Fatal(err)
	}
	viewDigest := "sha256:" + strings.Repeat("a", 64)
	dependencyDigest := "sha256:" + strings.Repeat("b", 64)
	ref := reviewv4.SourceTurnRef{Provider: "codex", SessionID: "foreign-session", TurnUnitID: "turn-foreign", SessionViewDigest: viewDigest}
	missing := "not_captured"
	loop := reviewv4.ClosedLoop{
		TriggerQuestion:   reviewv4.ClosedLoopSegment{State: "present", Text: "Verify it.", SourceTurnRefs: []reviewv4.SourceTurnRef{ref}},
		Conclusion:        reviewv4.ClosedLoopConclusion{Kind: reviewv4.ConclusionVisibleAnswerExcerpt, Text: "Verified.", SourceTurnRefs: []reviewv4.SourceTurnRef{ref}},
		Execution:         reviewv4.ClosedLoopSegment{State: "present", Text: "go test", SourceTurnRefs: []reviewv4.SourceTurnRef{ref}},
		Verification:      reviewv4.ClosedLoopSegment{State: "present", Text: "passed", SourceTurnRefs: []reviewv4.SourceTurnRef{ref}},
		ImpactAndFollowUp: reviewv4.ClosedLoopSegment{State: "missing", MissingReason: &missing, SourceTurnRefs: []reviewv4.SourceTurnRef{}},
		SourceTurnRefs:    []reviewv4.SourceTurnRef{ref}, Coverage: reviewv4.ClosedLoopCoverage{SourceTurns: 1, CapturedTurns: 1},
	}
	accepted.Review.Timeline = []reviewv4.Timeline{{ID: "milestone:foreign", GenerationID: manifest.GenerationID, OccurredAt: "2026-09-08T00:00:00Z", Kind: "machine_verification", Title: "Machine-observed verification", Summary: "passed", DecisionIDs: []string{}, ClosedLoop: loop}}
	accepted.Review.ChainDependencies = []reviewv4.ChainDependency{{Provider: ref.Provider, SessionID: ref.SessionID, SessionViewDigest: viewDigest, DependencyDigest: dependencyDigest, TurnUnitIDs: []string{ref.TurnUnitID}}}
	accepted.Review.MinimumReaderVersion, accepted.Review.MinimumWriterVersion = "0.4.3", "0.4.3"
	ledger := accepted.Ledger
	ledger.MinimumReaderVersion, ledger.MinimumWriterVersion = "0.4.3", "0.4.3"
	ledger.DocumentProjection.PresentationBase = accepted.Review
	seed, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = reviewv4.DecodeLedger(seed)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := reviewv4.RenderMarkdown(accepted.Review, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger.ReviewSHA256, ledger.HistorySHA256 = sha256Hex(pair.Review), sha256Hex(pair.History)
	ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256 = ledger.ReviewSHA256, ledger.HistorySHA256
	ledgerBody, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	for index := range plan.Files {
		switch plan.Files[index].Relative {
		case reviewv2.ReviewRelativePath:
			plan.Files[index].Desired = pair.Review
		case reviewv2.HistoryRelativePath:
			plan.Files[index].Desired = pair.History
		case reviewv2.MachineLedgerRelativePath:
			plan.Files[index].Desired = ledgerBody
		}
	}
	if _, err := Publish(t.Context(), Options{ProjectID: projectID, PreparedGeneration: manifest.GenerationID, Plan: plan, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}); err == nil || !strings.Contains(err.Error(), "conversation chain") {
		t.Fatalf("publication did not reject unrooted private evidence: %v", err)
	}
	for _, relative := range []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath, sessionIndexRelativePath} {
		for _, target := range []string{filepath.Join(projectRoot, filepath.FromSlash(relative)), filepath.Join(vaultRoot, filepath.FromSlash(vaultRelativePath(mapping.VaultReviewPath, relative)))} {
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("rejection changed public file %s: %v", target, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "publication-journal", projectID, "intent-v1.json")); !os.IsNotExist(err) {
		t.Fatalf("private-root rejection created a journal intent: %v", err)
	}
}

func TestVerifyPrivateChainBindingsAuthenticatesExactRootAndTurnSet(t *testing.T) {
	const projectID = "project-private-chain-matrix"
	dataRoot, _, _, _, manifest, _ := setupPublishEnvWithIndex(t, projectID, true)
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	viewBody, err := store.LoadObject(memorystore.ObjectSessionView, manifest.SessionViews[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	var view memory.SessionView
	if err := json.Unmarshal(viewBody, &view); err != nil {
		t.Fatal(err)
	}
	active := slices.Clone(view.ActiveRevisionIDs)
	slices.Sort(active)
	proof := conversationchain.DependencyProofV1{SessionViewDigest: view.Digest, SourceRecordDigest: view.SourceRecordDigest, VisibleRecords: []conversationchain.DependencyRecordProofV1{}, ActiveRevisionIDs: active, RuleVersion: "visible-turn-v1", RedactionVersion: "redaction-v1"}
	dependencyDigest, err := memory.Digest(proof)
	if err != nil {
		t.Fatal(err)
	}
	chain := conversationchain.Document{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", Digest: "sha256:" + strings.Repeat("0", 64), ProjectID: projectID, Provider: view.Provider, SessionID: view.SessionID, SessionViewDigest: view.Digest, DependencyDigest: dependencyDigest, DependencyProofV1: &proof, SegmentationRuleVersion: "visible-turn-v1", Coverage: conversationchain.Coverage{}, TurnUnits: []conversationchain.TurnUnit{}}
	body, err := conversationchain.Render(chain)
	if err != nil {
		t.Fatal(err)
	}
	chain, err = conversationchain.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutConversationChain(chain); err != nil {
		t.Fatal(err)
	}
	root := memory.ConversationChainDependency{Provider: chain.Provider, SessionID: chain.SessionID, SessionViewDigest: chain.SessionViewDigest, Digest: chain.Digest}
	manifest.ConversationChains = []memory.ConversationChainDependency{root}
	accepted := reviewv4.Accepted{Review: reviewv4.Presentation{ChainDependencies: []reviewv4.ChainDependency{{Provider: chain.Provider, SessionID: chain.SessionID, SessionViewDigest: chain.SessionViewDigest, DependencyDigest: chain.DependencyDigest, TurnUnitIDs: []string{}}}}}
	if err := VerifyPrivateChainBindings(t.Context(), store, manifest, accepted); err != nil {
		t.Fatalf("public-valid rooted setup was rejected: %v", err)
	}
	retained := manifest
	retained.ConversationChains = nil
	retained.RetainedConversationChains = []memory.ConversationChainDependency{root}
	if err := VerifyPrivateChainBindings(t.Context(), store, retained, accepted); err != nil {
		t.Fatalf("valid retained historical root was rejected: %v", err)
	}
	tests := []struct {
		name string
		edit func(*memory.GenerationManifest, *reviewv4.Accepted)
	}{
		{"invented dependency digest", func(_ *memory.GenerationManifest, value *reviewv4.Accepted) {
			value.Review.ChainDependencies[0].DependencyDigest = "sha256:" + strings.Repeat("f", 64)
		}},
		{"valid object absent from roots", func(value *memory.GenerationManifest, _ *reviewv4.Accepted) { value.ConversationChains = nil }},
		{"wrong historical view", func(_ *memory.GenerationManifest, value *reviewv4.Accepted) {
			value.Review.ChainDependencies[0].SessionViewDigest = "sha256:" + strings.Repeat("e", 64)
		}},
		{"foreign provider", func(_ *memory.GenerationManifest, value *reviewv4.Accepted) {
			value.Review.ChainDependencies[0].Provider = "claude"
		}},
		{"missing turn", func(_ *memory.GenerationManifest, value *reviewv4.Accepted) {
			value.Review.ChainDependencies[0].TurnUnitIDs = []string{"turn-invented"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateManifest := manifest
			candidateManifest.ConversationChains = slices.Clone(manifest.ConversationChains)
			candidate := accepted
			candidate.Review.ChainDependencies = slices.Clone(accepted.Review.ChainDependencies)
			test.edit(&candidateManifest, &candidate)
			if err := VerifyPrivateChainBindings(t.Context(), store, candidateManifest, candidate); err == nil {
				t.Fatal("fabricated private chain binding was accepted")
			}
		})
	}
	cancelled, cancel := context.WithCancelCause(t.Context())
	cause := context.Canceled
	cancel(cause)
	if err := VerifyPrivateChainBindings(cancelled, store, manifest, accepted); !errors.Is(err, cause) {
		t.Fatalf("cancellation was not propagated before private loads: %v", err)
	}
}
