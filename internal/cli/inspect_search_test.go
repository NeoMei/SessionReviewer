package cli

import (
	"bytes"
	"encoding/json"
	inspectapi "github.com/neomei/SessionReviewer/internal/inspect"
	"testing"
)

func TestInspectSearchRunsAtCLIWithoutWriting(t *testing.T) {
	fixture := newCLIInspectFixture(t)
	before := snapshotCLITree(t, fixture.data)
	var out, diag bytes.Buffer
	code := Run([]string{"inspect", "session-search", "--project-id", fixture.projectID, "--expected-generation-id", fixture.generationID, "--query-kind", "file", "--query", "../../no-real-path", "--limit", "20", "--data-dir", fixture.data, "--json"}, &out, &diag)
	if code != 0 {
		t.Fatalf("code=%d output=%s err=%s", code, &out, &diag)
	}
	var page inspectapi.SearchPage
	if err := json.Unmarshal(out.Bytes(), &page); err != nil || page.Total != 0 || page.Items == nil || page.ProjectID != fixture.projectID {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if snapshotCLITree(t, fixture.data) != before {
		t.Fatal("search CLI wrote private tree")
	}
}
