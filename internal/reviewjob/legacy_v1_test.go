package reviewjob

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreStrictlyMigratesHistoricalV1Fixtures(t *testing.T) {
	tests := []struct {
		name         string
		revision     int
		state        State
		payloadState PayloadState
		pending      bool
		errorCode    ErrorCode
	}{
		{name: "job-v1-22097a5-active.json", revision: 4, state: Running},
		{name: "job-v1-22097a5-terminal.json", revision: 6, state: Completed},
		{name: "job-v1-bdc3a11-active.json", revision: 9, state: Running, payloadState: PayloadRetained},
		{name: "job-v1-bdc3a11-terminal.json", revision: 12, state: Failed, payloadState: PayloadCleanupComplete, pending: true, errorCode: SyncConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, primary, historical := installHistoricalJobFixture(t, test.name)
			var exact legacyStoredJobV1
			if err := decodeStrictCanonical(historical, &exact); err != nil {
				t.Fatalf("fixture is not the exact strict historical wire: %v", err)
			}
			job, revision, found, err := (Store{Root: root}).Load("job-1")
			if err != nil || !found || revision != test.revision {
				t.Fatalf("Load()=%#v revision=%d found=%v err=%v", job, revision, found, err)
			}
			if job.ID != "job-1" || job.ProjectID != "project-1" || job.ProjectIdentity != exact.Job.ProjectIdentity ||
				job.State != test.state || job.PayloadState != test.payloadState || job.AcceptedSyncPending != test.pending || job.Error.Code != test.errorCode {
				t.Fatalf("migrated job=%#v", job)
			}
			if after := readFile(t, primary); !bytes.Equal(after, historical) {
				t.Fatal("read-only legacy migration changed canonical historical bytes")
			}

			updated, nextRevision, err := (Store{Root: root}).Update("job-1", revision, func(*Job) error { return nil })
			if err != nil || nextRevision != revision+1 || updated.AcceptedSyncPending != test.pending {
				t.Fatalf("durable migration=%#v revision=%d err=%v", updated, nextRevision, err)
			}
			current := readFile(t, primary)
			if bytes.Contains(current, []byte(`"sync_only_available"`)) || !bytes.Contains(current, []byte(`"accepted_sync_pending"`)) {
				t.Fatalf("new canonical write retained the retired field: %s", current)
			}
		})
	}
}

func TestStoreLegacyV1DecoderRejectsUnknownMissingAndDuplicateFields(t *testing.T) {
	original := readFile(t, filepath.Join("testdata", "job-v1-22097a5-active.json"))
	tests := map[string][]byte{
		"unknown":                     bytes.Replace(original, []byte(`"revision": 4,`), []byte("\"revision\": 4,\n  \"unknown\": true,"), 1),
		"missing mandatory sync-only": bytes.Replace(original, []byte(",\n    \"sync_only_available\": false"), nil, 1),
		"duplicate sync-only":         bytes.Replace(original, []byte(`"sync_only_available": false`), []byte("\"sync_only_available\": false,\n    \"sync_only_available\": false"), 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			root := newStoreWithJob(t)
			if err := os.WriteFile(filepath.Join(root, "review-jobs/jobs/job-1.json"), body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := (Store{Root: root}).Load("job-1"); err == nil {
				t.Fatal("Load() accepted a non-strict legacy record")
			}
		})
	}
}

func TestStoreLegacySyncOnlyRequiresExactAcceptedConflictEvidence(t *testing.T) {
	original := readFile(t, filepath.Join("testdata", "job-v1-bdc3a11-terminal.json"))
	for _, code := range []ErrorCode{AgentAuth, SyncPartial} {
		t.Run(string(code), func(t *testing.T) {
			body := bytes.Replace(original, []byte(`"E_SYNC_CONFLICT"`), []byte(`"`+string(code)+`"`), 1)
			root := newStoreWithJob(t)
			if err := os.WriteFile(filepath.Join(root, "review-jobs/jobs/job-1.json"), body, 0o600); err != nil {
				t.Fatal(err)
			}
			job, _, found, err := (Store{Root: root}).Load("job-1")
			if err != nil || !found {
				t.Fatalf("Load() found=%v err=%v", found, err)
			}
			if job.AcceptedSyncPending || job.Error.Code != ApplyRecovery || job.PayloadState != PayloadCleanupComplete {
				t.Fatalf("ambiguous old sync-only record was not recovery-safe: %#v", job)
			}
		})
	}
}

func installHistoricalJobFixture(t *testing.T, name string) (root, primary string, body []byte) {
	t.Helper()
	root = newStoreWithJob(t)
	primary = filepath.Join(root, "review-jobs/jobs/job-1.json")
	body = readFile(t, filepath.Join("testdata", name))
	if err := os.WriteFile(primary, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, primary, body
}
