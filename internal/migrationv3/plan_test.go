package migrationv3

import (
	"context"
	"os"
	"path/filepath"
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
	root := t.TempDir()
	for _, relative := range []string{reviewv2.ReviewRelativePath, reviewv2.HistoryRelativePath, reviewv2.MachineLedgerRelativePath} {
		body, err := os.ReadFile(filepath.Join("../../testdata/contracts/migration/v2", relative))
		if err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	accepted, err := reviewv2.Load(root)
	if err != nil {
		t.Fatalf("load complete v2 artifact set: %v", err)
	}
	in := Input{
		ProjectID:          accepted.State.Review.ProjectID,
		PreparedGeneration: "generation-v3-target",
		AcceptedV2:         accepted,
	}
	plan, err := BuildPlan(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SourceRevision != 2 || plan.PreparedGeneration != "generation-v3-target" || len(plan.LegacyItems) != 0 {
		t.Fatalf("legacy v2/v3 compatibility plan changed: %+v", plan)
	}
}
