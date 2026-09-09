package inspect

import "context"

// PublishedSessionIdentityRequest binds an external action to one exact
// Session view already authenticated by the read-only inspection boundary.
type PublishedSessionIdentityRequest struct {
	DataRoot                  string
	ProjectID                 string
	Provider                  string
	SessionID                 string
	ExpectedGenerationID      string
	ExpectedSessionViewDigest string
}

// PublishedSessionIdentity deliberately carries identity only, never source
// text or an editable projection.
type PublishedSessionIdentity struct {
	ProjectID          string `json:"project_id"`
	Provider           string `json:"provider"`
	SessionID          string `json:"session_id"`
	GenerationID       string `json:"generation_id"`
	SessionViewDigest  string `json:"session_view_digest"`
	SourceIdentity     string `json:"source_identity"`
	SourceRecordDigest string `json:"source_record_digest"`
}

func AuthenticatePublishedSessionIdentity(ctx context.Context, request PublishedSessionIdentityRequest) (PublishedSessionIdentity, error) {
	var result PublishedSessionIdentity
	_, err := inspectPublishedSession(ctx, EventPageRequest{
		DataRoot: request.DataRoot, ProjectID: request.ProjectID, Provider: request.Provider,
		SessionID: request.SessionID, ExpectedGenerationID: request.ExpectedGenerationID, Limit: 1,
	}, nil, func(value authenticatedSession) error {
		if request.ExpectedSessionViewDigest == "" || value.view.Digest != request.ExpectedSessionViewDigest {
			return publicError(CodeGenerationMismatch, "published Session view does not match the expected view")
		}
		result = PublishedSessionIdentity{
			ProjectID: value.view.ProjectID, Provider: value.view.Provider, SessionID: value.view.SessionID,
			GenerationID: value.generationID, SessionViewDigest: value.view.Digest,
			SourceIdentity: value.view.SourceIdentity, SourceRecordDigest: value.view.SourceRecordDigest,
		}
		return nil
	})
	return result, err
}
