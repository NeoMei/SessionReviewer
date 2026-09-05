import { readFile, writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";

const directory = new URL("./", import.meta.url);
const base = JSON.parse(await readFile(new URL("ledger.json", directory), "utf8"));
const write = async (name, value) => writeFile(new URL(name, directory), `${JSON.stringify(value, null, 2)}\n`);
const copy = () => structuredClone(base);

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
  return createHash("sha256").update(JSON.stringify(body)).digest("hex");
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
