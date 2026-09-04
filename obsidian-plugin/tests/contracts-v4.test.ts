import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  assertProblemGraph,
  assertSnapshotBindings,
  codeOf,
  parseAgentAnnotationV1,
  parseCandidateListV1,
  parseConversationChainV1,
  parseMachineLedgerV4,
  parsePricingSnapshotV1,
  parsePricingSupplementV1,
  parseProblemMapCandidateV1,
  parseReviewPresentationV4,
  parseSessionEventPageV1,
  parseSessionIndexV1,
  parseSessionSummaryV1,
  WireRejectionError,
  type WireRejectionCode
} from "../src/data/contracts-v4";
import type { ViewKind } from "../src/contracts/review-v4";

const here = dirname(fileURLToPath(import.meta.url));
const pluginFixture = (name: string): Promise<string> =>
  readFile(resolve(here, "fixtures/v4", name), "utf8");
const sharedFixture = (name: string): Promise<Buffer> =>
  readFile(resolve(here, "../../testdata/contracts/v4", name));

type JsonObject = Record<string, unknown>;
type Parser = (source: string) => unknown;

const contracts: ReadonlyArray<Readonly<{
  name: string;
  parser: Parser;
  invalidCode: WireRejectionCode;
}>> = [
  { name: "review-presentation-v4", parser: parseReviewPresentationV4, invalidCode: "wire_shape_invalid" },
  { name: "machine-ledger-v4", parser: parseMachineLedgerV4, invalidCode: "wire_contract_invalid" },
  { name: "session-index-v1", parser: parseSessionIndexV1, invalidCode: "wire_contract_invalid" },
  { name: "session-summary-v1", parser: parseSessionSummaryV1, invalidCode: "wire_shape_invalid" },
  { name: "session-event-page-v1", parser: parseSessionEventPageV1, invalidCode: "wire_contract_invalid" },
  { name: "agent-annotation-v1", parser: parseAgentAnnotationV1, invalidCode: "wire_shape_invalid" },
  { name: "pricing-snapshot-v1", parser: parsePricingSnapshotV1, invalidCode: "wire_contract_invalid" },
  { name: "pricing-supplement-v1", parser: parsePricingSupplementV1, invalidCode: "wire_contract_invalid" },
  { name: "conversation-chain-v1", parser: parseConversationChainV1, invalidCode: "wire_contract_invalid" },
  { name: "problem-map-candidate-v1", parser: parseProblemMapCandidateV1, invalidCode: "wire_contract_invalid" }
];

function captureRejection(action: () => unknown): WireRejectionError {
  try {
    action();
  } catch (error) {
    expect(error).toBeInstanceOf(WireRejectionError);
    return error as WireRejectionError;
  }
  throw new Error("expected parser rejection");
}

async function fixtureObject(name: string): Promise<JsonObject> {
  return JSON.parse(await pluginFixture(name)) as JsonObject;
}

function clone<T>(value: T): T {
  return structuredClone(value);
}

function decision(id: string, supersedes: string[], status = "active"): JsonObject {
  return {
    id,
    kind: "decision",
    occurred_at: "2026-09-04T00:00:00Z",
    title: id,
    rationale: "reason",
    impact: "impact",
    status,
    legacy_status_text: null,
    reevaluate_when: "later",
    supersedes,
    milestone_ids: [],
    session_refs: [],
    provenance: "human_created",
    pinned: false,
    revision: 1
  };
}

async function nonzeroBoundSnapshots(): Promise<{
  ledger: ReturnType<typeof parseMachineLedgerV4>;
  index: ReturnType<typeof parseSessionIndexV1>;
}> {
  const indexRaw = await fixtureObject("session-index-v1.valid.json");
  indexRaw.digest = "sha256:473d1dc1e8ebe67d6d14af9793c3272e0e78bc98b8c00c2cff2ba68111dc3565";
  const index = parseSessionIndexV1(JSON.stringify(indexRaw));
  const ledgerRaw = await fixtureObject("machine-ledger-v4.valid.json") as { sync_hashes: JsonObject };
  ledgerRaw.sync_hashes.session_index_digest = index.digest;
  ledgerRaw.sync_hashes.ledger_sha256 = "5330d4167966e653a320cb3e3582ad7c82fe568639f0b358d67124548d27f5b7";
  return { ledger: parseMachineLedgerV4(JSON.stringify(ledgerRaw)), index };
}

