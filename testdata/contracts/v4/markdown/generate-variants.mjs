import { readFile, writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";

const directory = new URL("./", import.meta.url);
const base = JSON.parse(await readFile(new URL("ledger.json", directory), "utf8"));
const write = async (name, value) => writeFile(new URL(name, directory), `${JSON.stringify(value, null, 2)}\n`);
const copy = () => structuredClone(base);

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
const review = await readFile(new URL("review.md", directory), "utf8");
const resignedReview = review.replace("项目目标夹具", resignedGoal);
if (resignedReview === review) throw new Error("review goal fixture was not replaced");
await writeFile(new URL("review-rehashed-public.md", directory), resignedReview);
const resignedReviewHash = createHash("sha256").update(resignedReview).digest("hex");
publiclyResigned.review_sha256 = resignedReviewHash;
publiclyResigned.sync_hashes.review_sha256 = resignedReviewHash;
publiclyResigned.document_projection.presentation_base.current_state.goal = resignedGoal;
publiclyResigned.sync_hashes.ledger_sha256 = "1ff8f204b5dffbffe4a55e9b6557a4e4a9d25cecf581c86a64c2ccb3b5769a84";
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
