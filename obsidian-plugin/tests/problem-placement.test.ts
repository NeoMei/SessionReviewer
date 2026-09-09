import { expect, it, vi } from "vitest";
import { parseProblemPlacementJob, type ProblemPlacementJob } from "../src/cli/problem-placement";
import { renderProblemPlacement } from "../src/view/problem-placement";
import { renderV4Problems } from "../src/view/render-v4-problems";
import type { ProblemCandidateV1 } from "../src/view/render-v4-problems";
import { v4PresentationFixture } from "./fixtures/v4-shell";

const candidate = { candidate_id: "candidate-one", project_id: "project-p", question: "Where does this belong?", source_turn_refs: [], recommended_relation: "keep_pending", recommended_target_id: null, alternate_target_ids: [], related_node_ids: [], grounds: [], confidence: "low", status: "pending", dependency_digests: [], analysis_mode: "deterministic", agent_run_id: null, revision: 2, created_at: "2026-09-09T00:00:00Z", updated_at: "2026-09-09T00:00:00Z" } as ProblemCandidateV1;
const running = { schema_version: 1, job_id: "problem-job-one", project_id: "project-p", candidate_id: "candidate-one", state: "running", revision: 2, result_candidate_revision: 0, error_code: null, can_cancel: true } as ProblemPlacementJob;
const tick = () => new Promise((done) => setTimeout(done, 0));

it("requires explicit Agent consent, cancels the exact job revision, and never confirms the graph", async () => {
  let starts = 0; let cancelled: unknown;
  const root = renderProblemPlacement(candidate, { start: async () => { starts++; return running; }, status: async () => running, cancel: async (jobId, revision) => { cancelled = { jobId, revision }; return { ...running, state: "cancelled", revision: 3, can_cancel: false }; } }, () => {});
  document.body.append(root);
  root.querySelector<HTMLButtonElement>('[data-action="prepare-problem-placement"]')!.click(); expect(starts).toBe(0); expect(root.textContent).toContain("消耗模型用量");
  root.querySelector<HTMLButtonElement>('[data-action="confirm-problem-placement"]')!.click(); await tick(); expect(starts).toBe(1);
  root.querySelector<HTMLButtonElement>('[data-action="cancel-problem-placement"]')!.click(); await tick(); expect(cancelled).toEqual({ jobId: "problem-job-one", revision: 2 }); expect(root.textContent).toContain("已取消");
  root.dispose(); root.remove();
});

it("disposes polling and ignores an old completion", async () => {
  vi.useFakeTimers(); let polls = 0; let completed = 0;
  const root = renderProblemPlacement(candidate, { start: async () => running, status: async () => { polls++; return running; }, cancel: async () => running }, () => { completed++; });
  try {
    root.querySelector<HTMLButtonElement>('[data-action="prepare-problem-placement"]')!.click(); root.querySelector<HTMLButtonElement>('[data-action="confirm-problem-placement"]')!.click(); await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(1000); expect(polls).toBe(1); expect(completed).toBe(0); root.dispose(); await vi.advanceTimersByTimeAsync(5000); expect(polls).toBe(1);
  } finally { root.dispose(); vi.useRealTimers(); }
});

it("strictly validates project, candidate and terminal result identity", () => {
  const completed = { ...running, state: "completed", revision: 3, result_candidate_revision: 3, can_cancel: false };
  expect(parseProblemPlacementJob(completed, "project-p", "candidate-one")).toEqual(completed);
  expect(() => parseProblemPlacementJob({ ...completed, project_id: "project-other" }, "project-p", "candidate-one")).toThrow();
  expect(() => parseProblemPlacementJob({ ...completed, candidate_id: "candidate-other" }, "project-p", "candidate-one")).toThrow();
  expect(() => parseProblemPlacementJob({ ...completed, extra: true }, "project-p", "candidate-one")).toThrow();
});

it("mounts Agent placement only inside a pending candidate and disposes it with the problems panel", () => {
  const presentation = v4PresentationFixture();
  const root = renderV4Problems(presentation, { projectId: "project-p", view: "problems", selectedMilestoneId: null, selectedProblemId: null }, () => {}, () => {}, { candidates: [candidate], agentPlacement: { start: async () => running, status: async () => running, cancel: async () => running } });
  document.body.append(root);
  expect(root.querySelector('[data-problem-candidate-id="candidate-one"] [data-action="prepare-problem-placement"]')).not.toBeNull();
  root.dispose(); expect(root.childElementCount).toBe(0); root.remove();
});

it("restores a persisted active placement after reload without starting a new Agent call", async () => {
  let starts = 0;
  const root = renderProblemPlacement(candidate, { start: async () => { starts++; return running; }, status: async () => running, cancel: async () => running, latest: async () => running }, () => {});
  document.body.append(root); await tick();
  expect(starts).toBe(0); expect(root.textContent).toContain("正在生成归类建议"); expect(root.querySelector('[data-action="cancel-problem-placement"]')).not.toBeNull();
  root.dispose(); root.remove();
});

it("sends fixed placement commands and binds results to project and candidate", async () => {
  const { CliRunner } = await import("../src/cli/runner");
  let argv: readonly string[] = [];
  const result = { schema_version: 1, job_id: "placement-job-one", project_id: "project-p", candidate_id: "candidate-one", state: "queued", revision: 1, result_candidate_revision: 0, error_code: null, can_cancel: true };
  const runner = new CliRunner("/bin/sr", (_file, args, options, callback) => { argv = args; expect(options.shell).toBe(false); callback(null, JSON.stringify(result), ""); });
  expect(typeof runner.startProblemPlacement).toBe("function");
  await expect(runner.startProblemPlacement("project-p", {candidate_id:"candidate-one",revision:3}, 2, "generation-one")).resolves.toMatchObject({state:"queued"});
  expect(argv).toEqual(["problems","placement","request","--project-id","project-p","--candidate-id","candidate-one","--expected-candidate-revision","3","--expected-problem-map-revision","2","--expected-generation-id","generation-one","--json"]);
  await expect(runner.startProblemPlacement("project-other", {candidate_id:"candidate-one",revision:3}, 2, "generation-one")).rejects.toThrow();
});

it("distinguishes no saved placement from a failed lookup",async()=>{
 const {CliRunner}=await import("../src/cli/runner");let fail=false;
 const runner=new CliRunner("/bin/sr",(_file,args,_options,callback)=>{expect(args).toEqual(["problems","placement","status","--project-id","project-p","--candidate-id","candidate-one","--json"]);callback(fail?new Error("unavailable"):null,fail?'{}':'null',"");});
 await expect(runner.latestProblemPlacement("project-p","candidate-one")).resolves.toBeUndefined();fail=true;await expect(runner.latestProblemPlacement("project-p","candidate-one")).rejects.toThrow();
});