describe("frozen v4 contract fixture parity", () => {
  for (const contract of contracts) {
    it(`accepts the frozen ${contract.name} valid fixture through its production parser`, async () => {
      const source = await pluginFixture(`${contract.name}.valid.json`);
      expect(() => contract.parser(source)).not.toThrow();
    });

    it(`rejects the frozen ${contract.name} invalid fixture with its Go-compatible code`, async () => {
      const source = await pluginFixture(`${contract.name}.invalid.json`);
      expect(codeOf(captureRejection(() => contract.parser(source)))).toBe(contract.invalidCode);
    });

    it(`keeps both ${contract.name} fixtures byte-identical to the shared Go fixtures`, async () => {
      for (const suffix of ["valid", "invalid"] as const) {
        const name = `${contract.name}.${suffix}.json`;
        await expect(readFile(resolve(here, "fixtures/v4", name))).resolves.toEqual(await sharedFixture(name));
      }
    });
  }

  it("exposes CandidateListV1 as the agent-annotation-v1 typed view", async () => {
    const source = await pluginFixture("agent-annotation-v1.valid.json");
    expect(parseCandidateListV1(source)).toEqual(parseAgentAnnotationV1(source));
  });
});

describe("stable wire rejection codes", () => {
  it("classifies every JavaScript-representable wire rejection phase", async () => {
    const annotation = await pluginFixture("agent-annotation-v1.valid.json");
    const annotationObject = JSON.parse(annotation) as JsonObject;
    const unknown = { ...annotationObject, unknown: true };
    const invalidID = { ...annotationObject, project_id: "invalid project id" };
    const missing = { ...annotationObject };
    delete missing.project_id;
    const tooManyAnnotations = { ...annotationObject, annotations: Array(65537).fill(null) };
    const review = await fixtureObject("review-presentation-v4.valid.json");
    review.revision = -1;
    const pricing = await pluginFixture("pricing-snapshot-v1.invalid.json");
    const cases: ReadonlyArray<Readonly<{
      name: string;
      source: string;
      code: WireRejectionCode;
      parser?: Parser;
    }>> = [
      { name: "input overflow", source: `"${"a".repeat((64 << 20) + 1)}"`, code: "wire_input_overflow" },
      { name: "literal unpaired surrogate", source: annotation.replace("project-p", "\ud800"), code: "wire_invalid_utf8" },
      { name: "escaped unpaired surrogate", source: annotation.replace("project-p", "\\ud800"), code: "wire_invalid_utf8" },
      { name: "malformed JSON", source: "{", code: "wire_json_invalid" },
      { name: "duplicate key", source: '{"schema_version":1,"schema_version":1}', code: "wire_json_invalid" },
      { name: "trailing JSON value", source: `${annotation} {}`, code: "wire_json_invalid" },
      { name: "wrong root container", source: "[]", code: "wire_shape_invalid" },
      { name: "unknown exact key", source: JSON.stringify(unknown), code: "wire_shape_invalid" },
      { name: "missing required key", source: JSON.stringify(missing), code: "wire_shape_invalid" },
      { name: "null in required scalar", source: JSON.stringify({ ...annotationObject, project_id: null }), code: "wire_shape_invalid" },
      { name: "wrong scalar type", source: JSON.stringify({ ...annotationObject, project_id: 7 }), code: "wire_shape_invalid" },
      { name: "correctly typed invalid format", source: JSON.stringify(invalidID), code: "wire_contract_invalid" },
      { name: "scalar byte limit", source: JSON.stringify({ ...annotationObject, project_id: "a".repeat(257) }), code: "wire_contract_invalid" },
      { name: "array item limit", source: JSON.stringify(tooManyAnnotations), code: "wire_contract_invalid" },
      { name: "numeric range", source: JSON.stringify(review), code: "wire_contract_invalid", parser: parseReviewPresentationV4 },
      { name: "closed enum", source: pricing, code: "wire_contract_invalid", parser: parsePricingSnapshotV1 }
    ];
    for (const testCase of cases) {
      expect(codeOf(captureRejection(() => (testCase.parser ?? parseAgentAnnotationV1)(testCase.source))), testCase.name)
        .toBe(testCase.code);
    }
  });

  it("preserves the production diagnostic message and cause", async () => {
    const annotation = await fixtureObject("agent-annotation-v1.valid.json");
    annotation.project_id = "invalid project id";
    const rejection = captureRejection(() => parseAgentAnnotationV1(JSON.stringify(annotation)));
    expect(rejection.cause).toBeInstanceOf(Error);
    expect(rejection.message).toBe((rejection.cause as Error).message);
    expect(codeOf(rejection)).toBe("wire_contract_invalid");
  });

  it("preserves a nested specific code and ignores unrelated errors", async () => {
    const source = await pluginFixture("agent-annotation-v1.valid.json");
    const malformed = source.replace("project-p", "\\ud800");
    const rejection = captureRejection(() => parseCandidateListV1(malformed));
    expect(codeOf(rejection)).toBe("wire_invalid_utf8");
    expect(codeOf(new Error("ordinary failure"))).toBeUndefined();
    expect(codeOf("not an error")).toBeUndefined();
  });

  it("preserves the native parser error behind malformed JSON string context", () => {
    const rejection = captureRejection(() => parseAgentAnnotationV1('{"schema_version":"\\q"}'));
    expect(codeOf(rejection)).toBe("wire_json_invalid");
    expect(rejection.message).toMatch(/decode JSON: malformed string/i);
    const causes: unknown[] = [];
    let current: unknown = rejection;
    while (typeof current === "object" && current !== null && !causes.includes(current)) {
      causes.push(current);
      current = (current as { cause?: unknown }).cause;
    }
    expect(causes.some((cause) => cause instanceof SyntaxError)).toBe(true);
  });
});

