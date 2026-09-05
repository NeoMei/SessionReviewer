// Package migrationv4 implements the explicit, digest-bound v3 to v4
// projection migration.  It deliberately does not share the legacy v2/v3
// migration journal.
package migrationv4

import (
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

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
	SourceGenerationID           string              `json:"source_generation_id,omitempty"`
	SourceManifestDigest         string              `json:"source_manifest_digest,omitempty"`
	TargetManifestDigest         string              `json:"target_manifest_digest,omitempty"`
	SourceJournalDigest          string              `json:"source_journal_digest,omitempty"`
	PreservedDecisionIDs         []string            `json:"preserved_decision_ids"`
	DefaultedFields              map[string][]string `json:"defaulted_fields"`
	RequiresSessionIndex         bool                `json:"requires_session_index"`
	SourceHashes                 ArtifactHashes      `json:"source_hashes"`
	SessionViewDependencyDigests []string            `json:"session_view_dependency_digests"`
	TargetHashes                 ArtifactHashes      `json:"target_hashes"`
	TargetPreimageHashes         ArtifactHashes      `json:"target_preimage_hashes"`
	VaultPreimageHashes          *ArtifactHashes     `json:"vault_preimage_hashes,omitempty"`
	PreviewDigest                string              `json:"preview_digest"`
	SourceFormat                 string              `json:"source_format,omitempty"`
	TargetFormat                 string              `json:"target_format,omitempty"`
	PreservedCustomHashes        map[string]string   `json:"preserved_custom_hashes,omitempty"`
	BlockingReasons              []string            `json:"blocking_reasons,omitempty"`
}

type Preimage struct {
	Exists bool
	Bytes  []byte
}

// Input contains every value that confirmation must recompute while holding
// the project lock. TargetPreimages is keyed by the four RelativePath constants.
type Input struct {
	Review       []byte
	History      []byte
	Ledger       []byte
	SessionIndex []byte
	// SourceSessionIndex preserves the exact old-v4 source index separately
	// from SessionIndex, which is the target index used by legacy callers.
	SourceSessionIndex           []byte
	GenerationID                 string
	SourceManifestDigest         string
	TargetManifestDigest         string
	SourceJournalDigest          string
	SessionViewDependencyDigests []string
	TargetPreimages              map[string]Preimage
	TargetVaultPreimages         map[string]Preimage
}

// MarkdownMigrationInput deliberately accepts typed reconstruction only as
// data. Current legacy sources have no authenticated ConversationChain bundle,
// so a non-nil Reconstructed value does not by itself make them publishable.
type MarkdownMigrationInput struct {
	Source        Input
	Reconstructed *reviewv4.Presentation
}

type BindingSuccessorInput struct {
	SourceManifest       memory.GenerationManifest
	SourceManifestDigest string
	ProjectView          memory.ProjectView
	SessionViews         map[sessionindex.SessionKey]*memory.SessionView
	SourceIndex          *sessionindex.Document
}

type BindingSuccessor struct {
	Manifest             memory.GenerationManifest
	Index                sessionindex.Document
	SourceManifestDigest string
	TargetManifestDigest string
	SeedIndexDigest      string
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
	// TargetVaultPreimages carry the exact second-side migration preimages.
	TargetVaultPreimages map[string]Preimage
	SuccessorManifest    *memory.GenerationManifest
}
