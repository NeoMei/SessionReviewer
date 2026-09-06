export function syncStatusFixture(projectId = "project-0123456789abcdef", overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    project_id: projectId, in_sync: 1, conflicted: 0, malformed: 0, queued: 0, blocked: 0,
    open_conflicts: [], pending: [], derived_state: "current", derived_files: 0,
    migration: "current", machine_state: "current", last_successful_sync: "",
    pending_operations: [], hidden_conflict_ids: [], ...overrides
  };
}