describe("strict JSON boundary", () => {
  it("rejects duplicate keys at nested object depth", () => {
    const source = '{"schema_version":1,"minimum_reader_version":"0.4.0","project_id":"p","annotations":[],"extraction_runs":[{"run_id":"a","run_id":"b"}]}';
    expect(() => parseAgentAnnotationV1(source)).toThrow(/duplicate/i);
  });

  it("rejects case aliases, unknown keys, and missing keys recursively", async () => {
    const valid = await fixtureObject("review-presentation-v4.valid.json");
    const alias = clone(valid) as { current_state: JsonObject };
    alias.current_state.Goal = "alias";
    expect(() => parseReviewPresentationV4(JSON.stringify(alias))).toThrow(/unknown|exact/i);

    const unknown = clone(valid) as { current_state: JsonObject };
    unknown.current_state.unknown = true;
    expect(() => parseReviewPresentationV4(JSON.stringify(unknown))).toThrow(/unknown|exact/i);

    const missing = clone(valid) as { current_state: JsonObject };
    delete missing.current_state.goal;
    expect(() => parseReviewPresentationV4(JSON.stringify(missing))).toThrow(/required|missing/i);
  });

  it("rejects literal and JSON-escaped unpaired surrogates", async () => {
    const source = await pluginFixture("agent-annotation-v1.valid.json");
    const literal = source.replace("project-p", "\ud800");
    expect(() => parseAgentAnnotationV1(literal)).toThrow(/surrogate|unicode/i);
    const escaped = source.replace("project-p", "\\ud800");
    expect(() => parseAgentAnnotationV1(escaped)).toThrow(/surrogate|unicode/i);
  });

  it("rejects input above the 64 MiB UTF-8 boundary before decoding", () => {
    const oversized = `"${"a".repeat((64 << 20) + 1)}"`;
    expect(() => parseAgentAnnotationV1(oversized)).toThrow(/67108864|64 MiB|byte/i);
  });

  it("rejects unsafe integers", async () => {
    const valid = await fixtureObject("review-presentation-v4.valid.json");
    valid.revision = Number.MAX_SAFE_INTEGER + 1;
    expect(() => parseReviewPresentationV4(JSON.stringify(valid))).toThrow(/safe integer/i);
  });

  it("applies string ceilings to UTF-8 bytes for CJK and emoji", async () => {
    const review = await fixtureObject("review-presentation-v4.valid.json") as { current_state: JsonObject };
    review.current_state.goal = `${"界".repeat(5461)}a`;
    expect(Buffer.byteLength(review.current_state.goal as string, "utf8")).toBe(16384);
    expect(() => parseReviewPresentationV4(JSON.stringify(review))).not.toThrow();
    review.current_state.goal = `${review.current_state.goal as string}界`;
    expect(() => parseReviewPresentationV4(JSON.stringify(review))).toThrow(/16384|byte/i);

    review.current_state.goal = "🙂".repeat(4096);
    expect(Buffer.byteLength(review.current_state.goal as string, "utf8")).toBe(16384);
    expect(() => parseReviewPresentationV4(JSON.stringify(review))).not.toThrow();
    review.current_state.goal = `${review.current_state.goal as string}🙂`;
    expect(() => parseReviewPresentationV4(JSON.stringify(review))).toThrow(/16384|byte/i);
  });

  it("applies pricing text ceilings to UTF-8 bytes", async () => {
    const snapshot = await fixtureObject("pricing-snapshot-v1.valid.json");
    snapshot.audit_reason = `${"价".repeat(1365)}a`;
    expect(Buffer.byteLength(snapshot.audit_reason as string, "utf8")).toBe(4096);
    expect(() => parsePricingSnapshotV1(JSON.stringify(snapshot))).not.toThrow();
    snapshot.audit_reason = `${snapshot.audit_reason as string}价`;
    expect(() => parsePricingSnapshotV1(JSON.stringify(snapshot))).toThrow(/4096|byte/i);
  });
});

