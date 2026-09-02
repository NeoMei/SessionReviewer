package migrationv3

import (
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

type LegacyClass string

const (
	LegacyHumanApproved LegacyClass = "human_approved"
	LegacyUnverified    LegacyClass = "legacy_unverified"
	LegacyRejected      LegacyClass = "rejected"
)

type LegacyItem struct {
	EntityID  string      `json:"entity_id"`
	Kind      string      `json:"kind"`
	Class     LegacyClass `json:"class"`
	Title     string      `json:"title"`
	Detail    string      `json:"detail,omitempty"`
	Rationale string      `json:"rationale,omitempty"`
	Impact    string      `json:"impact,omitempty"`
	Status    string      `json:"status,omitempty"`
}

type RejectedLegacyItem struct {
	EntityID string `json:"entity_id"`
	Kind     string `json:"kind"`
	Reason   string `json:"reason"`
}

type Input struct {
	ProjectID          string
	PreparedGeneration string
	AcceptedV2         reviewv2.Accepted
	PublicPreimages    []publication.Destination
}

type Plan struct {
	SchemaVersion      int                       `json:"schema_version"`
	ProjectID          string                    `json:"project_id"`
	SourceRevision     int                       `json:"source_revision"`
	PreparedGeneration string                    `json:"prepared_generation"`
	HumanPatches       []presentation.Patch      `json:"human_patches"`
	LegacyItems        []LegacyItem              `json:"legacy_items"`
	RejectedItems      []RejectedLegacyItem      `json:"rejected_items"`
	PublicPreimages    []publication.Destination `json:"public_preimages"`
}
