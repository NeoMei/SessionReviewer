package apply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
)

func TestTrustsAppliedTransitionRequiresCompleteReceiptChain(t *testing.T) {
	dataDir := t.TempDir()
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	relative := "docs/session-review/current-state.md"
	base, middle, target := []byte("base\n"), []byte("middle\n"), []byte("target\n")
	writeAppliedTransitionReceipt(t, root, "first", relative, true, base, middle)
	writeAppliedTransitionReceipt(t, root, "second", relative, true, middle, target)

	trusted, err := TrustsAppliedTransition(root, testProjectID, relative, true, contentHash(base), contentHash(target))
	if err != nil || !trusted {
		t.Fatalf("trusted=%t err=%v", trusted, err)
	}

	untrusted, err := TrustsAppliedTransition(root, testProjectID, relative, true, contentHash([]byte("unrelated\n")), contentHash(target))
	if err != nil || untrusted {
		t.Fatalf("disconnected transition trusted=%t err=%v", untrusted, err)
	}
}

func TestTrustsAppliedTransitionSupportsNewFilesAndRejectsOldReceiptDowngrade(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	relative := "docs/session-review/decisions/new.md"
	first, second := []byte("first\n"), []byte("second\n")
	writeAppliedTransitionReceipt(t, root, "create", relative, false, nil, first)
	writeAppliedTransitionReceipt(t, root, "advance", relative, true, first, second)

	trusted, err := TrustsAppliedTransition(root, testProjectID, relative, false, "", contentHash(second))
	if err != nil || !trusted {
		t.Fatalf("new-file chain trusted=%t err=%v", trusted, err)
	}
	downgrade, err := TrustsAppliedTransition(root, testProjectID, relative, true, contentHash(second), contentHash(first))
	if err != nil || downgrade {
		t.Fatalf("old receipt enabled downgrade=%t err=%v", downgrade, err)
	}
}

func TestTrustsAppliedTransitionFailsClosedOnTamperedReceipt(t *testing.T) {
	dataDir := t.TempDir()
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	relative := "docs/session-review/current-state.md"
	base, target := []byte("base\n"), []byte("target\n")
	writeAppliedTransitionReceipt(t, root, "tamper", relative, true, base, target)
	entries, err := os.ReadDir(filepath.Join(dataDir, "applied-proposals"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	receiptPath := filepath.Join(dataDir, "applied-proposals", entries[0].Name())
	body, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte(`"receipt_sha256": "sha256:`)
	index := strings.Index(string(body), string(marker))
	if index < 0 {
		t.Fatal("receipt digest marker is missing")
	}
	digestIndex := index + len(marker)
	if body[digestIndex] == '0' {
		body[digestIndex] = '1'
	} else {
		body[digestIndex] = '0'
	}
	if err := os.WriteFile(receiptPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	if trusted, err := TrustsAppliedTransition(root, testProjectID, relative, true, contentHash(base), contentHash(target)); err == nil || trusted {
		t.Fatalf("tampered receipt trusted=%t err=%v", trusted, err)
	}
}

func contentHash(body []byte) string {
	return strings.TrimPrefix(digestBytes(body), "sha256:")
}

func writeAppliedTransitionReceipt(t *testing.T, root *os.Root, label, relative string, preimageExists bool, preimage, target []byte) {
	t.Helper()
	proposalDigest := digestBytes([]byte("proposal-" + label))
	boundaryHash := strings.TrimPrefix(digestBytes([]byte("boundary-"+label)), "sha256:")
	ctx := inputContext{Packet: evidence.Packet{
		ProjectID: testProjectID, SessionID: testSessionID, FromCursor: 1, ToCursor: 1,
		ExpectedCursor: evidence.CursorBoundary{}, NextCursor: evidence.CursorBoundary{Line: 1, SourceHash: boundaryHash},
	}, ProposalDigest: proposalDigest, EvidenceFileDigest: digestBytes([]byte("evidence-file-" + label)), EvidencePacketDigest: digestBytes([]byte("packet-" + label))}
	plan := ledger.WritePlan{Files: []ledger.PlannedFile{{
		RelativePath: relative, Data: target, Perm: 0o644,
		ExpectedExists: preimageExists, ExpectedData: preimage, ExpectedPerm: 0o644,
	}}}
	receipt, err := newPreparedReceipt(ctx, plan, digestBytes([]byte("snapshot-"+label)))
	if err != nil {
		t.Fatal(err)
	}
	receipt.State = receiptApplied
	receipt.ChangedFiles = receiptPlannedChanges(receipt)
	if err := saveReceipt(root, receipt); err != nil {
		t.Fatal(err)
	}
}
