package publication

import (
	"errors"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

// ExpectedMarkdownResultFingerprint records a candidate's exact proposed
// three-file result before publication. Callers must separately authenticate
// acceptance with publicationstate.Reader.AcceptedMarkdownResult.
func ExpectedMarkdownResultFingerprint(plan presentation.RenderPlan, mapping config.ProjectMapping, index []byte) (string, error) {
	parsed, err := sessionindex.Parse(index)
	if err != nil {
		return "", err
	}
	if mapping.ID != plan.ProjectID || parsed.ProjectID != plan.ProjectID || parsed.GenerationID != plan.GenerationID || parsed.ProjectViewDigest != plan.ProjectViewDigest || len(plan.Files) != 3 {
		return "", errors.New("candidate publication result identity mismatch")
	}
	hash := sha256Hex(index)
	guard := &publicationstate.IndexGuard{Relative: sessionIndexRelativePath, VaultRelative: vaultRelativePath(mapping.VaultReviewPath, sessionIndexRelativePath), ProjectSHA256: hash, VaultSHA256: hash, Digest: parsed.Digest, GenerationID: parsed.GenerationID}
	destinations := make([]publicationstate.Destination, 0, 6)
	for _, f := range plan.Files {
		destinations = append(destinations, publicationstate.Destination{Side: "project", Relative: f.Relative, DesiredSHA256: sha256Hex(f.Desired)}, publicationstate.Destination{Side: "vault", Relative: vaultRelativePath(mapping.VaultReviewPath, f.Relative), DesiredSHA256: sha256Hex(f.Desired)})
	}
	return publicationstate.MarkdownResultFingerprint(plan.ProjectID, plan.GenerationID, plan.ProjectViewDigest, guard, destinations)
}
