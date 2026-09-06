package publication

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

func TestMarkdownVaultBlockCommentEditStatusSyncReopenAndRejectInvalid(t *testing.T) {
	dataRoot, projectRoot, vaultRoot, mapping, manifest, legacy := setupPublishEnvWithIndex(t, "project-block-comments", true)
	plan := validMarkdownPublicationPlanForTest(t, dataRoot, manifest, legacy)
	for i := range plan.Files {
		file := &plan.Files[i]
		if file.Relative == reviewv2.ReviewRelativePath {
			file.Desired = bytes.Replace(file.Desired, []byte("---\n"), []byte("---\n# source leading comment\n\n"), 1)
			file.Desired = bytes.Replace(file.Desired, []byte("\n---\n"), []byte("\ncustom_owner: one\n\n# keep B\ncustom_keep: two\n---\n"), 1)
		}
	}
	for i := range plan.Files {
		if plan.Files[i].Relative != reviewv2.MachineLedgerRelativePath {
			continue
		}
		ledger, err := reviewv4.DecodeLedger(plan.Files[i].Desired)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range plan.Files {
			if file.Relative == reviewv2.ReviewRelativePath {
				ledger.ReviewSHA256 = sha256Hex(file.Desired)
				ledger.SyncHashes.ReviewSHA256 = ledger.ReviewSHA256
			}
		}
		plan.Files[i].Desired, err = reviewv4.RenderLedger(ledger)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Publish(t.Context(), Options{ProjectID: manifest.ProjectID, PreparedGeneration: manifest.GenerationID, Plan: plan, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}); err != nil {
		t.Fatal(err)
	}
	env := markdownPublicationTestEnv{projectID: manifest.ProjectID, dataRoot: dataRoot, projectRoot: projectRoot, vaultRoot: vaultRoot, mapping: mapping, manifest: manifest, plan: plan}
	projectReview := filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	vaultReview := filepath.Join(vaultRoot, filepath.FromSlash(vaultRelativePath(mapping.VaultReviewPath, reviewv2.ReviewRelativePath)))
	indexes := map[string][]byte{}
	mtimes := map[string]time.Time{}
	for _, path := range []string{filepath.Join(projectRoot, filepath.FromSlash(sessionIndexRelativePath)), filepath.Join(vaultRoot, filepath.FromSlash(vaultRelativePath(mapping.VaultReviewPath, sessionIndexRelativePath)))} {
		indexes[path] = readTestFile(t, path)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		mtimes[path] = info.ModTime()
	}
	vaultEdit := bytes.Replace(readTestFile(t, vaultReview), []byte("custom_owner: one"), []byte("custom_owner: changed"), 1)
	if err := os.WriteFile(vaultReview, vaultEdit, 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := syncproject.StatusMarkdown(t.Context(), env.syncOptions(false))
	if err != nil || len(status.Pending) != 6 || status.Conflicted != 0 || status.Malformed != 0 {
		t.Fatalf("pending status=%+v err=%v", status, err)
	}
	publishes := 0
	options := env.syncOptions(false)
	options.RecoverMarkdown = func(ctx context.Context, owner *publicationlock.Owner) error {
		return RecoverMarkdownLocked(ctx, env.publishOptions(), owner)
	}
	options.PublishMarkdown = func(ctx context.Context, edit syncproject.MarkdownSyncPlan, owner *publicationlock.Owner) error {
		publishes++
		_, err := PublishMarkdownEditLocked(ctx, env.publishOptions(), edit, owner)
		return err
	}
	if _, err := syncproject.RunMarkdown(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(vaultEdit, []byte("revision: 1\n"), []byte("revision: 2\n"), 1)
	for _, path := range []string{projectReview, vaultReview} {
		if !bytes.Equal(readTestFile(t, path), want) {
			t.Fatalf("published block comments changed: %s", path)
		}
	}
	accepted := loadMarkdownProjectionForTest(t, env)
	if accepted.Review.Revision != 2 || accepted.Review.GenerationID != manifest.GenerationID {
		t.Fatalf("revision/generation changed: %+v", accepted.Review)
	}
	if status, err := syncproject.StatusMarkdown(t.Context(), env.syncOptions(false)); err != nil || status.InSync != 1 {
		t.Fatalf("reopened status=%+v err=%v", status, err)
	}
	if _, err := syncproject.RunMarkdown(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if publishes != 1 {
		t.Fatalf("no-op publication calls=%d", publishes)
	}
	for _, path := range []string{projectReview, vaultReview} {
		if !bytes.Equal(readTestFile(t, path), want) {
			t.Fatal("reopen/no-op changed paired bytes")
		}
	}
	for path, before := range indexes {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(readTestFile(t, path), before) || !info.ModTime().Equal(mtimes[path]) {
			t.Fatalf("index changed: %s", path)
		}
	}
	receipt, base := loadAcceptedReceiptForTest(t, env), loadMarkdownBaseForTest(t, env)
	invalid := bytes.Replace(want, []byte("custom_owner: changed"), []byte("custom_owner: [invalid"), 1)
	if err := os.WriteFile(vaultReview, invalid, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := syncproject.StatusMarkdown(t.Context(), env.syncOptions(false)); err == nil {
		t.Fatal("invalid status accepted")
	}
	if _, err := syncproject.RunMarkdown(t.Context(), options); err == nil {
		t.Fatal("invalid sync accepted")
	}
	if publishes != 1 || !bytes.Equal(readTestFile(t, projectReview), want) || !bytes.Equal(readTestFile(t, vaultReview), invalid) || !reflect.DeepEqual(receipt, loadAcceptedReceiptForTest(t, env)) || !reflect.DeepEqual(base, loadMarkdownBaseForTest(t, env)) {
		t.Fatal("invalid edit changed accepted state or draft")
	}
}