describe("session contracts", () => {
  it("rejects counter addition overflow without relying on a wrapped sum", async () => {
    const summary = await fixtureObject("session-summary-v1.valid.json") as { coverage: JsonObject };
    summary.coverage.seen = 0;
    summary.coverage.indexed = Number.MAX_SAFE_INTEGER;
    summary.coverage.collapsed = 1;
    expect(() => parseSessionSummaryV1(JSON.stringify(summary))).toThrow(/overflow|reconcile/i);
  });

  it("uses provider plus session_id for uniqueness", async () => {
    const index = await fixtureObject("session-index-v1.valid.json") as {
      coverage: JsonObject;
      sessions: JsonObject[];
    };
    index.sessions.push({ ...clone(index.sessions[0]), provider: "codex" });
    Object.assign(index.coverage, {
      total: 2,
      complete: 2,
      source_available: 2,
      started_at_known: 2,
      ended_at_known: 2
    });
    expect(() => parseSessionIndexV1(JSON.stringify(index))).not.toThrow();

    index.sessions[1] = clone(index.sessions[0]);
    expect(() => parseSessionIndexV1(JSON.stringify(index))).toThrow(/duplicate/i);
  });

  it("accepts null session timestamps, counts only known values, and sorts null last", async () => {
    const source = await pluginFixture("session-index-v1.unknown.valid.json");
    const parsed = parseSessionIndexV1(source);
    expect(parsed.coverage.started_at_known).toBe(1);
    expect(parsed.coverage.ended_at_known).toBe(1);
    expect(parsed.sessions.map((session) => session.started_at)).toEqual(["2026-09-04T00:00:00Z", null]);
    await expect(readFile(resolve(here, "fixtures/v4/session-index-v1.unknown.valid.json")))
      .resolves.toEqual(await sharedFixture("session-index-v1.unknown.valid.json"));

    const malformed = JSON.parse(source) as { coverage: JsonObject };
    malformed.coverage.started_at_known = 2;
    expect(() => parseSessionIndexV1(JSON.stringify(malformed))).toThrow(/coverage|known|reconcile/i);
  });

  it("rejects mixed project, generation, project digest, and index digest bindings", async () => {
    const { ledger, index } = await nonzeroBoundSnapshots();
    expect(() => assertSnapshotBindings(ledger, index)).not.toThrow();

    for (const field of ["project_id", "generation_id", "project_view_digest"] as const) {
      const changed = clone(index);
      changed[field] = field === "project_view_digest" ? `sha256:${"9".repeat(64)}` : "other";
      expect(() => assertSnapshotBindings(ledger, changed)).toThrow(/mismatch|binding/i);
    }
    const wrongDigest = clone(index);
    wrongDigest.digest = `sha256:${"9".repeat(64)}`;
    const rejection = captureRejection(() => assertSnapshotBindings(ledger, wrongDigest));
    expect(rejection.message).toMatch(/mismatch|binding/i);
    expect(codeOf(rejection)).toBe("wire_contract_invalid");
  });

  it("rejects placeholder digests at the accepted snapshot binding boundary", async () => {
    const zeroLedger = parseMachineLedgerV4(await pluginFixture("machine-ledger-v4.valid.json"));
    const zeroIndex = parseSessionIndexV1(await pluginFixture("session-index-v1.valid.json"));
    zeroLedger.sync_hashes.session_index_digest = zeroIndex.digest;
    expect(() => assertSnapshotBindings(zeroLedger, zeroIndex)).toThrow(/unset|zero|placeholder|digest/i);

    const { ledger, index } = await nonzeroBoundSnapshots();
    const zeroSelfHash = clone(ledger);
    zeroSelfHash.sync_hashes.ledger_sha256 = "0".repeat(64);
    expect(() => assertSnapshotBindings(zeroSelfHash, index)).toThrow(/unset|zero|placeholder|hash/i);
    expect(() => assertSnapshotBindings(ledger, index)).not.toThrow();
  });

  it("rejects cyclic decision supersession graphs", async () => {
    const review = await fixtureObject("review-presentation-v4.valid.json") as { decisions: JsonObject[] };
    review.decisions = [decision("a", ["b"]), decision("b", ["a"])];
    expect(() => parseReviewPresentationV4(JSON.stringify(review))).toThrow(/cycle/i);
  });

  it("accepts only the lossless legacy decision status representation", async () => {
    const review = await fixtureObject("review-presentation-v4.valid.json") as { decisions: JsonObject[] };
    const legacy = decision("legacy", [], "legacy_unmapped");
    legacy.provenance = "migrated";
    legacy.legacy_status_text = "已采用";
    review.decisions = [legacy];
    expect(() => parseReviewPresentationV4(JSON.stringify(review))).not.toThrow();

    legacy.legacy_status_text = null;
    expect(() => parseReviewPresentationV4(JSON.stringify(review))).toThrow(/legacy|status|text/i);
    legacy.legacy_status_text = "已采用";
    legacy.status = "active";
    expect(() => parseReviewPresentationV4(JSON.stringify(review))).toThrow(/legacy|status|text/i);
  });

  it("rejects cursors on a zero-total event page", async () => {
    const page = await fixtureObject("session-event-page-v1.valid.json");
    page.previous_cursor = "cursor";
    expect(() => parseSessionEventPageV1(JSON.stringify(page))).toThrow(/empty|cursor/i);
  });

  it("verifies non-zero canonical index digests and ledger self hashes", async () => {
    const index = await fixtureObject("session-index-v1.valid.json");
    index.digest = "sha256:473d1dc1e8ebe67d6d14af9793c3272e0e78bc98b8c00c2cff2ba68111dc3565";
    expect(() => parseSessionIndexV1(JSON.stringify(index))).not.toThrow();
    index.digest = `sha256:${"9".repeat(64)}`;
    expect(() => parseSessionIndexV1(JSON.stringify(index))).toThrow(/digest/i);

    const ledger = await fixtureObject("machine-ledger-v4.valid.json") as { sync_hashes: JsonObject };
    ledger.sync_hashes.ledger_sha256 = "2649fac1e8df09ee7857f3c337bee613d5f48bad3faa3e1e14bf75ef4651b9b7";
    expect(() => parseMachineLedgerV4(JSON.stringify(ledger))).not.toThrow();
    ledger.sync_hashes.ledger_sha256 = "9".repeat(64);
    expect(() => parseMachineLedgerV4(JSON.stringify(ledger))).toThrow(/digest|hash/i);
  });
});

