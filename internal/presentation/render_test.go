package presentation

import (
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

func projectViewFixture() memory.ProjectView {
	observation := digest64("5")
	view := memory.ProjectView{
		SchemaVersion: 1, ProjectID: "project-demo", Generation: 1,
		StartedAt: "2026-09-01T00:00:00Z", EndedAt: "2026-09-02T00:00:00Z",
		SourceSessions: 2, TerminalCounts: memory.TerminalCounts{Indexed: 2},
		SessionViewDependencies: []memory.SessionViewDependency{
			{Provider: "codex", SessionID: "session-demo", Digest: digest64("6")},
			{Provider: "codex", SessionID: "session-other", Digest: digest64("a")},
		},
		ObservationRevisionIDs: []string{observation},
		ProbeStateDigest:       digest64("7"), LiveState: memory.StateSnapshot{Branch: "main", Head: strings40("a"), DirtyPathCount: 2},
		WitnessedState: []memory.DerivedRecord{{
			ID: "witness-branch", Kind: "witnessed_state", Subject: "branch", OccurredAt: "2026-09-01T00:00:00Z",
			DependencyRevisionIDs: []string{observation}, RuleID: "newest", RuleVersion: "v1", Fields: map[string]string{"value": "main"},
		}},
		AggregationCoverage: memory.ProjectAggregationCoverage{
			ObservationSummariesSeen:  1,
			WitnessedKeys:             memory.AggregationChannelCoverage{Seen: 1, Emitted: 1},
			EventReferences:           memory.AggregationChannelCoverage{Seen: 1, Emitted: 1},
			SelectedEvidenceRevisions: memory.AggregationChannelCoverage{Seen: 2, Emitted: 1, Collapsed: 1},
		},
		DerivedRecords: []memory.DerivedRecord{{
			ID: "event-ref-a", Kind: "event_ref", Subject: "完成零 token 扫描", OccurredAt: "2026-09-01T01:00:00Z",
			DependencyRevisionIDs: []string{observation}, RuleID: "event", RuleVersion: "v1",
			Fields: map[string]string{"provider": "codex", "session_id": "session-demo", "sequence": "1", "fact_kind": "verification"},
		}},
		AssociatedUsage: []memory.AssociatedUsage{
			{Provider: "codex", SessionID: "session-demo", UsageRecordDigest: digest64("8"), Shared: true},
			{Provider: "codex", SessionID: "session-other", UsageRecordDigest: digest64("b"), Shared: false},
		},
		DependencyDigest: digest64("9"), ReducerVersion: "project-view-v1",
	}
	digest, err := memory.ProjectViewDigest(view)
	if err != nil {
		panic(err)
	}
	view.Digest = digest
	return view
}

func legacyPresentationFixture() reviewv2.LegacyPresentation {
	return reviewv2.LegacyPresentation{
		Review: reviewv2.Review{
			Name: "Demo", Goal: "自动目标", Stage: "main", Status: "自动状态", NextAction: "自动下一步",
			Risks:     []reviewv2.Risk{{ID: "risk-demo", Title: "自动风险", Status: "open", Detail: "自动风险详情"}},
			Decisions: []reviewv2.Decision{{ID: "decision-demo", Title: "自动决策", Rationale: "自动原因", Impact: "自动影响", Status: "accepted"}},
		},
		Events: []reviewv2.Event{{
			ID: "event-demo", OccurredAt: "2026-09-01T02:00:00Z", Kind: "verification", Title: "自动历史",
			Meaning: "自动意义", Summary: "自动摘要", Why: "自动原因", Changes: []string{"自动变更"},
			Results: []string{"自动结果"}, Next: "自动下一步",
		}},
		Compatibility: reviewv2.LegacyCompatibility{
			Timeline: []ledger.TimelineEvent{}, Decisions: []ledger.Decision{},
			OpenLoops: []ledger.OpenLoop{}, CurrentRisks: []reviewv2.CurrentRiskProvenance{},
		},
	}
}
