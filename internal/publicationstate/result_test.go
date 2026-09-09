package publicationstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resultTestIntent() Intent {
	hash := strings.Repeat("a", 64)
	digest := "sha256:" + hash
	i := Intent{Version: 2, Kind: KindMarkdown, ProjectID: "project-result", GenerationID: "scan-one", ManifestDigest: digest, ProjectViewDigest: digest, Stage: StageBaseCommitted, CreatedAt: time.Now().UTC(), BaseDesiredDigest: hash, Destinations: []Destination{{Side: "project", Relative: "docs/session-review/项目回顾.md", DesiredSHA256: hash}}, IndexGuard: &IndexGuard{Relative: "docs/session-review/.session-reviewer/session-index.json", VaultRelative: "Projects/P/Session Review/.session-reviewer/session-index.json", ProjectSHA256: hash, VaultSHA256: hash, Digest: digest, GenerationID: "scan-one"}}
	i.RevisionID = MarkdownRevisionID(i)
	return i
}

func TestAcceptedResultSurvivesLaterPublication(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	first := resultTestIntent()
	if _, err := WriteAccepted(root, first); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "accepted-result-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("accepted result not durably archived: files=%v err=%v", files, err)
	}
	before, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	next := first
	next.Destinations = cloneDestinations(first.Destinations)
	next.Destinations[0].DesiredSHA256 = strings.Repeat("b", 64)
	next.RevisionID = MarkdownRevisionID(next)
	if _, err := WriteAccepted(root, next); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(files[0])
	if err != nil || string(before) != string(after) {
		t.Fatal("later publication erased original accepted result")
	}
}

func TestAcceptedResultRejectsTamperingAndPreservesFirstReceipt(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	intent := resultTestIntent()
	receipt, err := WriteAccepted(root, intent)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := resultFingerprint(receipt)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := resultLeaf(fingerprint)
	before, err := os.ReadFile(filepath.Join(dir, leaf))
	if err != nil {
		t.Fatal(err)
	}
	// The same result reached from a different preimage must preserve its first receipt.
	intent.Destinations[0].PreimageExists = true
	intent.Destinations[0].PreimageSHA256 = strings.Repeat("c", 64)
	intent.RevisionID = MarkdownRevisionID(intent)
	if _, err := WriteAccepted(root, intent); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, leaf))
	if string(before) != string(after) {
		t.Fatal("archive was overwritten by a later equivalent result")
	}
	if _, err := ReadAcceptedResult(root, "other-project", fingerprint); err == nil {
		t.Fatal("accepted foreign project proof")
	}
	if _, err := ReadAcceptedResult(root, receipt.ProjectID, "../escape"); err == nil {
		t.Fatal("accepted malformed fingerprint")
	}
	receipt.Destinations[0].DesiredSHA256 = strings.Repeat("d", 64)
	forged, _ := canonical(receipt)
	if err := os.WriteFile(filepath.Join(dir, leaf), forged, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAcceptedResult(root, receipt.ProjectID, fingerprint); err == nil {
		t.Fatal("accepted altered archived receipt")
	}
}

func TestAcceptedResultArchiveFailureDoesNotPrecedeReceiptCommit(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	intent := resultTestIntent()
	receipt, err := ReceiptFromIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, _ := resultFingerprint(receipt)
	leaf, _ := resultLeaf(fingerprint)
	// Block only the archive destination. The existing receipt remains the commit point.
	if err := os.Mkdir(filepath.Join(dir, leaf), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteAccepted(root, intent); err == nil {
		t.Fatal("ignored archive write failure")
	}
	current, err := ReadAccepted(root, intent.ProjectID)
	if err != nil || current.RevisionID != intent.RevisionID {
		t.Fatalf("receipt commit missing: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, leaf)); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteAccepted(root, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAcceptedResult(root, intent.ProjectID, fingerprint); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedReceiptWriteCreatesNoAcceptanceProof(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Mkdir(filepath.Join(dir, AcceptedReceiptLeaf), 0o700); err != nil {
		t.Fatal(err)
	}
	intent := resultTestIntent()
	if _, err := WriteAccepted(root, intent); err == nil {
		t.Fatal("accepted blocked receipt write")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "accepted-result-*.json"))
	if len(files) != 0 {
		t.Fatalf("uncommitted operation gained acceptance proof: %v", files)
	}
}
