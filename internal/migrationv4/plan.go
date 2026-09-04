package migrationv4

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"

	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

var previewDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var decisionDefaultFields = []string{
	"kind=decision", "milestone_ids=[]", "pinned=false", "provenance=migrated",
	"reevaluate_when=", "revision=1", "session_refs=[]", "supersedes=[]",
}

// PreviewMigration validates a complete authenticated v3 source and returns
// its semantic-only preview. Target hashes remain empty until BuildPreview is
// supplied the required session index.
func PreviewMigration(review, history, ledger []byte) (MigrationPreview, error) {
	accepted, err := reviewv2.LoadV3Bytes(review, history, ledger)
	if err != nil {
		return MigrationPreview{}, err
	}
	preview := basePreview(accepted, review, history, ledger)
	preview.TargetPreimageHashes = absentHashes()
	preview.PreviewDigest = MigrationPreviewDigest(preview)
	return preview, nil
}

func basePreview(accepted reviewv2.AcceptedV3, review, history, ledger []byte) MigrationPreview {
	ids := make([]string, 0, len(accepted.State.Review.Decisions))
	defaults := make(map[string][]string, len(accepted.State.Review.Decisions))
	for _, decision := range accepted.State.Review.Decisions {
		ids = append(ids, decision.ID)
		defaults[decision.ID] = append([]string(nil), decisionDefaultFields...)
	}
	sort.Strings(ids)
	return MigrationPreview{
		SchemaVersion: 1, SourceVersion: 3, TargetVersion: 4,
		ProjectID: accepted.State.Review.ProjectID, GenerationID: accepted.State.Review.GenerationID,
		PreservedDecisionIDs: ids, DefaultedFields: defaults, RequiresSessionIndex: true,
		SourceHashes:                 ArtifactHashes{Review: digest(review), History: digest(history), Ledger: digest(ledger)},
		SessionViewDependencyDigests: []string{},
	}
}

// MigrationPreviewDigest authenticates canonical preview JSON with the digest
// field itself omitted.
func MigrationPreviewDigest(preview MigrationPreview) string {
	preview.PreviewDigest = ""
	normalizePreview(&preview)
	body, err := json.Marshal(preview)
	if err != nil {
		return ""
	}
	return digest(body)
}

func validatePreview(preview MigrationPreview) error {
	if preview.SchemaVersion != 1 || preview.SourceVersion != 3 || preview.TargetVersion != 4 || preview.ProjectID == "" || preview.GenerationID == "" || !preview.RequiresSessionIndex {
		return errors.New("invalid migration preview metadata")
	}
	for _, value := range []string{
		preview.SourceHashes.Review, preview.SourceHashes.History, preview.SourceHashes.Ledger,
		preview.TargetHashes.Review, preview.TargetHashes.History, preview.TargetHashes.Ledger, preview.TargetHashes.SessionIndex,
	} {
		if !previewDigestPattern.MatchString(value) {
			return errors.New("migration preview contains an invalid artifact hash")
		}
	}
	for _, value := range []string{
		preview.TargetPreimageHashes.Review, preview.TargetPreimageHashes.History,
		preview.TargetPreimageHashes.Ledger, preview.TargetPreimageHashes.SessionIndex,
	} {
		if value != AbsentPreimageSHA256 && !previewDigestPattern.MatchString(value) {
			return errors.New("migration preview contains an invalid target preimage hash")
		}
	}
	for _, value := range preview.SessionViewDependencyDigests {
		if !previewDigestPattern.MatchString(value) {
			return errors.New("migration preview contains an invalid SessionView dependency digest")
		}
	}
	if MigrationPreviewDigest(preview) != preview.PreviewDigest {
		return errors.New("migration preview digest mismatch")
	}
	return nil
}

func normalizePreview(preview *MigrationPreview) {
	sort.Strings(preview.PreservedDecisionIDs)
	if preview.PreservedDecisionIDs == nil {
		preview.PreservedDecisionIDs = []string{}
	}
	if preview.DefaultedFields == nil {
		preview.DefaultedFields = map[string][]string{}
	}
	for key, values := range preview.DefaultedFields {
		copyValues := append([]string(nil), values...)
		sort.Strings(copyValues)
		preview.DefaultedFields[key] = copyValues
	}
	preview.SessionViewDependencyDigests = sortedUnique(preview.SessionViewDependencyDigests)
}

func sortedUnique(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	if result == nil {
		return []string{}
	}
	for index := 1; index < len(result); {
		if result[index] == result[index-1] {
			result = append(result[:index], result[index+1:]...)
		} else {
			index++
		}
	}
	return result
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func preimageHash(value Preimage) string {
	if !value.Exists {
		return AbsentPreimageSHA256
	}
	return digest(value.Bytes)
}

func absentHashes() ArtifactHashes {
	return ArtifactHashes{Review: AbsentPreimageSHA256, History: AbsentPreimageSHA256, Ledger: AbsentPreimageSHA256, SessionIndex: AbsentPreimageSHA256}
}