describe("conversation chain and problem map contracts", () => {
  it("keeps five view kinds including the formal problems view", () => {
    const kinds: ViewKind[] = ["evolution", "problems", "decisions", "sessions", "usage"];
    expect(kinds).toHaveLength(5);
  });

  it("rejects hidden roles, oversized UTF-8 excerpts, and raw tool output keys", async () => {
    const chain = await fixtureObject("conversation-chain-v1.valid.json") as { turn_units: JsonObject[] };
    const turn = chain.turn_units[0] as { user_message: JsonObject; actions: JsonObject[] };
    turn.user_message.role = "system";
    expect(() => parseConversationChainV1(JSON.stringify(chain))).toThrow(/role|enum|user/i);
    turn.user_message.role = "user";
    turn.user_message.visible_excerpt = "界".repeat(1366);
    expect(() => parseConversationChainV1(JSON.stringify(chain))).toThrow(/4096|byte/i);
    turn.user_message.visible_excerpt = "question";
    turn.actions[0].raw_tool_output = { secret: true };
    expect(codeOf(captureRejection(() => parseConversationChainV1(JSON.stringify(chain))))).toBe("wire_shape_invalid");
  });

  it("binds both new contracts to canonical digests that omit only their digest field", async () => {
    const chain = await fixtureObject("conversation-chain-v1.valid.json");
    chain.segmentation_rule_version = "visible-turn-v2";
    expect(() => parseConversationChainV1(JSON.stringify(chain))).toThrow(/digest/i);

	const zeroChain = await fixtureObject("conversation-chain-v1.valid.json");
	zeroChain.digest = `sha256:${"0".repeat(64)}`;
	expect(() => parseConversationChainV1(JSON.stringify(zeroChain))).toThrow(/digest/i);

    const candidates = await fixtureObject("problem-map-candidate-v1.valid.json") as { candidates: JsonObject[] };
    candidates.candidates[0].question = "Tampered question?";
    expect(() => parseProblemMapCandidateV1(JSON.stringify(candidates))).toThrow(/digest/i);

	const zeroCandidates = await fixtureObject("problem-map-candidate-v1.valid.json");
	zeroCandidates.digest = `sha256:${"0".repeat(64)}`;
	expect(() => parseProblemMapCandidateV1(JSON.stringify(zeroCandidates))).toThrow(/digest/i);
  });

  it("keeps revision-zero and integer maxima identical across v4 parsers", async () => {
	const review = await fixtureObject("review-presentation-v4.valid.json") as {
	  problem_map_revision: number;
	  problem_root_ids: string[];
	  problem_nodes: JsonObject[];
	};
	review.problem_nodes = [{
	  id: "problem-1", question: "Why?", primary_parent_id: null, related_node_ids: [],
	  workflow_state: "not_started", answer_state: "no_answer", completion_criterion: "",
	  current_conclusion: "", source_turn_refs: [], provenance: "human_created",
	  first_proposed_at: "2026-09-04T00:00:00Z", sibling_order: 0, confirmed_at: null, revision: 1
	}];
	review.problem_root_ids = ["problem-1"];
	expect(() => parseReviewPresentationV4(JSON.stringify(review))).toThrow(/revision|zero|positive/i);

	const chain = await fixtureObject("conversation-chain-v1.valid.json") as { coverage: JsonObject };
	chain.coverage.source_messages = Number.MAX_SAFE_INTEGER + 1;
	expect(() => parseConversationChainV1(JSON.stringify(chain))).toThrow(/integer|safe/i);

	const candidates = await fixtureObject("problem-map-candidate-v1.valid.json") as { candidates: JsonObject[] };
	candidates.candidates[0].revision = Number.MAX_SAFE_INTEGER + 1;
	expect(() => parseProblemMapCandidateV1(JSON.stringify(candidates))).toThrow(/integer|safe/i);
  });

  it("enforces formal problem graph cycles, relations, and sibling order", () => {
    const node = (id: string, parent: string | null, order: number): JsonObject => ({
      id, question: `${id}?`, primary_parent_id: parent, related_node_ids: [], workflow_state: "not_started",
      answer_state: "no_answer", completion_criterion: "", current_conclusion: "", source_turn_refs: [],
      provenance: "human_created", first_proposed_at: "2026-09-04T00:00:00Z", sibling_order: order,
      confirmed_at: null, revision: 1
    });
    const cycle = [node("a", "b", 0), node("b", "a", 0)];
    expect(() => assertProblemGraph(cycle as never)).toThrow(/cycle/i);
    const missing = [node("a", null, 0), { ...node("b", "a", 0), related_node_ids: ["missing"] }];
    expect(() => assertProblemGraph(missing as never)).toThrow(/missing|related/i);
    const siblings = [node("a", null, 0), node("b", "a", 0), node("c", "a", 0)];
    expect(() => assertProblemGraph(siblings as never)).toThrow(/sibling|order|duplicate/i);
  });

  it("requires honest missing conclusions and source-turn-backed milestone annotations", async () => {
    const review = await fixtureObject("review-presentation-v4.valid.json") as { timeline: JsonObject[] };
    review.timeline = [{
      id: "m", generation_id: "generation-1", occurred_at: "2026-09-04T00:00:00Z", kind: "milestone", title: "M", summary: "S", decision_ids: [],
      closed_loop: {
        trigger_question: { state: "missing", text: "", missing_reason: "not_captured", source_turn_refs: [] },
        conclusion: { kind: "missing", text: "invented", missing_reason: "not_captured", source_turn_refs: [] },
        execution: { state: "missing", text: "", missing_reason: "not_captured", source_turn_refs: [] },
        verification: { state: "missing", text: "", missing_reason: "not_captured", source_turn_refs: [] },
        impact_and_follow_up: { state: "missing", text: "", missing_reason: "not_captured", source_turn_refs: [] },
        source_turn_refs: [], coverage: { source_turns: 0, captured_turns: 0, truncated_turns: 0, source_unavailable_turns: 0 }
      }
    }];
    expect(() => parseReviewPresentationV4(JSON.stringify(review))).toThrow(/missing|conclusion|text/i);

    const annotation = await fixtureObject("agent-annotation-v1.valid.json") as { annotations: JsonObject[] };
    annotation.annotations[0].entity_id = "decision-only";
    expect(() => parseAgentAnnotationV1(JSON.stringify(annotation))).toThrow(/entity|milestone|unknown/i);
  });
});

