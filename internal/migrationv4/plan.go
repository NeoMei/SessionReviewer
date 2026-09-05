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
	if len(preview.BlockingReasons) != 0 {
		return errors.New("blocked migration preview is not publishable")
	}
	if preview.TargetFormat != "" && (preview.SourceFormat == "" || preview.TargetFormat != FormatMarkdownV1) {
		return errors.New("invalid migration format route")
	}
	validVersionRoute := preview.SourceVersion == 3 && preview.TargetVersion == 4
	if preview.TargetFormat != "" {
		validVersionRoute = (preview.SourceVersion == 2 || preview.SourceVersion == 3 || preview.SourceVersion == 4) && preview.TargetVersion == 4
	}
	if preview.SchemaVersion != 1 || !validVersionRoute || preview.ProjectID == "" || preview.GenerationID == "" || !preview.RequiresSessionIndex {
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
	if preview.TargetFormat != "" && !previewDigestPattern.MatchString(preview.SourceHashes.SessionIndex) {
		return errors.New("migration preview contains an invalid source index hash")
	}
	for _, value := range []string{
		preview.TargetPreimageHashes.Review, preview.TargetPreimageHashes.History,
		preview.TargetPreimageHashes.Ledger, preview.TargetPreimageHashes.SessionIndex,
	} {
		if value != AbsentPreimageSHA256 && !previewDigestPattern.MatchString(value) {
			return errors.New("migration preview contains an invalid target preimage hash")
		}
	}
	if preview.TargetFormat != "" && preview.VaultPreimageHashes == nil {
		return errors.New("Markdown migration preview has no Vault preimages")
	}
	if preview.VaultPreimageHashes != nil {
		for _, value := range []string{preview.VaultPreimageHashes.Review, preview.VaultPreimageHashes.History, preview.VaultPreimageHashes.Ledger, preview.VaultPreimageHashes.SessionIndex} {
			if value != AbsentPreimageSHA256 && !previewDigestPattern.MatchString(value) {
				return errors.New("migration preview contains an invalid Vault preimage hash")
			}
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

func validateBlockedPreview(preview MigrationPreview) error {
	if preview.SchemaVersion != 1 || preview.ProjectID == "" || preview.SourceFormat == "" || preview.TargetFormat != FormatMarkdownV1 || len(preview.BlockingReasons) == 0 || preview.VaultPreimageHashes == nil {
		return errors.New("invalid blocked migration preview metadata")
	}
	if preview.TargetHashes != (ArtifactHashes{}) {
		return errors.New("blocked migration preview contains target hashes")
	}
	for _, value := range []string{preview.SourceHashes.Review, preview.SourceHashes.History, preview.SourceHashes.Ledger, preview.SourceHashes.SessionIndex} {
		if value != AbsentPreimageSHA256 && !previewDigestPattern.MatchString(value) {
			return errors.New("blocked migration preview contains an invalid source hash")
		}
	}
	if MigrationPreviewDigest(preview) != preview.PreviewDigest {
		return errors.New("migration preview digest mismatch")
	}
	return nil
}

func normalizePreview(preview *MigrationPreview) {
	preview.PreservedDecisionIDs = append([]string(nil), preview.PreservedDecisionIDs...)
	sort.Strings(preview.PreservedDecisionIDs)
	if preview.PreservedDecisionIDs == nil {
		preview.PreservedDecisionIDs = []string{}
	}
	defaults := make(map[string][]string, len(preview.DefaultedFields))
	for key, values := range preview.DefaultedFields {
		copyValues := append([]string(nil), values...)
		sort.Strings(copyValues)
		defaults[key] = copyValues
	}
	preview.DefaultedFields = defaults
	preview.SessionViewDependencyDigests = sortedUnique(preview.SessionViewDependencyDigests)
	preview.BlockingReasons = sortedUniqueOmitNil(preview.BlockingReasons)
	if preview.PreservedCustomHashes != nil {
		preview.PreservedCustomHashes = cloneStringMap(preview.PreservedCustomHashes)
	}
}

func sortedUniqueOmitNil(values []string) []string {
	if values == nil {
		return nil
	}
	return sortedUnique(values)
}

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
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
