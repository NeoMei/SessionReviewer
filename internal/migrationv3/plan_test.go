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
