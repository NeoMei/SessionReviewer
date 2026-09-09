package contextupdate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/contextupdate"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func TestRunPublishesQualifiedMilestoneAndKeepsIdenticalScanByteStable(t *testing.T) {
	projectRoot, vaultRoot, dataRoot, sessionsRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, ".gitignore"), []byte("docs/session-review/\n.session-reviewer-directory.lock\n**/.session-reviewer-directory.lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "README.md"), []byte("# fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = projectRoot
		if args[0] == "-c" {
			add := exec.Command("git", "add", ".gitignore", "README.md")
			add.Dir = projectRoot
			if out, err := add.CombinedOutput(); err != nil {
				t.Fatalf("fixture git add: %v: %s", err, out)
			}
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git %v: %v: %s", args, err, out)
		}
	}
	const projectID = "project-milestone-scan"
	const sessionID = "88888888-8888-4888-8888-888888888888"
	mapping := config.ProjectMapping{ID: projectID, Root: projectRoot, VaultRoot: vaultRoot, VaultReviewPath: "Projects/Milestone/Session Review", VaultCaseMode: platform.CaseSensitive}
	if err := config.Save(filepath.Join(dataRoot, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{mapping}}); err != nil {
		t.Fatal(err)
	}
	var source bytes.Buffer
	add := func(stamp, kind string, payload map[string]any) {
		t.Helper()
		if err := json.NewEncoder(&source).Encode(map[string]any{"timestamp": stamp, "type": kind, "payload": payload}); err != nil {
			t.Fatal(err)
		}
	}
	message := func(role, body string) map[string]any {
		kind := "input_text"
		if role == "assistant" {
			kind = "output_text"
		}
		return map[string]any{"type": "message", "role": role, "content": []any{map[string]any{"type": kind, "text": body}}}
	}
	add("2026-09-08T00:00:00Z", "session_meta", map[string]any{"id": sessionID, "cwd": projectRoot})
	add("2026-09-08T00:00:01Z", "response_item", message("user", "Verify the implementation."))
	add("2026-09-08T00:00:02Z", "response_item", map[string]any{"type": "function_call", "call_id": "check-one", "name": "exec_command", "arguments": `{"cmd":"go test ./..."}`})
	add("2026-09-08T00:00:03Z", "response_item", map[string]any{"type": "function_call_output", "call_id": "check-one", "output": `{"exit_code":0,"output":"PASS"}`})
	answer := message("assistant", "Original bounded Agent conclusion.")
	answer["phase"] = "final_answer"
	add("2026-09-08T00:00:04Z", "response_item", answer)
	if err := os.WriteFile(filepath.Join(sessionsRoot, "rollout-2026-09-08T00-00-00-"+sessionID+".jsonl"), source.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	userOnly := strings.Join([]string{
		`{"timestamp":"2026-09-08T00:00:00Z","type":"session_meta","payload":{"id":"99999999-9999-4999-8999-999999999999","cwd":"` + filepath.ToSlash(projectRoot) + `"}}`,
		`{"timestamp":"2026-09-08T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Question without a qualifying machine fact."}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(sessionsRoot, "rollout-2026-09-08T00-00-00-99999999-9999-4999-8999-999999999999.jsonl"), []byte(userOnly), 0o600); err != nil {
		t.Fatal(err)
	}
	nowCalls := 0
	opts := contextupdate.Options{ProjectID: projectID, SessionsRoot: sessionsRoot, DataRoot: dataRoot, Now: func() time.Time {
		nowCalls++
		return time.Date(2026, 9, 8, 0, 1+nowCalls, 0, 0, time.UTC)
	}}
	if _, err := contextupdate.Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	paths := []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath, presentation.SessionIndexRelativePath}
	type publicFile struct{ side, root, relative string }
	publicFiles := make([]publicFile, 0, len(paths)*2)
	for _, relative := range paths {
		publicFiles = append(publicFiles,
			publicFile{side: "project", root: projectRoot, relative: relative},
			publicFile{side: "vault", root: vaultRoot, relative: filepath.ToSlash(filepath.Join(mapping.VaultReviewPath, strings.TrimPrefix(relative, "docs/session-review/")))},
		)
	}
	before := make(map[string][]byte, len(publicFiles))
	for _, file := range publicFiles {
		body, err := os.ReadFile(filepath.Join(file.root, filepath.FromSlash(file.relative)))
		if err != nil {
			t.Fatal(err)
		}
		before[file.side+"\x00"+file.relative] = body
	}
	accepted, err := reviewv4.LoadProjection(before["project\x00"+reviewv2.ReviewRelativePath], before["project\x00"+reviewv2.HistoryRelativePath], before["project\x00"+reviewv2.MachineLedgerRelativePath], before["project\x00"+presentation.SessionIndexRelativePath])
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted.Review.Timeline) != 1 || accepted.Review.Timeline[0].ClosedLoop.Conclusion.Text != "Original bounded Agent conclusion." || len(accepted.Review.GeneratedBaselines) != 4 {
		t.Fatalf("qualified scan milestone was not seeded with exact baselines: timeline=%+v baselines=%+v", accepted.Review.Timeline, accepted.Review.GeneratedBaselines)
	}
	if len(accepted.Review.ChainDependencies) != 1 || accepted.Review.ChainDependencies[0].SessionID != sessionID {
		t.Fatalf("user-only Session created milestone evidence: dependencies=%+v", accepted.Review.ChainDependencies)
	}
	if _, err := contextupdate.Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	for _, file := range publicFiles {
		after, err := os.ReadFile(filepath.Join(file.root, filepath.FromSlash(file.relative)))
		if err != nil || !bytes.Equal(before[file.side+"\x00"+file.relative], after) {
			status := exec.Command("git", "status", "--porcelain=v1", "--ignored", "--untracked-files=all")
			status.Dir = projectRoot
			statusBody, _ := status.CombinedOutput()
			t.Fatalf("identical scan changed %s %s: %v\ngit status:\n%s", file.side, file.relative, err, statusBody)
		}
	}
	originalMilestone := accepted.Review.Timeline[0]
	originalRefs := append([]reviewv4.SourceTurnRef(nil), originalMilestone.ClosedLoop.Conclusion.SourceTurnRefs...)
	vaultReviewRelative := filepath.ToSlash(filepath.Join(mapping.VaultReviewPath, strings.TrimPrefix(reviewv2.ReviewRelativePath, "docs/session-review/")))
	vaultHistoryRelative := filepath.ToSlash(filepath.Join(mapping.VaultReviewPath, strings.TrimPrefix(reviewv2.HistoryRelativePath, "docs/session-review/")))
	vaultReviewPath := filepath.Join(vaultRoot, filepath.FromSlash(vaultReviewRelative))
	vaultHistoryPath := filepath.Join(vaultRoot, filepath.FromSlash(vaultHistoryRelative))
	vaultReview, err := reviewv4.ParseMarkdownDocument(reviewv2.ReviewRelativePath, mustReadMilestoneFile(t, vaultReviewPath))
	if err != nil {
		t.Fatal(err)
	}
	editedReview, err := vaultReview.ReplaceFields(map[reviewv4.FieldKey]string{{Entity: "project-overview", Name: "goal"}: "Human-owned scan goal"})
	if err != nil {
		t.Fatal(err)
	}
	vaultHistory, err := reviewv4.ParseMarkdownDocument(reviewv2.HistoryRelativePath, mustReadMilestoneFile(t, vaultHistoryPath))
	if err != nil {
		t.Fatal(err)
	}
	editedHistory, err := vaultHistory.ReplaceFields(map[reviewv4.FieldKey]string{{Entity: "milestone:" + originalMilestone.ID, Name: "conclusion"}: "Human-confirmed scan conclusion"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultReviewPath, editedReview, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vaultHistoryPath, editedHistory, 0o600); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(sessionsRoot, "rollout-2026-09-08T00-00-00-"+sessionID+".jsonl")
	appended := strings.Join([]string{
		`{"timestamp":"2026-09-08T00:00:05Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Verify the follow-up."}]}}`,
		`{"timestamp":"2026-09-08T00:00:06Z","type":"response_item","payload":{"type":"function_call","call_id":"check-two","name":"exec_command","arguments":"{\"cmd\":\"go test ./internal/...\"}"}}`,
		`{"timestamp":"2026-09-08T00:00:07Z","type":"response_item","payload":{"type":"function_call_output","call_id":"check-two","output":"{\"exit_code\":0,\"output\":\"PASS follow-up\"}"}}`,
		`{"timestamp":"2026-09-08T00:00:08Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Follow-up bounded Agent conclusion."}]}}`,
	}, "\n") + "\n"
	file, err := os.OpenFile(sourcePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(appended); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := contextupdate.Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	humanAccepted := loadMilestoneProjection(t, projectRoot, paths)
	vaultAccepted := loadMilestoneProjection(t, vaultRoot, []string{vaultReviewRelative, vaultHistoryRelative, filepath.ToSlash(filepath.Join(mapping.VaultReviewPath, ".session-reviewer/ledger.json")), filepath.ToSlash(filepath.Join(mapping.VaultReviewPath, ".session-reviewer/session-index.json"))})
	if humanAccepted.Review.CurrentState.Goal != "Human-owned scan goal" || humanAccepted.Review.Timeline[0].ID != originalMilestone.ID || humanAccepted.Review.Timeline[0].ClosedLoop.Conclusion.Text != "Human-confirmed scan conclusion" || humanAccepted.Review.Timeline[0].ClosedLoop.Conclusion.Kind != reviewv4.ConclusionHumanConfirmed {
		t.Fatalf("scan lost human edits: %+v", humanAccepted.Review)
	}
	if !bytes.Equal(mustReadMilestoneFile(t, filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))), mustReadMilestoneFile(t, vaultReviewPath)) || !bytes.Equal(mustReadMilestoneFile(t, filepath.Join(projectRoot, filepath.FromSlash(reviewv2.HistoryRelativePath))), mustReadMilestoneFile(t, vaultHistoryPath)) {
		t.Fatal("human-edited Project/Vault Markdown did not converge byte-for-byte")
	}
	if humanAccepted.Review.CurrentState.Goal != vaultAccepted.Review.CurrentState.Goal || humanAccepted.Review.Timeline[0].ClosedLoop.Conclusion.Text != vaultAccepted.Review.Timeline[0].ClosedLoop.Conclusion.Text || len(humanAccepted.Review.HumanPatches) < 2 || len(humanAccepted.Review.GeneratedBaselines) < 4 || !reflect.DeepEqual(humanAccepted.Review.Timeline[0].ClosedLoop.Conclusion.SourceTurnRefs, originalRefs) {
		t.Fatalf("human patch/baseline/ref metadata was not preserved: project=%+v vault=%+v", humanAccepted.Review, vaultAccepted.Review)
	}
	afterHuman := make(map[string][]byte, len(publicFiles))
	for _, public := range publicFiles {
		afterHuman[public.side+"\x00"+public.relative] = mustReadMilestoneFile(t, filepath.Join(public.root, filepath.FromSlash(public.relative)))
	}
	if _, err := contextupdate.Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	for _, public := range publicFiles {
		if got := mustReadMilestoneFile(t, filepath.Join(public.root, filepath.FromSlash(public.relative))); !bytes.Equal(got, afterHuman[public.side+"\x00"+public.relative]) {
			t.Fatalf("post-human no-op changed %s %s", public.side, public.relative)
		}
	}
	awayPath := filepath.Join(dataRoot, "source-away")
	if err := os.Rename(sourcePath, awayPath); err != nil {
		t.Fatal(err)
	}
	if _, err := contextupdate.Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	unavailable := loadMilestoneProjection(t, projectRoot, paths)
	if unavailable.Review.Timeline[0].ClosedLoop.Conclusion.Text != "Human-confirmed scan conclusion" || !reflect.DeepEqual(unavailable.Review.Timeline[0].ClosedLoop.Conclusion.SourceTurnRefs, originalRefs) || milestoneSourceAvailability(unavailable, sessionID) != "unavailable" {
		t.Fatalf("source loss discarded accepted human history or exact refs: %+v", unavailable)
	}
	if err := os.Rename(awayPath, sourcePath); err != nil {
		t.Fatal(err)
	}
	if _, err := contextupdate.Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	restored := loadMilestoneProjection(t, projectRoot, paths)
	if restored.Review.Timeline[0].ClosedLoop.Conclusion.Text != "Human-confirmed scan conclusion" || !reflect.DeepEqual(restored.Review.Timeline[0].ClosedLoop.Conclusion.SourceTurnRefs, originalRefs) || milestoneSourceAvailability(restored, sessionID) != "available" {
		t.Fatalf("source restoration changed accepted human history or exact refs: %+v", restored)
	}
	beforeCancellation := make(map[string][]byte, len(publicFiles))
	for _, public := range publicFiles {
		beforeCancellation[public.side+"\x00"+public.relative] = mustReadMilestoneFile(t, filepath.Join(public.root, filepath.FromSlash(public.relative)))
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancelOpts := opts
	cancelOpts.PhaseObserver = func(phase string) error {
		if phase == "rendering" {
			cancel()
		}
		return nil
	}
	if _, err := contextupdate.Run(cancelled, cancelOpts); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation before rendering was not propagated: %v", err)
	}
	for _, public := range publicFiles {
		if got := mustReadMilestoneFile(t, filepath.Join(public.root, filepath.FromSlash(public.relative))); !bytes.Equal(got, beforeCancellation[public.side+"\x00"+public.relative]) {
			t.Fatalf("cancellation before rendering changed %s %s", public.side, public.relative)
		}
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "README.md"), []byte("# changed fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextupdate.Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	afterProjectChange := func() reviewv4.Accepted {
		bodies := make(map[string][]byte, len(paths))
		for _, relative := range paths {
			body, readErr := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(relative)))
			if readErr != nil {
				t.Fatal(readErr)
			}
			bodies[relative] = body
		}
		value, loadErr := reviewv4.LoadProjection(bodies[reviewv2.ReviewRelativePath], bodies[reviewv2.HistoryRelativePath], bodies[reviewv2.MachineLedgerRelativePath], bodies[presentation.SessionIndexRelativePath])
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		return value
	}()
	if afterProjectChange.Review.ProjectViewDigest == accepted.Review.ProjectViewDigest || afterProjectChange.Review.Revision <= accepted.Review.Revision {
		t.Fatal("changed tracked project state was suppressed as an identity-only no-op")
	}
}

func mustReadMilestoneFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func loadMilestoneProjection(t *testing.T, root string, relatives []string) reviewv4.Accepted {
	t.Helper()
	if len(relatives) != 4 {
		t.Fatal("projection fixture requires four files")
	}
	accepted, err := reviewv4.LoadProjection(
		mustReadMilestoneFile(t, filepath.Join(root, filepath.FromSlash(relatives[0]))),
		mustReadMilestoneFile(t, filepath.Join(root, filepath.FromSlash(relatives[1]))),
		mustReadMilestoneFile(t, filepath.Join(root, filepath.FromSlash(relatives[2]))),
		mustReadMilestoneFile(t, filepath.Join(root, filepath.FromSlash(relatives[3]))),
	)
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func milestoneSourceAvailability(accepted reviewv4.Accepted, sessionID string) string {
	for _, session := range accepted.Ledger.Sessions {
		if session.SessionID == sessionID {
			return session.SourceAvailability
		}
	}
	return ""
}
