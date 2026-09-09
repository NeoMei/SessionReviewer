package publication

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

func TestExpectedMarkdownResultFingerprintReadsAcceptedArchive(t *testing.T) {
	env := setupMarkdownPublication(t, "project-result-fingerprint")
	fingerprint := publishAcceptedResultForTest(t, env, "fingerprinted goal")

	reader, err := publicationstate.OpenReadOnly(env.dataRoot, env.projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	receipt, err := reader.AcceptedMarkdownResult(fingerprint)
	if err != nil {
		t.Fatalf("read accepted result %s: %v", fingerprint, err)
	}
	latest := loadAcceptedReceiptForTest(t, env)
	if receipt.RevisionID != latest.RevisionID || receipt.GenerationID != env.manifest.GenerationID || receipt.ProjectViewDigest != env.manifest.ProjectViewDigest {
		t.Fatalf("archived receipt does not match accepted candidate: archive=%+v latest=%+v", receipt, latest)
	}
}

func TestMarkdownRecoveryCompletesAcceptedResultArchive(t *testing.T) {
	env := setupMarkdownPublication(t, "project-result-recovery")
	accepted := loadMarkdownProjectionForTest(t, env)
	projectReview := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	updated := replaceMarkdownFieldForTest(t, readTestFile(t, projectReview), "project-overview", "goal", accepted.Review.CurrentState.Goal, "archive recovery goal")
	if err := os.WriteFile(projectReview, updated, 0o644); err != nil {
		t.Fatal(err)
	}
	edit := captureMarkdownPlanForTest(t, env)
	fingerprint, err := ExpectedMarkdownResultFingerprint(edit.Plan, env.mapping, edit.Index)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := acceptedResultPathForTest(env, fingerprint)
	if err := os.Mkdir(archivePath, 0o700); err != nil {
		t.Fatal(err)
	}

	owner, err := publicationlock.Acquire(env.dataRoot, env.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	if _, err := PublishMarkdownEditLocked(context.Background(), env.publishOptions(), edit, owner); err == nil || !strings.Contains(err.Error(), "archive accepted Markdown result") {
		t.Fatalf("archive obstruction did not fail after receipt commit: %v", err)
	}

	journal, err := OpenJournal(env.dataRoot, env.projectID)
	if err != nil {
		t.Fatal(err)
	}
	intent, intentErr := journal.Load()
	receipt, receiptErr := journal.LoadAcceptedMarkdown()
	closeErr := journal.Close()
	if err := errors.Join(intentErr, receiptErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if intent.Stage != StageBaseCommitted || intent.Outcome != "" || receipt.RevisionID != intent.RevisionID {
		t.Fatalf("archive failure changed acceptance boundary: intent=%+v receipt=%+v", intent, receipt)
	}

	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	if err := RecoverMarkdownLocked(context.Background(), env.publishOptions(), owner); err != nil {
		t.Fatalf("recover accepted result archive: %v", err)
	}
	journal, err = OpenJournal(env.dataRoot, env.projectID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredIntent, intentErr := journal.Load()
	recoveredReceipt, receiptErr := journal.LoadAcceptedMarkdown()
	closeErr = journal.Close()
	if err := errors.Join(intentErr, receiptErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if recoveredIntent.Stage != StageCommitted || recoveredIntent.Outcome != OutcomeAccepted || recoveredReceipt.RevisionID != receipt.RevisionID {
		t.Fatalf("recovery did not preserve accepted revision: intent=%+v receipt=%+v", recoveredIntent, recoveredReceipt)
	}
	reader, err := publicationstate.OpenReadOnly(env.dataRoot, env.projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	archived, err := reader.AcceptedMarkdownResult(fingerprint)
	if err != nil || archived.RevisionID != receipt.RevisionID {
		t.Fatalf("recovered archive=%+v err=%v", archived, err)
	}
	if got := loadMarkdownProjectionForTest(t, env); got.Review.CurrentState.Goal != "archive recovery goal" {
		t.Fatalf("recovery rolled back accepted presentation: goal=%q", got.Review.CurrentState.Goal)
	}
}

func TestAcceptedMarkdownResultRejectsForgedLocatorAndTampering(t *testing.T) {
	env := setupMarkdownPublication(t, "project-result-tamper")
	fingerprint := publishAcceptedResultForTest(t, env, "tamper goal")
	archivePath := acceptedResultPathForTest(env, fingerprint)
	body, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}

	reader, err := publicationstate.OpenReadOnly(env.dataRoot, env.projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	forged := "sha256:" + strings.Repeat("0", 64)
	if err := os.WriteFile(acceptedResultPathForTest(env, forged), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.AcceptedMarkdownResult(forged); err == nil {
		t.Fatal("accepted valid receipt under a forged result fingerprint")
	}

	receipt, err := reader.AcceptedMarkdownResult(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(body, []byte(receipt.Destinations[0].DesiredSHA256), []byte(strings.Repeat("f", 64)), 1)
	if bytes.Equal(tampered, body) {
		t.Fatal("tamper fixture did not change archived receipt")
	}
	if err := os.WriteFile(archivePath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.AcceptedMarkdownResult(fingerprint); err == nil {
		t.Fatal("accepted tampered result archive")
	}
}

func publishAcceptedResultForTest(t *testing.T, env markdownPublicationTestEnv, goal string) string {
	t.Helper()
	accepted := loadMarkdownProjectionForTest(t, env)
	projectReview := filepath.Join(env.projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath))
	updated := replaceMarkdownFieldForTest(t, readTestFile(t, projectReview), "project-overview", "goal", accepted.Review.CurrentState.Goal, goal)
	if err := os.WriteFile(projectReview, updated, 0o644); err != nil {
		t.Fatal(err)
	}
	edit := captureMarkdownPlanForTest(t, env)
	fingerprint, err := ExpectedMarkdownResultFingerprint(edit.Plan, env.mapping, edit.Index)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := publicationlock.Acquire(env.dataRoot, env.projectID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, publishErr := PublishMarkdownEditLocked(context.Background(), env.publishOptions(), edit, owner); publishErr != nil {
		_ = owner.Release()
		t.Fatal(publishErr)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func acceptedResultPathForTest(env markdownPublicationTestEnv, fingerprint string) string {
	return filepath.Join(env.dataRoot, "publication-journal", env.projectID, "accepted-result-"+strings.TrimPrefix(fingerprint, "sha256:")+".json")
}
