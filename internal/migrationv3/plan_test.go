package migrationv3

import (
	"context"
	"testing"

	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

func TestMigrationV3PlanDeterministic(t *testing.T) {
	in := Input{
		ProjectID:          "project-mig-test",
		PreparedGeneration: "scan-gen-001",
		AcceptedV2: reviewv2.Accepted{
			State: reviewv2.State{
				Review: reviewv2.Review{
					ProjectID: "project-mig-test",
					Revision:  3,
					Decisions: []reviewv2.Decision{{ID: "decision-1", Title: "Keep local", Status: "decided"}},
					Risks:     []reviewv2.Risk{{ID: "risk-1", Title: "Migration risk", Status: "open"}},
				},
			},
		},
	}

	plan1, err := BuildPlan(context.Background(), in)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	plan2, err := BuildPlan(context.Background(), in)
	if err != nil {
		t.Fatalf("BuildPlan 2: %v", err)
	}
	if plan1.ProjectID != plan2.ProjectID || len(plan1.LegacyItems) != len(plan2.LegacyItems) {
		t.Fatal("plans differ")
	}
	if len(plan1.LegacyItems) != 2 {
		t.Fatalf("expected 2 legacy items, got %d", len(plan1.LegacyItems))
	}
}

func TestCompatibilityV2StillUsesMigrationV3Plan(t *testing.T) {
	in := Input{
		ProjectID:          "project-v2-compatibility",
		PreparedGeneration: "generation-v3-target",
		AcceptedV2: reviewv2.Accepted{State: reviewv2.State{Review: reviewv2.Review{
			ProjectID: "project-v2-compatibility",
			Revision:  2,
			Decisions: []reviewv2.Decision{{ID: "decision-v2", Title: "Preserve v2 route", Status: "active"}},
		}}},
	}
	plan, err := BuildPlan(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SourceRevision != 2 || plan.PreparedGeneration != "generation-v3-target" || len(plan.LegacyItems) != 1 || plan.LegacyItems[0].EntityID != "decision-v2" {
		t.Fatalf("legacy v2/v3 compatibility plan changed: %+v", plan)
	}
}
