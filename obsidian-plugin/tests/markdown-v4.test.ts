import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { codeOf, parseMachineLedgerV4 } from "../src/data/contracts-v4";
import { markdownCodeOf, parseMarkdownV4, renderProblemTreeV4 } from "../src/data/markdown-v4";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../../testdata/contracts/v4/markdown");
const read = (name: string): string => readFileSync(resolve(root, name), "utf8");

interface CorpusCase {
  name: string;
  review: string;
  history: string;
  ledger: string;
  expected_code: string;
  expected_document_code?: string;
  expected_markdown_code?: string;
  expected_fields: Record<string, string>;
}

const cases = (JSON.parse(read("cases.json")) as { cases: CorpusCase[] }).cases;

describe("v4 human Markdown codec", () => {
  it("keeps raw-document diagnostics distinct from ledger-backed draft diagnostics", () => {
    const draftOnly = cases.filter((entry) => entry.expected_markdown_code && !entry.expected_document_code);
    expect(draftOnly.map((entry) => entry.name)).toEqual([
      "draft-duplicate-baseline",
      "draft-invalid-baseline-hash",
      "draft-baseline-generation-mismatch",
      "draft-baseline-kind-mismatch",
      "draft-baseline-value-missing",
      "draft-baseline-values-present",
      "draft-duplicate-human-patch",
      "draft-orphan-patch-collision"
    ]);
  });

  it.each(cases)("matches the shared corpus for $name", (entry) => {
    let ledger;
    try {
      ledger = parseMachineLedgerV4(read(entry.ledger));
    } catch (error) {
      expect(codeOf(error)).toBe(entry.expected_code);
      return;
    }
    expect(entry.expected_code).toBe("");
    let result: ReturnType<typeof parseMarkdownV4> | undefined;
    let markdownError: unknown;
    try { result = parseMarkdownV4({ review: read(entry.review), history: read(entry.history) }, ledger); }
    catch (error) { markdownError = error; }
    expect(markdownCodeOf(markdownError)).toBe(entry.expected_markdown_code);
    if (entry.expected_markdown_code) return;
    expect(result).toBeDefined();
    const fields = Object.fromEntries(result!.fields.map((field) => [`${field.entity}/${field.name}`, field.value]));
    expect(fields).toMatchObject(entry.expected_fields);
  });

  it("does not promote a local edit to an accepted machine state", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    expect(pair.review.split("项目目标夹具")).toHaveLength(2);
    const before = structuredClone(ledger);

    const result = parseMarkdownV4({ ...pair, review: pair.review.replace("项目目标夹具", "人工编辑目标") }, ledger);

    expect(result.changedFields).toContainEqual({ entity: "project-overview", name: "goal" });
    expect(result.presentation.current_state.goal).toBe("人工编辑目标");
    expect(ledger).toEqual(before);
  });

  it("treats custom shell edits as pending without changing typed fields", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    const result = parseMarkdownV4({ ...pair, review: `${pair.review}\n人工附注。\n` }, ledger);

    expect(result.changedFields).toEqual([]);
    expect(result.changedDocuments).toEqual(["review"]);
    expect(result.presentation.revision).toBe(ledger.accepted_revision + 1);
  });

  it("accepts composite and non-integer user YAML while rejecting nested merge keys", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    const userYaml = "custom:\n  list: [one, null, 1.5]\n  map: {enabled: true}\n";
    const review = pair.review.replace("---\n# 项目回顾", `${userYaml}---\n# 项目回顾`);

    expect(() => parseMarkdownV4({ ...pair, review }, ledger)).not.toThrow();
    const nestedMerge = pair.review.replace("---\n# 项目回顾", "custom:\n  <<: {enabled: true}\n---\n# 项目回顾");
    expect(() => parseMarkdownV4({ ...pair, review: nestedMerge }, ledger)).toThrowError(expect.objectContaining({ code: "markdown_format_invalid" }));
  });

  it("rejects removal of a stable entity anchor as a structural edit", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    const review = pair.review.replace('<a id="decision-x6465636973696f6e3a616c706861"></a>\n', "");
    expect(review).not.toBe(pair.review);

    expect(() => parseMarkdownV4({ ...pair, review }, ledger)).toThrowError(expect.objectContaining({ code: "markdown_structure_edit_requires_command" }));
  });

  it("scans a large legal history shell without per-entity rescans", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    const custom = Array.from({ length: 20_000 }, (_, index) => `<a id="custom-${index}"></a>`).join("\n");

    const result = parseMarkdownV4({ ...pair, history: `${pair.history}\n${custom}\n` }, ledger);

    expect(result.changedDocuments).toEqual(["history"]);
    expect(result.changedFields).toEqual([]);
  });

  it("preserves verification while upgrading a nonempty human milestone conclusion", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    const history = pair.history.replace("人工字段可编辑，结构仍需显式操作。", "人工确认的新结论");
    const beforeVerification = structuredClone(ledger.document_projection!.presentation_base.timeline[0].closed_loop.verification);

    const result = parseMarkdownV4({ ...pair, history }, ledger);

    expect(result.presentation.timeline[0].closed_loop.conclusion).toMatchObject({ kind: "human_confirmed", text: "人工确认的新结论", missing_reason: null });
    expect(result.presentation.timeline[0].closed_loop.verification).toEqual(beforeVerification);
  });

  it("rejects clearing a non-missing milestone conclusion", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const pair = { review: read("review.md"), history: read("history.md") };
    const history = pair.history.replace("人工字段可编辑，结构仍需显式操作。", "");

    expect(() => parseMarkdownV4({ ...pair, history }, ledger)).toThrowError(expect.objectContaining({ code: "markdown_structure_edit_requires_command" }));
  });

  it("updates one valid existing patch without mutating the accepted ledger", () => {
    const ledger = parseMachineLedgerV4(read("ledger-existing-patch.json"));
    const before = structuredClone(ledger);

    const result = parseMarkdownV4({ review: read("review-existing-patch-draft.md"), history: read("history.md") }, ledger);

    expect(result.presentation.current_state.goal).toBe("再次人工编辑目标");
    expect(result.presentation.human_patches).toHaveLength(1);
    expect(result.presentation.human_patches[0]).toMatchObject({ entity_id: "project-overview", field: "goal", operation: "set", value: "再次人工编辑目标" });
    expect(result.presentation.generated_baselines).toEqual(before.generated_baselines);
    expect(ledger).toEqual(before);
  });

  it("updates a carried human patch after an authenticated generation advance", () => {
    const ledger = structuredClone(parseMachineLedgerV4(read("ledger-existing-patch.json")));
    ledger.generation_id = "generation-after-scan";
    ledger.document_projection!.presentation_base.generation_id = "generation-after-scan";
    for (const milestone of ledger.document_projection!.presentation_base.timeline) milestone.generation_id = "generation-after-scan";
    ledger.document_projection!.presentation_base.generated_baselines[0].generation_id = "generation-after-scan";
    const review = read("review-existing-patch-draft.md").replaceAll("generation-1", "generation-after-scan");

    const result = parseMarkdownV4({ review, history: read("history.md").replaceAll("generation-1", "generation-after-scan") }, ledger);

    expect(result.presentation.current_state.goal).toBe("再次人工编辑目标");
    expect(result.presentation.generated_baselines[0]).toMatchObject({
      generation_id: "generation-after-scan",
      value: "项目目标夹具",
      generated_hash: ledger.generated_baselines[0].generated_hash
    });
  });

  it("removes an existing human patch when the field returns to its generated baseline", () => {
    const ledger = parseMachineLedgerV4(read("ledger-existing-patch.json"));

    const result = parseMarkdownV4({ review: read("review.md"), history: read("history.md") }, ledger);

    expect(result.presentation.current_state.goal).toBe("项目目标夹具");
    expect(result.presentation.human_patches).toEqual([]);
    expect(result.presentation.generated_baselines).toEqual(ledger.generated_baselines);
  });

  it("accepts a second edit after a restored baseline is carried to a new authenticated generation", () => {
    const ledger = parseMachineLedgerV4(read("ledger-existing-patch.json"));
    const restored = parseMarkdownV4({ review: read("review.md"), history: read("history.md") }, ledger);
    expect(restored.presentation.human_patches).toEqual([]);

    const nextLedger = structuredClone(ledger);
    nextLedger.generation_id = "generation-after-restore";
    nextLedger.document_projection!.presentation_base = structuredClone(restored.presentation);
    nextLedger.document_projection!.presentation_base.generation_id = "generation-after-restore";
    for (const milestone of nextLedger.document_projection!.presentation_base.timeline) milestone.generation_id = "generation-after-restore";
    nextLedger.document_projection!.presentation_base.generated_baselines[0].generation_id = "generation-after-restore";
    nextLedger.generated_baselines = structuredClone(nextLedger.document_projection!.presentation_base.generated_baselines);
    const nextReview = read("review-existing-patch-draft.md")
      .replace("revision: 1", `revision: ${restored.presentation.revision}`)
      .replaceAll("generation-1", "generation-after-restore");
    const nextHistory = read("history.md")
      .replace("revision: 1", `revision: ${restored.presentation.revision}`)
      .replaceAll("generation-1", "generation-after-restore");

    const editedAgain = parseMarkdownV4({ review: nextReview, history: nextHistory }, nextLedger);

    expect(editedAgain.presentation.current_state.goal).toBe("再次人工编辑目标");
    expect(editedAgain.presentation.human_patches[0]).toMatchObject({
      value: "再次人工编辑目标",
      base_generated_hash: restored.presentation.generated_baselines[0].generated_hash
    });
    expect(editedAgain.presentation.generated_baselines[0]).toMatchObject({
      generation_id: "generation-after-restore",
      value: "项目目标夹具",
      generated_hash: restored.presentation.generated_baselines[0].generated_hash
    });
  });

  it("updates and restores one uniquely resolved legacy bare non-project baseline", () => {
    const ledger = structuredClone(parseMachineLedgerV4(read("ledger.json")));
    const decision = ledger.document_projection!.presentation_base.decisions[0];
    const generated = "legacy generated decision";
    const generatedHash = "e339ea33de501b152e7c3058731988176b62df4d5c72f58bacd80e7e9799b19b";
    decision.title = "legacy human decision";
    const baseline = { generation_id: ledger.generation_id, entity_id: decision.id, field: "title", kind: "scalar" as const, value: generated, generated_hash: generatedHash };
    const patch = { entity_id: decision.id, field: "title", operation: "set" as const, value: decision.title, base_generated_hash: generatedHash };
    ledger.document_projection!.presentation_base.generated_baselines = [baseline];
    ledger.document_projection!.presentation_base.human_patches = [patch];
    ledger.generated_baselines = structuredClone(ledger.document_projection!.presentation_base.generated_baselines);
    ledger.human_patches = structuredClone(ledger.document_projection!.presentation_base.human_patches);
    const acceptedReview = read("review.md").replace("选择 [Markdown] 作为人工编辑面。", decision.title);

    const edited = parseMarkdownV4({ review: acceptedReview.replace(decision.title, "edited migrated decision"), history: read("history.md") }, ledger);
    expect(edited.presentation.generated_baselines).toEqual([baseline]);
    expect(edited.presentation.human_patches).toEqual([{ ...patch, value: "edited migrated decision" }]);

    const restored = parseMarkdownV4({ review: acceptedReview.replace(decision.title, generated), history: read("history.md") }, ledger);
    expect(restored.presentation.generated_baselines).toEqual([baseline]);
    expect(restored.presentation.human_patches).toEqual([]);
  });

  it("rejects simultaneous bare and qualified baselines for one semantic field", () => {
    const ledger = structuredClone(parseMachineLedgerV4(read("ledger.json")));
    const decision = ledger.document_projection!.presentation_base.decisions[0];
    const value = decision.title;
    const bare = { generation_id: ledger.generation_id, entity_id: decision.id, field: "title", kind: "scalar" as const, value, generated_hash: "a9816e82efb51cbf2bed59687f40939a9c4e5b54aff984778267a777737fc4b9" };
    const qualified = { generation_id: ledger.generation_id, entity_id: `decision:${decision.id}`, field: "title", kind: "scalar" as const, value, generated_hash: "19ffa1597d3b097a53f7bfe8f73a42341ef3f6eee23e2cf2684ce7ae80fd2c63" };
    ledger.document_projection!.presentation_base.generated_baselines = [bare, qualified];
    ledger.generated_baselines = structuredClone(ledger.document_projection!.presentation_base.generated_baselines);

    expect(() => parseMarkdownV4({ review: read("review.md").replace(value, "duplicate alias edit"), history: read("history.md") }, ledger)).toThrowError(expect.objectContaining({ code: "markdown_baseline_missing" }));
  });

  it("rejects an ambiguous legacy bare identity shared by two entity kinds", () => {
    const ledger = structuredClone(parseMachineLedgerV4(read("ledger.json")));
    const decision = ledger.document_projection!.presentation_base.decisions[0];
    const risk = ledger.document_projection!.presentation_base.risks[0];
    risk.id = decision.id;
    const value = decision.title;
    const hash = "a9816e82efb51cbf2bed59687f40939a9c4e5b54aff984778267a777737fc4b9";
    ledger.document_projection!.presentation_base.generated_baselines = [{ generation_id: ledger.generation_id, entity_id: decision.id, field: "title", kind: "scalar", value, generated_hash: hash }];
    ledger.generated_baselines = structuredClone(ledger.document_projection!.presentation_base.generated_baselines);
    const review = read("review.md")
      .replaceAll("risk:risk:alpha", `risk:${decision.id}`)
      .replaceAll("risk-x7269736b3a616c706861", "risk-x6465636973696f6e3a616c706861");

    expect(() => parseMarkdownV4({ review: review.replace(value, "ambiguous edit"), history: read("history.md") }, ledger)).toThrowError(expect.objectContaining({ code: "markdown_baseline_missing" }));
  });

  it("rejects a stored identity with qualified and legacy interpretations", () => {
    const ledger = structuredClone(parseMachineLedgerV4(read("ledger.json")));
    const decision = ledger.document_projection!.presentation_base.decisions[0];
    const risk = ledger.document_projection!.presentation_base.risks[0];
    const priorID = decision.id;
    decision.id = `risk:${risk.id}`;
    for (const milestone of ledger.document_projection!.presentation_base.timeline) {
      milestone.decision_ids = milestone.decision_ids.map((id) => id === priorID ? decision.id : id);
    }
    const generated = "legacy generated title";
    const hash = "6087acf518f8073d448499b374654689804396b7c3f2d64aa438879116a87c47";
    ledger.document_projection!.presentation_base.generated_baselines = [{ generation_id: ledger.generation_id, entity_id: decision.id, field: "title", kind: "scalar", value: generated, generated_hash: hash }];
    ledger.generated_baselines = structuredClone(ledger.document_projection!.presentation_base.generated_baselines);
    const review = read("review.md")
      .replaceAll("decision:decision:alpha", `decision:${decision.id}`)
      .replaceAll("decision-x6465636973696f6e3a616c706861", "decision-x7269736b3a7269736b3a616c706861");
    const history = read("history.md").replaceAll(priorID, decision.id);

    expect(() => parseMarkdownV4({ review: review.replace(decision.title, "ambiguous edit"), history }, ledger)).toThrowError(expect.objectContaining({ code: "markdown_baseline_missing" }));
  });

  it("validates Go baseline hashes containing HTML and line-separator characters", () => {
    const ledger = parseMachineLedgerV4(read("ledger-special-baseline.json"));
    expect(ledger.generated_baselines[0].generated_hash).toBe("112173e9eb315eaffc9ce7b55ca9fc1a768be05486a426a2365c1354301476bd");

    const result = parseMarkdownV4({ review: read("review-special-baseline-draft.md"), history: read("history.md") }, ledger);

    expect(result.presentation.current_state.goal).toBe("再次特殊字符覆盖");
    expect(result.presentation.human_patches[0]).toMatchObject({ value: "再次特殊字符覆盖", base_generated_hash: "112173e9eb315eaffc9ce7b55ca9fc1a768be05486a426a2365c1354301476bd" });
  });

  it.each([
    ["ledger-duplicate-baseline.json", "review.md"],
    ["ledger-invalid-baseline-hash.json", "review.md"],
    ["ledger-baseline-generation-mismatch.json", "review.md"],
    ["ledger-baseline-kind-mismatch.json", "review.md"],
    ["ledger-baseline-value-missing.json", "review.md"],
    ["ledger-baseline-values-present.json", "review.md"],
    ["ledger-duplicate-human-patch.json", "review-existing-patch.md"],
    ["ledger-orphan-patch-collision.json", "review-existing-patch.md"]
  ])("does not reject malformed patch metadata for an untouched field in %s", (ledgerName, reviewName) => {
    const ledger = parseMachineLedgerV4(read(ledgerName));

    const result = parseMarkdownV4({ review: read(reviewName), history: read("history.md") }, ledger);

    expect(result.changedFields).toEqual([]);
  });

  it("renders a deep legal problem chain iteratively within the document bound", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const presentation = structuredClone(ledger.document_projection!.presentation_base);
    const template = presentation.problem_nodes[0];
    presentation.problem_nodes = Array.from({ length: 5_000 }, (_, index) => ({
      ...template,
      id: `deep-${index}`,
      primary_parent_id: index === 0 ? null : `deep-${index - 1}`
    }));

    const rendered = renderProblemTreeV4(presentation);

    expect(rendered.split("\n")).toHaveLength(5_000);
    expect(rendered).toContain("#problem-x646565702d34393939");
  });

  it("rejects a legal-depth problem chain once bounded rendering would exceed 64 MiB", () => {
    const ledger = parseMachineLedgerV4(read("ledger.json"));
    const presentation = structuredClone(ledger.document_projection!.presentation_base);
    const template = presentation.problem_nodes[0];
    presentation.problem_nodes = Array.from({ length: 9_000 }, (_, index) => ({
      ...template,
      id: `oversize-${index}`,
      primary_parent_id: index === 0 ? null : `oversize-${index - 1}`
    }));

    expect(() => renderProblemTreeV4(presentation)).toThrowError(expect.objectContaining({ code: "markdown_format_invalid" }));
  });
});
