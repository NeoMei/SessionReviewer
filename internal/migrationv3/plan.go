package migrationv3

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/publication"
)

func BuildPlan(ctx context.Context, in Input) (Plan, error) {
	if ctx == nil {
		return Plan{}, errors.New("context is required")
	}
	if in.ProjectID == "" || in.PreparedGeneration == "" {
		return Plan{}, errors.New("project ID and prepared generation are required")
	}

	var legacyItems []LegacyItem
	var rejectedItems []RejectedLegacyItem
	var humanPatches []presentation.Patch

	// Classify decisions from v2 state
	for _, dec := range in.AcceptedV2.State.Review.Decisions {
		if dec.ID == "" || dec.Title == "" {
			rejectedItems = append(rejectedItems, RejectedLegacyItem{
				EntityID: dec.ID,
				Kind:     "decision",
				Reason:   "missing ID or title",
			})
			continue
		}
		class := LegacyHumanApproved
		legacyItems = append(legacyItems, LegacyItem{
			EntityID:  dec.ID,
			Kind:      "decision",
			Class:     class,
			Title:     dec.Title,
			Rationale: dec.Rationale,
			Impact:    dec.Impact,
			Status:    dec.Status,
		})
	}

	// Classify risks from v2 state
	for _, risk := range in.AcceptedV2.State.Review.Risks {
		if risk.ID == "" || risk.Title == "" {
			rejectedItems = append(rejectedItems, RejectedLegacyItem{
				EntityID: risk.ID,
				Kind:     "risk",
				Reason:   "missing ID or title",
			})
			continue
		}
		class := LegacyHumanApproved
		legacyItems = append(legacyItems, LegacyItem{
			EntityID: risk.ID,
			Kind:     "risk",
			Class:    class,
			Title:    risk.Title,
			Detail:   risk.Detail,
			Status:   risk.Status,
		})
	}

	sort.Slice(legacyItems, func(i, j int) bool {
		return legacyItems[i].EntityID < legacyItems[j].EntityID
	})
	sort.Slice(rejectedItems, func(i, j int) bool {
		return rejectedItems[i].EntityID < rejectedItems[j].EntityID
	})

	plan := Plan{
		SchemaVersion:      1,
		ProjectID:          in.ProjectID,
		SourceRevision:     in.AcceptedV2.State.Review.Revision,
		PreparedGeneration: in.PreparedGeneration,
		HumanPatches:       humanPatches,
		LegacyItems:        legacyItems,
		RejectedItems:      rejectedItems,
		PublicPreimages:    append([]publication.Destination(nil), in.PublicPreimages...),
	}
	if err := ValidatePlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func ValidatePlan(p Plan) error {
	if p.SchemaVersion != 1 {
		return fmt.Errorf("unsupported migration plan schema version %d", p.SchemaVersion)
	}
	if p.ProjectID == "" {
		return errors.New("migration plan project ID is required")
	}
	if p.PreparedGeneration == "" {
		return errors.New("migration plan prepared generation is required")
	}
	return nil
}