describe("pricing and optional-field semantics", () => {
  it("distinguishes a complete free price from an unknown price", async () => {
    const snapshot = await fixtureObject("pricing-snapshot-v1.valid.json") as {
      rates: JsonObject;
      line_costs_usd: JsonObject;
      missing_billing_dimensions: string[];
      known_subtotal_usd: number;
      total_cost_usd: number | null;
      pricing_complete: boolean;
    };
    for (const key of ["input", "cached_input", "cache_write_input", "output", "reasoning_output"]) {
      snapshot.rates[key] = 0;
      snapshot.line_costs_usd[key] = 0;
    }
    snapshot.missing_billing_dimensions = [];
    snapshot.known_subtotal_usd = 0;
    snapshot.total_cost_usd = 0;
    snapshot.pricing_complete = true;
    expect(() => parsePricingSnapshotV1(JSON.stringify(snapshot))).not.toThrow();

    snapshot.rates.input = null;
    expect(() => parsePricingSnapshotV1(JSON.stringify(snapshot))).toThrow(/complete|unknown|availability/i);
  });

  it("requires incomplete pricing totals to remain null and report unknown billed dimensions", async () => {
    const snapshot = await fixtureObject("pricing-snapshot-v1.valid.json") as {
      rates: JsonObject;
      line_costs_usd: JsonObject;
      missing_billing_dimensions: string[];
      total_cost_usd: number | null;
    };
    snapshot.total_cost_usd = 0;
    expect(() => parsePricingSnapshotV1(JSON.stringify(snapshot))).toThrow(/incomplete|total/i);

    snapshot.total_cost_usd = null;
    snapshot.rates.output = null;
    snapshot.line_costs_usd.output = null;
    snapshot.missing_billing_dimensions = [];
    expect(() => parsePricingSnapshotV1(JSON.stringify(snapshot))).toThrow(/dimension|reported/i);
  });

  it("derives aggregate completeness from current pricing snapshots only", async () => {
    const ledger = await fixtureObject("machine-ledger-v4.valid.json") as {
      accounting: JsonObject;
      pricing_snapshots: JsonObject[];
      current_pricing_snapshot_ids: string[];
    };
    const complete = clone(ledger.pricing_snapshots[0]);
    ledger.pricing_snapshots[0].status = "superseded";
    complete.snapshot_id = "snapshot-current";
    complete.supersedes_snapshot_id = ledger.pricing_snapshots[0].snapshot_id;
    complete.pricing_complete = true;
    complete.rates = { input: 0, cached_input: 0, cache_write_input: 0, output: 0, reasoning_output: 0 };
    complete.line_costs_usd = { input: 0, cached_input: 0, cache_write_input: 0, output: 0, reasoning_output: 0 };
    complete.billable_quantities = { input: 0, cached_input: 0, cache_write_input: 0, output: 0, reasoning_output: 0 };
    complete.missing_billing_dimensions = [];
    complete.known_subtotal_usd = 0;
    complete.total_cost_usd = 0;
    ledger.pricing_snapshots.push(complete);
    ledger.current_pricing_snapshot_ids = ["snapshot-current"];
    ledger.accounting.total_cost_usd = 0;
    expect(() => parseMachineLedgerV4(JSON.stringify(ledger))).not.toThrow();

    ledger.current_pricing_snapshot_ids = ["snapshot-1"];
    expect(() => parseMachineLedgerV4(JSON.stringify(ledger))).toThrow(/aggregate|incomplete|null|current/i);
  });

  it("enforces a single identity-bound current leaf in each pricing history", async () => {
    const ledger = await fixtureObject("machine-ledger-v4.valid.json") as {
      pricing_snapshots: JsonObject[];
      current_pricing_snapshot_ids: string[];
    };
    const predecessor = clone(ledger.pricing_snapshots[0]);
    predecessor.status = "superseded";
    const successor = clone(ledger.pricing_snapshots[0]);
    successor.snapshot_id = "snapshot-successor";
    successor.supersedes_snapshot_id = predecessor.snapshot_id;
    ledger.pricing_snapshots = [predecessor, successor];
    ledger.current_pricing_snapshot_ids = [successor.snapshot_id as string];
    expect(() => parseMachineLedgerV4(JSON.stringify(ledger))).not.toThrow();

    for (const testCase of [
      { name: "missing predecessor", mutate: (copy: typeof ledger) => { copy.pricing_snapshots[1].supersedes_snapshot_id = "missing"; } },
      { name: "self reference", mutate: (copy: typeof ledger) => { copy.pricing_snapshots[1].supersedes_snapshot_id = copy.pricing_snapshots[1].snapshot_id; } },
      { name: "cycle", mutate: (copy: typeof ledger) => { copy.pricing_snapshots[0].supersedes_snapshot_id = copy.pricing_snapshots[1].snapshot_id; } },
      { name: "identity mismatch", mutate: (copy: typeof ledger) => { copy.pricing_snapshots[1].session_id = "other-session"; } },
      { name: "provider mismatch", mutate: (copy: typeof ledger) => { copy.pricing_snapshots[1].provider = "claude"; } },
      { name: "usage record mismatch", mutate: (copy: typeof ledger) => { copy.pricing_snapshots[1].usage_record_digest = `sha256:${"2".repeat(64)}`; } },
      { name: "branching successors", mutate: (copy: typeof ledger) => {
        const branch = clone(copy.pricing_snapshots[1]);
        branch.snapshot_id = "snapshot-branch";
        copy.pricing_snapshots.push(branch);
      } },
      { name: "non-leaf selected", mutate: (copy: typeof ledger) => { copy.pricing_snapshots[0].status = "current"; copy.current_pricing_snapshot_ids = [copy.pricing_snapshots[0].snapshot_id as string]; } },
      { name: "multiple effective leaves", mutate: (copy: typeof ledger) => {
        const branch = clone(copy.pricing_snapshots[1]);
        branch.snapshot_id = "snapshot-branch";
        branch.supersedes_snapshot_id = null;
        copy.pricing_snapshots.push(branch);
      } }
    ] as const) {
      const malformed = clone(ledger);
      testCase.mutate(malformed);
      expect(() => parseMachineLedgerV4(JSON.stringify(malformed)), testCase.name)
        .toThrow(/pricing|snapshot|predecessor|cycle|identity|leaf|current|branch/i);
    }
  });

  it("preserves explicit empty optional arrays instead of erasing them", async () => {
    const review = await fixtureObject("review-presentation-v4.valid.json") as {
      human_patches: JsonObject[];
    };
    review.human_patches = [{
      entity_id: "entity-1",
      field: "field-1",
      operation: "set",
      values: [],
      base_generated_hash: "1".repeat(64)
    }];
    const parsed = parseReviewPresentationV4(JSON.stringify(review));
    expect(parsed.human_patches[0]).toHaveProperty("values");
    expect(parsed.human_patches[0]?.values).toEqual([]);
    expect(parsed.human_patches[0]).not.toHaveProperty("value");
  });
});
