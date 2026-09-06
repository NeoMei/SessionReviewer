package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

// The real CLI must route authenticated Markdown status without invoking the
// legacy writer, and must never turn a refused read into successful JSON.
func TestSyncCLIMarkdownStatusReadOnly(t *testing.T) {
	for _, name := range []string{"clean", "pending", "conflict", "generated edit", "missing binding", "pending journal", "missing locks", "missing private lock directory", "missing journal directory"} {
		t.Run(name, func(t *testing.T) {
			f := newCLIAuthenticatedMarkdownFixture(t)
			projectReview := filepath.Join(f.project, filepath.FromSlash(reviewv2.ReviewRelativePath))
			vaultReview := filepath.Join(f.vault, "Projects/Markdown/Session Review/项目回顾.md")
			edit := func(path, old, next string) {
				t.Helper()
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				after := bytes.Replace(body, []byte(old), []byte(next), 1)
				if bytes.Equal(body, after) {
					t.Fatal("fixture edit did not change bytes")
				}
				if err := os.WriteFile(path, after, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "pending", "conflict":
				edit(projectReview, "authenticated Markdown migration", "Project draft")
				if name == "conflict" {
					edit(vaultReview, "authenticated Markdown migration", "Vault draft")
				}
			case "generated edit":
				marker := "<!-- session-reviewer:v4-generated entity=\"project-overview\" name=\"problem-tree\" -->\n"
				edit(vaultReview, marker, marker+"unauthorized generated content\n")
			case "missing binding":
				if err := os.Remove(filepath.Join(f.data, "publication-journal", f.projectID, publicationstate.AcceptedReceiptLeaf)); err != nil {
					t.Fatal(err)
				}
			case "pending journal":
				path := filepath.Join(f.data, "publication-journal", f.projectID, publicationstate.IntentLeaf)
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var intent publicationstate.Intent
				if err := json.Unmarshal(body, &intent); err != nil {
					t.Fatal(err)
				}
				intent.Stage = publicationstate.StagePrepared
				body, err = json.Marshal(intent)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing locks":
				for _, relative := range []string{"publication-locks", "projects/" + f.projectID + "/locks"} {
					if err := os.RemoveAll(filepath.Join(f.data, relative)); err != nil {
						t.Fatal(err)
					}
				}
			case "missing private lock directory":
				if err := os.RemoveAll(filepath.Join(f.data, "projects", f.projectID, "memory-v1/locks")); err != nil {
					t.Fatal(err)
				}
			case "missing journal directory":
				if err := os.RemoveAll(filepath.Join(f.data, "publication-journal", f.projectID)); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotCLITree(t, filepath.Dir(f.data))
			var stdout, stderr bytes.Buffer
			code := Run([]string{"sync", "status", "--json", "--project-id", f.projectID, "--data-dir", f.data}, &stdout, &stderr)
			if after := snapshotCLITree(t, filepath.Dir(f.data)); before != after {
				t.Fatal("status changed Project/Vault/private tree entries or bytes")
			}
			if name == "generated edit" || name == "missing binding" || name == "pending journal" || name == "missing journal directory" || name == "missing private lock directory" {
				if code == 0 || stdout.Len() != 0 || stderr.Len() == 0 {
					t.Fatalf("refused status code=%d out=%q err=%q", code, stdout.String(), stderr.String())
				}
				return
			}
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("status code=%d out=%q err=%q", code, stdout.String(), stderr.String())
			}
			var status syncengine.Status
			if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if status.ProjectID != f.projectID || status.Migration != "current" || status.LastSuccessfulSync != "" || status.DerivedFiles != 0 || status.Malformed != 0 || status.Queued != 0 || status.Blocked != 0 || len(status.HiddenConflictIDs) != 0 {
				t.Fatalf("incorrect Markdown status: %+v", status)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(stdout.Bytes(), &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire) != 15 {
				t.Fatalf("Status wire was replaced or extended: %s", stdout.String())
			}
			for _, field := range []string{"pending", "pending_operations", "open_conflicts", "hidden_conflict_ids"} {
				if string(wire[field]) == "null" {
					t.Fatalf("null %s", field)
				}
			}
			switch name {
			case "pending":
				if status.InSync != 0 || status.Conflicted != 0 || len(status.Pending) != 6 || !reflect.DeepEqual(status.Pending, status.PendingOperations) || status.MachineState != syncengine.MachinePending || status.DerivedState != syncengine.DerivedPending {
					t.Fatalf("pending status: %+v", status)
				}
			case "conflict":
				if status.InSync != 0 || status.Conflicted != 1 || len(status.OpenConflicts) != 1 || strings.HasPrefix(status.OpenConflicts[0], "conflict-") || len(status.Pending) != 0 || status.MachineState != syncengine.MachineBlocked || status.DerivedState != syncengine.DerivedDeferred {
					t.Fatalf("conflict status: %+v", status)
				}
			default:
				if status.InSync != 1 || status.Conflicted != 0 || len(status.Pending) != 0 || status.MachineState != syncengine.MachineCurrent || status.DerivedState != syncengine.DerivedCurrent {
					t.Fatalf("clean status: %+v", status)
				}
			}
		})
	}
}

func TestSyncCLIStatusPreservesLegacyMissingNewLayout(t *testing.T) {
	f := newCLISyncFixture(t)
	for _, relative := range []string{"projects/" + f.projectID + "/memory-v1", "publication-journal"} {
		if err := os.RemoveAll(filepath.Join(f.data, relative)); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"sync", "status", "--json", "--project-id", f.projectID, "--data-dir", f.data}, &stdout, &stderr); code != 0 {
		t.Fatalf("legacy status code=%d err=%q", code, stderr.String())
	}
	var status syncengine.Status
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil || status.ProjectID != f.projectID {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestSyncCLIStatusPreservesV3HumanDraft(t *testing.T) {
	f := newCLIV3FormatFixture(t)
	locks := filepath.Join(f.data, "projects", f.projectID, "locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locks, "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.project, filepath.FromSlash(reviewv2.ReviewRelativePath))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Replace(body, []byte("classify v3"), []byte("edited v3 goal"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"sync", "status", "--json", "--project-id", f.projectID, "--data-dir", f.data}, &stdout, &stderr); code != 0 {
		t.Fatalf("v3 draft status code=%d err=%q", code, stderr.String())
	}
	var status syncengine.Status
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil || status.ProjectID != f.projectID || len(status.PendingOperations) == 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestSyncCLIStatusOldV4JSONRefusesWithoutMigrationWrites(t *testing.T) {
	f := newCLIOldV4Fixture(t)
	before := snapshotCLITree(t, filepath.Dir(f.data))
	var stdout, stderr bytes.Buffer
	code := Run([]string{"sync", "status", "--json", "--project-id", f.projectID, "--data-dir", f.data}, &stdout, &stderr)
	if code == 0 || stdout.Len() != 0 || stderr.Len() == 0 || snapshotCLITree(t, filepath.Dir(f.data)) != before {
		t.Fatalf("old v4 status code=%d out=%q err=%q", code, stdout.String(), stderr.String())
	}
}
