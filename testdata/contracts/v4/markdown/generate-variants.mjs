import { readFile, writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";

const directory = new URL("./", import.meta.url);
const base = JSON.parse(await readFile(new URL("ledger.json", directory), "utf8"));
const write = async (name, value) => writeFile(new URL(name, directory), `${JSON.stringify(value, null, 2)}\n`);
const copy = () => structuredClone(base);
const hash = (value) => createHash("sha256").update(value).digest("hex");
const goJSON = (value) => JSON.stringify(value).replace(/[<>&\u2028\u2029]/g, (character) => ({
  "<": "\\u003c", ">": "\\u003e", "&": "\\u0026", "\u2028": "\\u2028", "\u2029": "\\u2029"
})[character]);
const baselineHash = (entity_id, field, kind, value, values = null) =>
  hash(goJSON({ schema_version: 1, entity_id, field, kind, value, values }));

const canonicalLedgerHash = (ledger) => {
  const body = {
    schema_version: ledger.schema_version,
    minimum_reader_version: ledger.minimum_reader_version,
    minimum_writer_version: ledger.minimum_writer_version,
    project_id: ledger.project_id,
    generation_id: ledger.generation_id,
    project_view_digest: ledger.project_view_digest,
    accepted_revision: ledger.accepted_revision,
    review_sha256: ledger.review_sha256,
    history_sha256: ledger.history_sha256,
    accounting: ledger.accounting,
    sessions: ledger.sessions,
    human_patches: ledger.human_patches,
    orphan_patches: ledger.orphan_patches,
    generated_baselines: ledger.generated_baselines,
    pricing_snapshots: ledger.pricing_snapshots,
    current_pricing_snapshot_ids: ledger.current_pricing_snapshot_ids,
    document_projection: ledger.document_projection,
    sync_hashes: {
      review_sha256: ledger.sync_hashes.review_sha256,
      history_sha256: ledger.sync_hashes.history_sha256,
      session_index_digest: ledger.sync_hashes.session_index_digest
    }
  };
  return createHash("sha256").update(goJSON(body)).digest("hex");
};

const review = await readFile(new URL("review.md", directory), "utf8");
const history = await readFile(new URL("history.md", directory), "utf8");
base.review_sha256 = createHash("sha256").update(review).digest("hex");
base.history_sha256 = createHash("sha256").update(history).digest("hex");
base.sync_hashes.review_sha256 = base.review_sha256;
base.sync_hashes.history_sha256 = base.history_sha256;
base.sync_hashes.ledger_sha256 = canonicalLedgerHash(base);

const reorder = (value) => {
  if (Array.isArray(value)) return value.map(reorder);
  if (value === null || typeof value !== "object") return value;
  return Object.fromEntries(Object.entries(value).reverse().map(([key, child]) => [key, reorder(child)]));
};

const reordered = copy();
reordered.document_projection.presentation_base = reorder(reordered.document_projection.presentation_base);
await write("ledger-reordered.json", reordered);

const tampered = copy();
tampered.document_projection.presentation_base.current_state.goal = "已篡改但未重签";
await write("ledger-tampered-self-digest.json", tampered);

const publiclyResigned = copy();
const resignedGoal = "公开自摘要已重签，私有认证仍应拒绝";
const containerText = [
  "- <!-- session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->",
  "> <!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->"
].join("\n");
const containerReview = review.replace(
  "这段自定义文本和 [链接](https://example.test/custom) 必须原样保留。",
  `这段自定义文本和 [链接](https://example.test/custom) 必须原样保留。\n\n${containerText}`
);
if (containerReview === review) throw new Error("review container fixture insertion failed");
await writeFile(new URL("review-container-markers.md", directory), containerReview);
const containerReviewHash = createHash("sha256").update(containerReview).digest("hex");
const containerLedger = copy();
containerLedger.review_sha256 = containerReviewHash;
containerLedger.sync_hashes.review_sha256 = containerReviewHash;
containerLedger.sync_hashes.ledger_sha256 = canonicalLedgerHash(containerLedger);
await write("ledger-container-markers.json", containerLedger);

const resignedReview = review.replace("项目目标夹具", resignedGoal);
if (resignedReview === review) throw new Error("review goal fixture was not replaced");
await writeFile(new URL("review-rehashed-public.md", directory), resignedReview);
const resignedReviewHash = createHash("sha256").update(resignedReview).digest("hex");
publiclyResigned.review_sha256 = resignedReviewHash;
publiclyResigned.sync_hashes.review_sha256 = resignedReviewHash;
publiclyResigned.document_projection.presentation_base.current_state.goal = resignedGoal;
publiclyResigned.sync_hashes.ledger_sha256 = canonicalLedgerHash(publiclyResigned);
await write("ledger-rehashed-public.json", publiclyResigned);

const wrongVersion = copy();
wrongVersion.minimum_reader_version = "0.4.0";
await write("ledger-wrong-version.json", wrongVersion);

const revisionMismatch = copy();
revisionMismatch.document_projection.presentation_base.revision += 1;
await write("ledger-revision-mismatch.json", revisionMismatch);

const nullProjection = copy();
nullProjection.document_projection = null;
await write("ledger-null-projection.json", nullProjection);

const unknownKey = copy();
unknownKey.document_projection.unknown = true;
await write("ledger-unknown-key.json", unknownKey);

const patchMismatch = copy();
patchMismatch.document_projection.presentation_base.human_patches = [{
  entity_id: "decision:decision:alpha",
  field: "title",
  operation: "set",
  value: "仅内层有 patch",
  base_generated_hash: "1111111111111111111111111111111111111111111111111111111111111111"
}];
await write("ledger-patch-mismatch.json", patchMismatch);

const goalEntity = "project-overview";
const goalField = "goal";
const originalGoal = "项目目标夹具";
const acceptedGoal = "已有人工目标";
const draftGoal = "再次人工编辑目标";
const goalBaseline = {
  generation_id: base.generation_id,
  entity_id: goalEntity,
  field: goalField,
  kind: "scalar",
  value: originalGoal,
  generated_hash: baselineHash(goalEntity, goalField, "scalar", originalGoal)
};
const goalPatch = {
  entity_id: goalEntity,
  field: goalField,
  operation: "set",
  value: acceptedGoal,
  base_generated_hash: goalBaseline.generated_hash
};
const setPatchState = (ledger, human, orphan, baselines) => {
  ledger.human_patches = structuredClone(human);
  ledger.orphan_patches = structuredClone(orphan);
  ledger.generated_baselines = structuredClone(baselines);
  const presentation = ledger.document_projection.presentation_base;
  presentation.human_patches = structuredClone(human);
  presentation.orphan_patches = structuredClone(orphan);
  presentation.generated_baselines = structuredClone(baselines);
};
const acceptedPatchReview = review.replace(originalGoal, acceptedGoal);
const draftPatchReview = review.replace(originalGoal, draftGoal);
await writeFile(new URL("review-existing-patch.md", directory), acceptedPatchReview);
await writeFile(new URL("review-existing-patch-draft.md", directory), draftPatchReview);
await writeFile(new URL("review-goal-edit.md", directory), draftPatchReview);

const existingPatch = copy();
existingPatch.document_projection.presentation_base.current_state.goal = acceptedGoal;
setPatchState(existingPatch, [goalPatch], [], [goalBaseline]);
existingPatch.review_sha256 = hash(acceptedPatchReview);
existingPatch.sync_hashes.review_sha256 = existingPatch.review_sha256;
existingPatch.sync_hashes.ledger_sha256 = canonicalLedgerHash(existingPatch);
await write("ledger-existing-patch.json", existingPatch);

const specialBaselineValue = "<目标>&\u2028\u2029";
const specialAcceptedGoal = "已有特殊字符覆盖";
const specialDraftGoal = "再次特殊字符覆盖";
const specialBaseline = {
  generation_id: base.generation_id,
  entity_id: goalEntity,
  field: goalField,
  kind: "scalar",
  value: specialBaselineValue,
  generated_hash: baselineHash(goalEntity, goalField, "scalar", specialBaselineValue)
};
const specialPatch = {
  entity_id: goalEntity,
  field: goalField,
  operation: "set",
  value: specialAcceptedGoal,
  base_generated_hash: specialBaseline.generated_hash
};
const specialAcceptedReview = review.replace(originalGoal, specialAcceptedGoal);
const specialDraftReview = review.replace(originalGoal, specialDraftGoal);
await writeFile(new URL("review-special-baseline.md", directory), specialAcceptedReview);
await writeFile(new URL("review-special-baseline-draft.md", directory), specialDraftReview);
const specialBaselineLedger = copy();
specialBaselineLedger.document_projection.presentation_base.current_state.goal = specialAcceptedGoal;
setPatchState(specialBaselineLedger, [specialPatch], [], [specialBaseline]);
specialBaselineLedger.review_sha256 = hash(specialAcceptedReview);
specialBaselineLedger.sync_hashes.review_sha256 = specialBaselineLedger.review_sha256;
specialBaselineLedger.sync_hashes.ledger_sha256 = canonicalLedgerHash(specialBaselineLedger);
await write("ledger-special-baseline.json", specialBaselineLedger);

const duplicateBaseline = copy();
setPatchState(duplicateBaseline, [], [], [goalBaseline, goalBaseline]);
duplicateBaseline.sync_hashes.ledger_sha256 = canonicalLedgerHash(duplicateBaseline);
await write("ledger-duplicate-baseline.json", duplicateBaseline);

const invalidBaselineHash = copy();
setPatchState(invalidBaselineHash, [], [], [{ ...goalBaseline, generated_hash: "0".repeat(64) }]);
invalidBaselineHash.sync_hashes.ledger_sha256 = canonicalLedgerHash(invalidBaselineHash);
await write("ledger-invalid-baseline-hash.json", invalidBaselineHash);

const baselineVariants = [
  ["ledger-baseline-generation-mismatch.json", { ...goalBaseline, generation_id: "other-generation" }],
  ["ledger-baseline-kind-mismatch.json", { ...goalBaseline, kind: "list", generated_hash: baselineHash(goalEntity, goalField, "list", originalGoal) }],
  ["ledger-baseline-value-missing.json", (({ value: _value, ...baseline }) => baseline)(goalBaseline)],
  ["ledger-baseline-values-present.json", {
    generation_id: goalBaseline.generation_id,
    entity_id: goalBaseline.entity_id,
    field: goalBaseline.field,
    kind: goalBaseline.kind,
    value: goalBaseline.value,
    values: [],
    generated_hash: goalBaseline.generated_hash
  }]
];
for (const [name, baseline] of baselineVariants) {
  const ledger = copy();
  setPatchState(ledger, [], [], [baseline]);
  ledger.sync_hashes.ledger_sha256 = canonicalLedgerHash(ledger);
  await write(name, ledger);
}

const duplicatePatch = copy();
duplicatePatch.document_projection.presentation_base.current_state.goal = acceptedGoal;
setPatchState(duplicatePatch, [goalPatch, goalPatch], [], [goalBaseline]);
duplicatePatch.review_sha256 = hash(acceptedPatchReview);
duplicatePatch.sync_hashes.review_sha256 = duplicatePatch.review_sha256;
duplicatePatch.sync_hashes.ledger_sha256 = canonicalLedgerHash(duplicatePatch);
await write("ledger-duplicate-human-patch.json", duplicatePatch);

const orphanCollision = copy();
orphanCollision.document_projection.presentation_base.current_state.goal = acceptedGoal;
setPatchState(orphanCollision, [goalPatch], [goalPatch], [goalBaseline]);
orphanCollision.review_sha256 = hash(acceptedPatchReview);
orphanCollision.sync_hashes.review_sha256 = orphanCollision.review_sha256;
orphanCollision.sync_hashes.ledger_sha256 = canonicalLedgerHash(orphanCollision);
await write("ledger-orphan-patch-collision.json", orphanCollision);

const oversizedID = "a".repeat(257);
await writeFile(new URL("review-oversized-marker-id.md", directory), review.replaceAll("decision:decision:alpha", `decision:${oversizedID}`));
await writeFile(new URL("review-project-overview-colon.md", directory), review.replaceAll('entity="project-overview" name="goal"', 'entity="project-overview:" name="goal"'));
