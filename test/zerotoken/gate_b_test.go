package zerotoken

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/contextupdate"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestGateBEndToEndPublicationAndIdempotence(t *testing.T) {
	dataRoot := t.TempDir()
	projectRoot := t.TempDir()
	vaultRoot := t.TempDir()
	sessionsRoot := t.TempDir()
	projectID := "project-gate-b-test"
	_ = os.WriteFile(filepath.Join(projectRoot, "VERSION"), []byte("v1.0.0\n"), 0o600)
	_ = os.WriteFile(filepath.Join(projectRoot, "project-fixture.md"), []byte("# fixture\n"), 0o600)
	initializeGateRepository(t, projectRoot)

	mapping := config.ProjectMapping{
		ID:              projectID,
		Root:            projectRoot,
		VaultRoot:       vaultRoot,
		VaultReviewPath: "Projects/" + projectID + "/Session Review",
		VaultCaseMode:   platform.CaseSensitive,
	}
	cfg := config.Config{
		Version:  1,
		Projects: []config.ProjectMapping{mapping},
	}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	base := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	scanNow := base.Add(10 * time.Minute)
	for i := 1; i <= 154; i++ {
		started := base.Add(time.Duration(i) * time.Second)
		sessionID := fmt.Sprintf("session-%03d", i)
		sessionLines := []string{
			`{"timestamp":"` + started.Format(time.RFC3339) + `","type":"session_meta","payload":{"id":"` + sessionID + `","cwd":"` + filepath.ToSlash(projectRoot) + `","source":"codex"}}`,
			`{"timestamp":"` + started.Add(time.Second).Format(time.RFC3339) + `","type":"turn_context","payload":{"cwd":"` + filepath.ToSlash(projectRoot) + `","model":"gpt-5"}}`,
			`{"timestamp":"` + started.Add(2*time.Second).Format(time.RFC3339) + `","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"total_token_usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}}`,
		}
		if err := os.WriteFile(filepath.Join(sessionsRoot, sessionID+".jsonl"), []byte(strings.Join(sessionLines, "\n")+"\n"), 0o600); err != nil {
			t.Fatalf("write session %s: %v", sessionID, err)
		}
	}

	phases := []string{}
	cuOpts := contextupdate.Options{
		ProjectID:    projectID,
		SessionsRoot: sessionsRoot,
		DataRoot:     dataRoot,
		Now:          func() time.Time { return scanNow },
		PhaseObserver: func(phase string) error {
			phases = append(phases, phase)
			return nil
		},
	}

	result, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("contextupdate.Run: %v", err)
	}
	if result.GenerationID == "" {
		t.Fatal("expected non-empty GenerationID")
	}
	if result.ReviewRunTokens != 0 {
		t.Fatalf("expected 0 review run tokens, got %d", result.ReviewRunTokens)
	}

	// Verify files in Project and Vault match
	store, err := memorystore.Open(dataRoot, projectID)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	pubID, _, err := store.LoadPublished()
	if err != nil || pubID != result.GenerationID {
		t.Fatalf("published pointer mismatch: pubID=%s resID=%s", pubID, result.GenerationID)
	}

	pReview, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath)))
	if err != nil {
		t.Fatalf("read project review: %v", err)
	}
	vReview, err := os.ReadFile(filepath.Join(vaultRoot, "Projects", projectID, "Session Review", "项目回顾.md"))
	if err != nil {
		t.Fatalf("read vault review: %v", err)
	}
	if string(pReview) != string(vReview) {
		t.Fatal("project and vault review contents differ")
	}
	pHistory, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(reviewv2.HistoryRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	pLedger, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(reviewv2.MachineLedgerRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	pIndex, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(presentation.SessionIndexRelativePath)))
	if err != nil {
		t.Fatalf("new scan did not publish session index: %v", err)
	}
	acceptedV4, err := reviewv4.LoadProjection(pReview, pHistory, pLedger, pIndex)
	if err != nil || acceptedV4.Ledger.DocumentProjection == nil || len(acceptedV4.SessionIndex.Sessions) != 154 {
		t.Fatalf("new scan did not publish accepted v4 Markdown: projection=%+v err=%v", acceptedV4, err)
	}

	// Second run reflects newly created projection files in git status
	result2, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("second contextupdate.Run: %v", err)
	}
	// Third run with stable git status and unchanged inputs is completely idempotent
	result3, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("third contextupdate.Run: %v", err)
	}
	if result3.GenerationID != result2.GenerationID {
		t.Fatalf("generation changed on unchanged third run: got=%s want=%s", result3.GenerationID, result2.GenerationID)
	}

	// Distinct Project/Vault human edits are merged while a genuinely new
	// source session advances the machine index in the same scan publication.
	projectReviewPath := filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	vaultReviewPath := filepath.Join(vaultRoot, "Projects", projectID, "Session Review", "项目回顾.md")
	editV4Field(t, projectReviewPath, reviewv2.ReviewRelativePath, reviewv4.FieldKey{Entity: "project-overview", Name: "status"}, "人工确认状态")
	editV4Field(t, vaultReviewPath, reviewv2.ReviewRelativePath, reviewv4.FieldKey{Entity: "project-overview", Name: "goal"}, "Vault 人工目标")
	session2 := []string{
		`{"timestamp":"` + base.Add(5*time.Minute).Format(time.RFC3339) + `","type":"session_meta","payload":{"id":"session-155","cwd":"` + filepath.ToSlash(projectRoot) + `","source":"codex"}}`,
		`{"timestamp":"` + base.Add(5*time.Minute+time.Second).Format(time.RFC3339) + `","type":"turn_context","payload":{"cwd":"` + filepath.ToSlash(projectRoot) + `","model":"gpt-5"}}`,
	}
	if err := os.WriteFile(filepath.Join(sessionsRoot, "session-155.jsonl"), []byte(strings.Join(session2, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result4, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("human-edit contextupdate.Run: %v", err)
	}
	if result4.GenerationID == result3.GenerationID {
		t.Fatal("new source session did not advance the scan generation")
	}
	for side, root := range map[string]string{
		"project": filepath.Join(projectRoot, "docs", "session-review"),
		"vault":   filepath.Join(vaultRoot, "Projects", projectID, "Session Review"),
	} {
		accepted := loadGateBV4(t, root)
		if accepted.Review.CurrentState.Status != "人工确认状态" || accepted.Review.CurrentState.Goal != "Vault 人工目标" || len(accepted.SessionIndex.Sessions) != 155 {
			t.Fatalf("%s merged scan lost human edits or source facts: review=%+v sessions=%d", side, accepted.Review.CurrentState, len(accepted.SessionIndex.Sessions))
		}
	}
	ledgerBody, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(reviewv2.MachineLedgerRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	machine, err := reviewv4.DecodeLedger(ledgerBody)
	if err != nil {
		t.Fatal(err)
	}
	foundHumanStatus := false
	for _, patch := range machine.HumanPatches {
		if patch.EntityID == "project-overview" && patch.Field == "status" && patch.Operation == "set" && patch.Value != nil && *patch.Value == "人工确认状态" {
			foundHumanStatus = true
		}
	}
	if !foundHumanStatus {
		t.Fatalf("human status patch was not persisted: %+v", machine.HumanPatches)
	}
	if _, err := contextupdate.Run(context.Background(), cuOpts); err != nil {
		t.Fatalf("post-human-edit idempotence run: %v", err)
	}

	// A later wall clock with byte-identical source/project facts reuses the
	// immutable generation and must not rewrite the public Project/Vault projection.
	beforePrepared, _, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	beforePublished, _, err := store.LoadPublished()
	if err != nil {
		t.Fatal(err)
	}
	publicPaths := []string{
		filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath)),
		filepath.Join(projectRoot, filepath.FromSlash(reviewv2.HistoryRelativePath)),
		filepath.Join(projectRoot, filepath.FromSlash(reviewv2.MachineLedgerRelativePath)),
		filepath.Join(vaultRoot, "Projects", projectID, "Session Review", "项目回顾.md"),
		filepath.Join(vaultRoot, "Projects", projectID, "Session Review", "项目历史.md"),
		filepath.Join(vaultRoot, "Projects", projectID, "Session Review", ".session-reviewer", "ledger.json"),
	}
	beforePublic := make(map[string][]byte, len(publicPaths))
	beforeModTime := make(map[string]time.Time, len(publicPaths))
	for _, publicPath := range publicPaths {
		beforePublic[publicPath], err = os.ReadFile(publicPath)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(publicPath)
		if err != nil {
			t.Fatal(err)
		}
		beforeModTime[publicPath] = info.ModTime()
	}
	scanNow = scanNow.Add(time.Minute)
	auditOnly, err := contextupdate.Run(context.Background(), cuOpts)
	if err != nil {
		t.Fatalf("audit-only contextupdate.Run: %v", err)
	}
	afterPrepared, _, err := store.LoadPrepared()
	if err != nil {
		t.Fatal(err)
	}
	afterPublished, _, err := store.LoadPublished()
	if err != nil {
		t.Fatal(err)
	}
	if afterPrepared.GenerationID != beforePrepared.GenerationID || afterPublished != beforePublished || auditOnly.GenerationID != beforePublished {
		t.Fatalf("audit-only generation leaked into public projection: before prepared=%s published=%s; after prepared=%s published=%s result=%s",
			beforePrepared.GenerationID, beforePublished, afterPrepared.GenerationID, afterPublished, auditOnly.GenerationID)
	}
	for _, publicPath := range publicPaths {
		afterBody, err := os.ReadFile(publicPath)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(publicPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(afterBody, beforePublic[publicPath]) || !info.ModTime().Equal(beforeModTime[publicPath]) {
			t.Fatalf("audit-only scan rewrote %s", publicPath)
		}
	}
}

func TestGateBLegacyV3ProjectionStaysOnTheLegacyScanPath(t *testing.T) {
	dataRoot, projectRoot, vaultRoot, sessionsRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	projectID := "project-gate-b-legacy"
	if err := os.WriteFile(filepath.Join(projectRoot, "VERSION"), []byte("v1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "project-fixture.md"), []byte("# fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initializeGateRepository(t, projectRoot)
	mapping := config.ProjectMapping{ID: projectID, Root: projectRoot, VaultRoot: vaultRoot, VaultReviewPath: "Projects/" + projectID + "/Session Review", VaultCaseMode: platform.CaseSensitive}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{mapping}}); err != nil {
		t.Fatal(err)
	}
	seedGateBLegacyV3(t, projectRoot, mapping)

	started := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	lines := []string{
		`{"timestamp":"` + started.Format(time.RFC3339) + `","type":"session_meta","payload":{"id":"legacy-session","cwd":"` + filepath.ToSlash(projectRoot) + `","source":"codex"}}`,
		`{"timestamp":"` + started.Add(time.Second).Format(time.RFC3339) + `","type":"turn_context","payload":{"cwd":"` + filepath.ToSlash(projectRoot) + `","model":"gpt-5"}}`,
	}
	if err := os.WriteFile(filepath.Join(sessionsRoot, "legacy-session.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextupdate.Run(context.Background(), contextupdate.Options{ProjectID: projectID, SessionsRoot: sessionsRoot, DataRoot: dataRoot, Now: func() time.Time { return started.Add(time.Minute) }}); err != nil {
		t.Fatalf("legacy v3 contextupdate.Run: %v", err)
	}
	read := func(relative string) []byte {
		body, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	accepted, err := reviewv2.LoadV3Bytes(read(reviewv2.ReviewRelativePath), read(reviewv2.HistoryRelativePath), read(reviewv2.MachineLedgerRelativePath))
	if err != nil || accepted.State.Review.ProjectID != projectID || len(accepted.State.Machine.Sessions) != 1 {
		t.Fatalf("legacy v3 ordinary scan did not remain valid: accepted=%+v err=%v", accepted, err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(presentation.SessionIndexRelativePath))); !os.IsNotExist(err) {
		t.Fatalf("legacy v3 scan silently published a v4 index: %v", err)
	}
}

func seedGateBLegacyV3(t *testing.T, projectRoot string, mapping config.ProjectMapping) {
	t.Helper()
	view := memory.ProjectView{
		SchemaVersion: 1, ProjectID: mapping.ID, Generation: 1,
		StartedAt: "2026-08-30T00:00:00Z", EndedAt: "2026-08-30T00:00:01Z",
		SessionViewDependencies: []memory.SessionViewDependency{}, ObservationRevisionIDs: []string{},
		ProbeStateDigest: "sha256:" + strings.Repeat("1", 64), DependencyDigest: "sha256:" + strings.Repeat("2", 64),
		WitnessedState: []memory.DerivedRecord{}, DerivedRecords: []memory.DerivedRecord{}, AssociatedUsage: []memory.AssociatedUsage{}, ReducerVersion: "project-view-v1",
	}
	var err error
	view.Digest, err = memory.ProjectViewDigest(view)
	if err != nil {
		t.Fatal(err)
	}
	input := presentation.ProjectInput{
		ProjectView: view, GenerationID: "generation-legacy-seed", Revision: 1,
		Legacy: reviewv2.LegacyPresentation{Review: reviewv2.Review{Name: "Legacy", Goal: "legacy goal", Stage: "legacy", Status: "active", NextAction: "scan"}, Events: []reviewv2.Event{}, Compatibility: reviewv2.LegacyCompatibility{}},
	}
	output, err := presentation.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := presentation.Render(input, output)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range plan.Files {
		full := filepath.Join(projectRoot, filepath.FromSlash(file.Relative))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, file.Desired, file.Mode); err != nil {
			t.Fatal(err)
		}
	}
}

func editV4Field(t *testing.T, fullPath, relative string, key reviewv4.FieldKey, value string) {
	t.Helper()
	body, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatal(err)
	}
	document, err := reviewv4.ParseMarkdownDocument(filepath.Base(relative), body)
	if err != nil {
		t.Fatal(err)
	}
	next, err := document.ReplaceFields(map[reviewv4.FieldKey]string{key: value})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, next, 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadGateBV4(t *testing.T, root string) reviewv4.Accepted {
	t.Helper()
	read := func(relative string) []byte {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	accepted, err := reviewv4.LoadProjection(read("项目回顾.md"), read("项目历史.md"), read(".session-reviewer/ledger.json"), read(".session-reviewer/session-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}
