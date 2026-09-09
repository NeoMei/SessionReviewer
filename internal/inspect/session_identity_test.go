package inspect

import (
	"context"
	"strings"
	"testing"
)

func TestAuthenticatePublishedSessionIdentityBindsExactView(t *testing.T) {
	fixture := buildEventFixture(t, "project-session-open", "generation-session-open", "session-1")
	wantDigest := fixture.sessionDigests["session-1"]
	request := PublishedSessionIdentityRequest{
		DataRoot: fixture.dataRoot, ProjectID: fixture.projectID, Provider: "codex",
		SessionID: "session-1", ExpectedGenerationID: fixture.generationID,
		ExpectedSessionViewDigest: wantDigest,
	}

	got, err := AuthenticatePublishedSessionIdentity(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != fixture.projectID || got.Provider != "codex" || got.SessionID != "session-1" || got.GenerationID != fixture.generationID || got.SessionViewDigest != wantDigest || got.SourceIdentity == "" || got.SourceRecordDigest == "" {
		t.Fatalf("identity=%+v", got)
	}

	request.ExpectedSessionViewDigest = "sha256:" + strings.Repeat("f", 64)
	if _, err := AuthenticatePublishedSessionIdentity(context.Background(), request); eventErrorCode(err) != CodeGenerationMismatch {
		t.Fatalf("wrong view digest code=%q err=%v", eventErrorCode(err), err)
	}
}
