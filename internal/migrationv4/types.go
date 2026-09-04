// Package migrationv4 implements the explicit, digest-bound v3 to v4
// projection migration.  It deliberately does not share the legacy v2/v3
// migration journal.
package migrationv4

import "github.com/neomei/SessionReviewer/internal/reviewv4"

const (
	ReviewRelativePath       = "docs/session-review/项目回顾.md"
	HistoryRelativePath      = "docs/session-review/项目历史.md"
	LedgerRelativePath       = "docs/session-review/.session-reviewer/ledger.json"
	SessionIndexRelativePath = "docs/session-review/.session-reviewer/session-index.json"
	AbsentPreimageSHA256     = "absent"
)

// ArtifactHashes names every member of the v4 public projection atom.
type ArtifactHashes struct {
	Review       string `json:"review"`
	History      string `json:"history"`
	Ledger       string `json:"ledger"`
	SessionIndex string `json:"session_index"`
}

type MigrationPreview struct {
	SchemaVersion                int                 `json:"schema_version"`
	SourceVersion                int                 `json:"source_version"`
	TargetVersion                int                 `json:"target_version"`
	ProjectID                    string              `json:"project_id"`
	GenerationID                 string              `json:"generation_id"`
	PreservedDecisionIDs         []string            `json:"preserved_decision_ids"`
	DefaultedFields              map[string][]string `json:"defaulted_fields"`
	RequiresSessionIndex         bool                `json:"requires_session_index"`
	SourceHashes                 ArtifactHashes      `json:"source_hashes"`
	SessionViewDependencyDigests []string            `json:"session_view_dependency_digests"`
	TargetHashes                 ArtifactHashes      `json:"target_hashes"`
	TargetPreimageHashes         ArtifactHashes      `json:"target_preimage_hashes"`
	PreviewDigest                string              `json:"preview_digest"`
}

type Preimage struct {
	Exists bool
	Bytes  []byte
}

// Input contains every value that confirmation must recompute while holding
// the project lock. TargetPreimages is keyed by the four RelativePath constants.
type Input struct {
	Review                       []byte
	History                      []byte
	Ledger                       []byte
	SessionIndex                 []byte
	GenerationID                 string
	SessionViewDependencyDigests []string
	TargetPreimages              map[string]Preimage
}

// Result is the complete deterministic four-file migration plan.
type Result struct {
	Review       []byte
	History      []byte
	Ledger       []byte
	SessionIndex []byte
	Preview      MigrationPreview
	Accepted     reviewv4.Accepted
	// TargetPreimages are the exact bytes authenticated by PreviewDigest and
	// must be forwarded unchanged to the publication transaction.
	TargetPreimages map[string]Preimage
}
