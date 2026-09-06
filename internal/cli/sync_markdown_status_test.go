package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
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
	if err := os.WriteFile(path, bytes.Replace(body, []byte("classify v3"), []byte("Discuss review-markdown-v1 and session-reviewer:v4-field in an ordinary legacy goal"), 1), 0o600); err != nil {
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

func TestSyncCLIStatusPreservesMalformedV3EngineDiagnostics(t *testing.T) {
	for _, relative := range []string{reviewv2.ReviewRelativePath, reviewv2.MachineLedgerRelativePath, reviewv2.HistoryRelativePath} {
		t.Run(filepath.Base(relative), func(t *testing.T) {
			f := newCLIV3FormatFixture(t)
			locks := filepath.Join(f.data, "projects", f.projectID, "locks")
			if err := os.MkdirAll(locks, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(locks, "sync.lock"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			root, mapping, data, err := resolveSyncMapping("", f.projectID, f.data)
			if err != nil {
				t.Fatal(err)
			}
			engine, err := syncengine.NewEngine(syncengine.Options{ProjectRoot: root.Path, ProjectRootExpected: root.Expected, VaultRoot: mapping.VaultRoot, VaultReviewPath: mapping.VaultReviewPath, DataRoot: data, ProjectID: mapping.ID, GOOS: runtime.GOOS, VaultCaseMode: mapping.VaultCaseMode, Retry: syncengine.DefaultRetryPolicy(), Now: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := engine.Reconcile(t.Context(), syncengine.ReconcileRequest{Trigger: syncengine.TriggerCLI}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.project, relative), []byte("malformed legacy canary\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotCLITree(t, filepath.Dir(f.data))
			want, err := engine.Status(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
			if want.Blocked == 0 {
				t.Fatalf("legacy Engine did not expose malformed diagnostic: %+v", want)
			}
			var stdout, stderr bytes.Buffer
			code := Run([]string{"sync", "status", "--json", "--project-id", f.projectID, "--data-dir", f.data}, &stdout, &stderr)
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("lost legacy diagnostic status: code=%d err=%q", code, stderr.String())
			}
			var got syncengine.Status
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("CLI status=%+v differs from Engine=%+v", got, want)
			}
			if snapshotCLITree(t, filepath.Dir(f.data)) != before {
				t.Fatal("legacy diagnostic status changed fixture trees")
			}
		})
	}
}

func TestSyncCLIStatusConflictRejectsInvalidVaultConclusion(t *testing.T) {
	f := newCLIV4PublicationFixture(t, true, func(p *reviewv4.Presentation) {
		missing := "not_captured"
		segment := reviewv4.ClosedLoopSegment{State: "missing", MissingReason: &missing, SourceTurnRefs: []reviewv4.SourceTurnRef{}}
		p.Timeline = []reviewv4.Timeline{{ID: "milestone-status", GenerationID: p.GenerationID, OccurredAt: "2026-09-05T00:00:00Z", Kind: "milestone", Title: "Confirmed milestone", Summary: "A confirmed conclusion must not be cleared", DecisionIDs: []string{}, ClosedLoop: reviewv4.ClosedLoop{
			TriggerQuestion: segment, Execution: segment, Verification: segment, ImpactAndFollowUp: segment,
			Conclusion: reviewv4.ClosedLoopConclusion{Kind: reviewv4.ConclusionHumanConfirmed, Text: "Confirmed conclusion", SourceTurnRefs: []reviewv4.SourceTurnRef{}}, SourceTurnRefs: []reviewv4.SourceTurnRef{},
		}}}
	})
	edit := func(path, relative string, key reviewv4.FieldKey, value string) {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := reviewv4.ParseMarkdownDocument(relative, body)
		if err != nil {
			t.Fatal(err)
		}
		body, err = doc.ReplaceFields(map[reviewv4.FieldKey]string{key: value})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	edit(filepath.Join(f.project, reviewv2.ReviewRelativePath), "项目回顾.md", reviewv4.FieldKey{Entity: "project-overview", Name: "goal"}, "Project goal")
	edit(filepath.Join(f.vault, "Projects/Markdown/Session Review/项目回顾.md"), "项目回顾.md", reviewv4.FieldKey{Entity: "project-overview", Name: "goal"}, "Vault goal")
	edit(filepath.Join(f.vault, "Projects/Markdown/Session Review/项目历史.md"), "项目历史.md", reviewv4.FieldKey{Entity: "milestone:milestone-status", Name: "conclusion"}, "")
	options := syncproject.Options{ProjectID: f.projectID, DataDir: f.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI}
	if format, err := syncproject.DetectFormat(t.Context(), options); err != nil || format != syncproject.ProjectionMarkdown {
		t.Fatalf("valid Project draft rejected: format=%s err=%v", format, err)
	}
	before := snapshotCLITree(t, filepath.Dir(f.data))
	status, err := syncproject.StatusMarkdown(t.Context(), options)
	if err == nil || !reflect.DeepEqual(status, syncengine.Status{}) {
		t.Errorf("invalid Vault conclusion hidden by conflict: status=%+v err=%v", status, err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"sync", "status", "--json", "--project-id", f.projectID, "--data-dir", f.data}, &stdout, &stderr)
	if code == 0 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Errorf("invalid Vault draft yielded JSON: code=%d out=%q err=%q", code, stdout.String(), stderr.String())
	}
	if snapshotCLITree(t, filepath.Dir(f.data)) != before {
		t.Fatal("status changed recorded Project/Vault/private trees")
	}
}

func TestSyncCLIStatusRejectsPartialV4Downgrade(t *testing.T) {
	for _, scenario := range []string{"legacy documents with v4 sidecars", "legacy documents with only v4 receipt", "truncated v4 ledger with legacy review", "truncated noncanonical v4 ledger with legacy review", "v4 history with legacy review", "damaged v4 without ledger or binding"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCLIAuthenticatedMarkdownFixture(t)
			donor := newCLIV3FormatFixture(t)
			if scenario != "damaged v4 without ledger or binding" {
				for _, relative := range []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath} {
					if scenario == "v4 history with legacy review" && relative == reviewv2.HistoryRelativePath {
						continue
					}
					body, err := os.ReadFile(filepath.Join(donor.project, relative))
					if err != nil {
						t.Fatal(err)
					}
					body = bytes.ReplaceAll(body, []byte(donor.projectID), []byte(f.projectID))
					for _, path := range []string{filepath.Join(f.project, relative), filepath.Join(f.vault, "Projects/Markdown/Session Review", strings.TrimPrefix(relative, "docs/session-review/"))} {
						if err := os.WriteFile(path, body, 0o600); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if scenario != "legacy documents with v4 sidecars" {
				for _, path := range []string{filepath.Join(f.project, "docs/session-review/.session-reviewer/session-index.json"), filepath.Join(f.vault, "Projects/Markdown/Session Review/.session-reviewer/session-index.json")} {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario != "legacy documents with v4 sidecars" && scenario != "legacy documents with only v4 receipt" {
				if err := os.RemoveAll(filepath.Join(f.data, "publication-journal", f.projectID)); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(scenario, "truncated") {
				number := "4"
				if strings.Contains(scenario, "noncanonical") {
					number = "4.0"
				}
				if err := os.WriteFile(filepath.Join(f.project, reviewv2.MachineLedgerRelativePath), []byte("{\"schema_version\":"+number+","), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "damaged v4 without ledger or binding" {
				for _, path := range []string{filepath.Join(f.project, reviewv2.MachineLedgerRelativePath), filepath.Join(f.vault, "Projects/Markdown/Session Review/.session-reviewer/ledger.json")} {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(f.project, reviewv2.ReviewRelativePath)
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, bytes.Replace(body, []byte("schema_version: 4"), []byte("schema_version: 3"), 1), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotCLITree(t, filepath.Dir(f.data))
			format, err := syncproject.DetectStatusFormat(t.Context(), syncproject.Options{ProjectID: f.projectID, DataDir: f.data})
			if err == nil || format != "" {
				t.Errorf("partial v4 was classified as %q, err=%v", format, err)
			}
			var stdout, stderr bytes.Buffer
			code := Run([]string{"sync", "status", "--json", "--project-id", f.projectID, "--data-dir", f.data}, &stdout, &stderr)
			if code == 0 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Errorf("partial v4 status code=%d out=%q err=%q", code, stdout.String(), stderr.String())
			}
			if snapshotCLITree(t, filepath.Dir(f.data)) != before {
				t.Fatal("partial v4 status changed fixture trees")
			}
		})
	}
}
