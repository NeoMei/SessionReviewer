package inspect

import (
	"context"
	"encoding/json"
	"github.com/neomei/SessionReviewer/internal/memory"
	"reflect"
	"strings"
	"testing"
)

func TestSessionSearchReturnsOnlyMatchedIdentitiesWithBoundPagination(t *testing.T) {
	fixture := buildEventFixtureCustomizedAt(t, t.TempDir(), "project-search", "generation-search", []string{"s1", "s2", "s3"}, func(rows []memory.ObservationRevision) {
		rows[1].Fields = map[string]string{"branch": "feature/private-search"}
		rows[1].RevisionID = memory.ObservationRevisionID(rows[1])
	}, nil)
	request := SearchRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, ExpectedGenerationID: fixture.generationID, QueryKind: "branch", Query: " PRIVATE-SEARCH ", Limit: 2}
	before := snapshotEventTree(t, fixture.dataRoot)
	page, err := LoadSessionSearch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("wrong search page: %+v", page)
	}
	body, _ := json.Marshal(page)
	if strings.Contains(string(body), "feature/private-search") || strings.Contains(string(body), fixture.projectRoot) || strings.Contains(string(body), "excerpt") {
		t.Fatal("search leaked private evidence")
	}
	request.Cursor = *page.NextCursor
	next, err := LoadSessionSearch(context.Background(), request)
	if err != nil || len(next.Items) != 1 || next.NextCursor != nil {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	request.Query = "another"
	if _, err := LoadSessionSearch(context.Background(), request); eventErrorCode(err) != CodeStaleCursor {
		t.Fatalf("query cursor accepted: %v", err)
	}
	if !reflect.DeepEqual(before, snapshotEventTree(t, fixture.dataRoot)) {
		t.Fatal("search wrote private state")
	}
}

func TestSessionSearchUsesTypedFieldsAndNeverResolvesQueryAsPath(t *testing.T) {
	rows := []memory.ObservationRevision{{Key: memory.ObservationKey{Kind: "file"}, Object: "src/main.go"}, {Key: memory.ObservationKey{Kind: "request"}, Excerpt: "src/secret.go error boom"}, {Key: memory.ObservationKey{Kind: "error"}, Fields: map[string]string{"code": "BUILD_FAILED"}}}
	if !searchMatches(rows, "file", "MAIN.GO") || searchMatches(rows, "file", "secret.go") || !searchMatches(rows, "error", "build_failed") || searchMatches(rows, "branch", "main") {
		t.Fatal("typed matching is wrong")
	}
	fixture := buildEventFixture(t, "project-search-empty", "generation-search-empty", "s1")
	request := SearchRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, ExpectedGenerationID: fixture.generationID, QueryKind: "file", Query: "../../private/not-a-file", Limit: 20}
	page, err := LoadSessionSearch(context.Background(), request)
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("literal path query: %+v %v", page, err)
	}
	request.ExpectedGenerationID = "generation-stale"
	if _, err := LoadSessionSearch(context.Background(), request); eventErrorCode(err) != CodeGenerationMismatch {
		t.Fatalf("stale=%v", err)
	}
}

func TestSessionSearchStopsWhenCancelledDuringRead(t *testing.T) {
	fixture := buildEventFixture(t, "project-search-race", "generation-search-race", "s1")
	ctx, cancel := context.WithCancel(context.Background())
	inspectCheckpoint = func(name string) {
		if name == "revision_item" {
			cancel()
		}
	}
	defer func() { inspectCheckpoint = nil }()
	_, err := LoadSessionSearch(ctx, SearchRequest{DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, ExpectedGenerationID: fixture.generationID, QueryKind: "file", Query: "x", Limit: 1})
	if err == nil {
		t.Fatal("cancelled search succeeded")
	}
}
