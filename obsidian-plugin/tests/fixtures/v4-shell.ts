import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type { MachineLedgerV4, ReviewPresentationV4, SessionIndexV1 } from "../../src/contracts/review-v4";
import type { Snapshot } from "../../src/data/repository";
import { parseMachineLedgerV4, parseReviewPresentationV4, parseSessionIndexV1 } from "../../src/data/contracts-v4";

const here = dirname(fileURLToPath(import.meta.url));
const contractRoot = resolve(here, "../../../testdata/contracts/v4");
const markdownRoot = resolve(contractRoot, "markdown");

function json(path: string): Record<string, unknown> {
  return JSON.parse(readFileSync(path, "utf8")) as Record<string, unknown>;
}

export function v4PresentationFixture(projectId = "project-p"): ReviewPresentationV4 {
  const ledger = json(resolve(markdownRoot, "ledger.json"));
  const projection = ledger.document_projection as { presentation_base: Record<string, unknown> };
  const presentation = structuredClone(projection.presentation_base);
  presentation.project_id = projectId;
  return parseReviewPresentationV4(JSON.stringify(presentation));
}

export function v4LedgerFixture(projectId = "project-p"): MachineLedgerV4 {
  const ledger = json(resolve(contractRoot, "machine-ledger-v4.valid.json"));
  ledger.project_id = projectId;
  ledger.accounting = {
    total_duration_ms: 60_000,
    total_tokens: 15,
    total_cost_usd: null,
    models: [{ model: "model-1", total_tokens: 15, total_cost_usd: null }]
  };
  ledger.sessions = [];
  for (const snapshot of ledger.pricing_snapshots as Array<Record<string, unknown>>) snapshot.project_id = projectId;
  return parseMachineLedgerV4(JSON.stringify(ledger));
}

export function v4IndexFixture(projectId = "project-p"): SessionIndexV1 {
  const index = json(resolve(contractRoot, "session-index-v1.valid.json"));
  index.project_id = projectId;
  return parseSessionIndexV1(JSON.stringify(index));
}

export function v4SnapshotFixture(projectId = "project-p", name = "SessionReviewer"): Extract<Snapshot, { kind: "markdown-v4" }> {
  return {
    kind: "markdown-v4",
    descriptor: { projectId, root: `Projects/${name}`, name, format: "markdown-v4" },
    state: {
      kind: "public_valid",
      value: { presentation: v4PresentationFixture(projectId), changedFields: [], changedDocuments: [], fields: [] },
      index: v4IndexFixture(projectId),
      ledger: v4LedgerFixture(projectId)
    },
    loadedAt: 1
  };
}
