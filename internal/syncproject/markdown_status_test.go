package syncproject

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

// A post-build edit or binding/journal change must not be blessed as the input
// that produced the reported plan, even when the plan found a field conflict.
func TestStatusMarkdownRefusesChangedReadSet(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		for _, mutation := range []string{"Project review", "Vault history", "Project index", "Vault ledger", "receipt", "journal", "Base", "published pointer", "ProjectView", "SessionIndex", "journal directory binding", "memory directory binding", "mapping", "cancel"} {
			t.Run(mutation+map[bool]string{true: " after conflict", false: " after clean"}[conflict], func(t *testing.T) {
				f, _ := newMarkdownLockFixture(t)
				if conflict {
					statusEditGoal(t, filepath.Join(f.project, filepath.FromSlash(reviewv2.ReviewRelativePath)), "Project draft")
					statusEditGoal(t, filepath.Join(f.vault, "Projects/Migration/Session Review/项目回顾.md"), "Vault draft")
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				options := Options{ProjectID: f.projectID, CWD: f.project, DataDir: f.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI}
				options.afterMarkdownBuild = func() error {
					var path string
					switch mutation {
					case "cancel":
						cancel()
						return nil
					case "Project review":
						path = filepath.Join(f.project, filepath.FromSlash(reviewv2.ReviewRelativePath))
					case "Vault history":
						path = filepath.Join(f.vault, "Projects/Migration/Session Review/项目历史.md")
					case "Project index":
						path = filepath.Join(f.project, "docs/session-review/.session-reviewer/session-index.json")
					case "Vault ledger":
						path = filepath.Join(f.vault, "Projects/Migration/Session Review/.session-reviewer/ledger.json")
					case "receipt":
						path = filepath.Join(f.data, "publication-journal", f.projectID, publicationstate.AcceptedReceiptLeaf)
					case "journal":
						path = filepath.Join(f.data, "publication-journal", f.projectID, publicationstate.IntentLeaf)
						body, err := os.ReadFile(path)
						if err != nil {
							return err
						}
						var intent publicationstate.Intent
						if err := json.Unmarshal(body, &intent); err != nil {
							return err
						}
						intent.Stage = publicationstate.StagePrepared
						body, err = json.Marshal(intent)
						if err != nil {
							return err
						}
						return os.WriteFile(path, body, 0o600)
					case "published pointer":
						path = filepath.Join(f.data, "projects", f.projectID, "memory-v1/published_generation")
					case "mapping":
						path = filepath.Join(f.data, "config.toml")
					case "journal directory binding", "memory directory binding":
						path = filepath.Join(f.data, "publication-journal", f.projectID)
						if mutation == "memory directory binding" {
							path = filepath.Join(f.data, "projects", f.projectID, "memory-v1")
						}
						previous := path + ".previous"
						if err := os.Rename(path, previous); err != nil {
							return err
						}
						if err := os.CopyFS(path, os.DirFS(previous)); err != nil {
							return err
						}
						return filepath.Walk(path, func(next string, info os.FileInfo, err error) error {
							if err != nil {
								return err
							}
							mode := os.FileMode(0o600)
							if info.IsDir() {
								mode = 0o700
							}
							return os.Chmod(next, mode)
						})
					case "Base", "ProjectView", "SessionIndex":
						relative := map[string]string{"Base": "merge-bases", "ProjectView": "memory-v1/project-views", "SessionIndex": "memory-v1/session-indexes"}[mutation]
						matches, err := filepath.Glob(filepath.Join(f.data, "projects", f.projectID, relative, "*.json"))
						if err != nil {
							return err
						}
						if len(matches) == 0 {
							t.Fatal("missing private fixture object")
						}
						path = matches[0]
					}
					body, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					return os.WriteFile(path, append(body, []byte("\nchanged after build")...), 0o600)
				}
				status, err := StatusMarkdown(ctx, options)
				if err == nil || !reflect.DeepEqual(status, syncengine.Status{}) {
					t.Fatalf("changed inputs yielded status %+v, err=%v", status, err)
				}
			})
		}
	}
}

func TestStatusMarkdownConflictDoesNotHideInvalidOtherDocument(t *testing.T) {
	f, _ := newMarkdownLockFixture(t)
	statusEditGoal(t, filepath.Join(f.project, filepath.FromSlash(reviewv2.ReviewRelativePath)), "Project draft")
	statusEditGoal(t, filepath.Join(f.vault, "Projects/Migration/Session Review/项目回顾.md"), "Vault draft")
	path := filepath.Join(f.vault, "Projects/Migration/Session Review/项目历史.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte("generation_id:"), []byte("invalid_generation_id:"), 1)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := StatusMarkdown(t.Context(), Options{ProjectID: f.projectID, CWD: f.project, DataDir: f.data, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI})
	if err == nil || !reflect.DeepEqual(status, syncengine.Status{}) {
		t.Fatalf("conflict hid invalid history: status=%+v err=%v", status, err)
	}
}

func statusEditGoal(t *testing.T, path, value string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	document, err := reviewv4.ParseMarkdownDocument("项目回顾.md", body)
	if err != nil {
		t.Fatal(err)
	}
	body, err = document.ReplaceFields(map[reviewv4.FieldKey]string{{Entity: "project-overview", Name: "goal"}: value})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}
