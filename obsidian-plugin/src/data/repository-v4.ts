import type { MachineLedgerV4, SessionIndexV1 } from "../contracts/review-v4";
import { assertSnapshotBindings, codeOf, parseMachineLedgerV4, parseSessionIndexV1 } from "./contracts-v4";
import { markdownCodeOf, parseMarkdownV4, type MarkdownRead } from "./markdown-v4";
import { sha256Text } from "./hash";

export type MarkdownSnapshot =
  | { kind: "public_valid"; value: MarkdownRead; index: SessionIndexV1; ledger: MachineLedgerV4; ledgerSHA256?: string }
  | { kind: "pending_edit"; value: MarkdownRead; ledger: MachineLedgerV4; ledgerSHA256?: string }
  | { kind: "unverified"; reason: "cli_unavailable" | "baseline_missing" | "private_binding_unavailable" }
  | { kind: "read_failed"; document: MarkdownDocumentPath; category: VaultReadFailureCategory }
  | { kind: "invalid"; code: string };

export type MarkdownDocumentPath = "项目回顾.md" | "项目历史.md" | ".session-reviewer/ledger.json" | ".session-reviewer/session-index.json";

export type VaultReadFailureCategory = "missing" | "permission-denied" | "read-failed";

export function vaultReadFailure(document: MarkdownDocumentPath, error: unknown): Extract<MarkdownSnapshot, { kind: "read_failed" }> {
  const code = safeErrorCode(error);
  const category = code === "ENOENT" ? "missing" : code === "EACCES" || code === "EPERM" ? "permission-denied" : "read-failed";
  return { kind: "read_failed", document, category };
}

function safeErrorCode(error: unknown): unknown {
  try {
    return typeof error === "object" && error !== null && "code" in error ? (error as { code?: unknown }).code : undefined;
  } catch {
    return undefined;
  }
}

export function loadMarkdownSnapshot(input: { review: string; history: string; ledger: string; index: string }): MarkdownSnapshot {
  let ledger: MachineLedgerV4;
  let index: SessionIndexV1;
  try {
    ledger = parseMachineLedgerV4(input.ledger);
    if (!ledger.document_projection) return { kind: "unverified", reason: "baseline_missing" };
    index = parseSessionIndexV1(input.index);
    assertSnapshotBindings(ledger, index);
  } catch (error) {
    return { kind: "invalid", code: codeOf(error) ?? "wire_contract_invalid" };
  }
  try {
    const value = parseMarkdownV4({ review: input.review, history: input.history }, ledger);
    if (value.changedDocuments.length > 0 || value.changedFields.length > 0) return { kind: "pending_edit", value, ledger, ledgerSHA256: sha256Text(input.ledger) };
    if (ledger.review_sha256 !== sha256Text(input.review) || ledger.history_sha256 !== sha256Text(input.history)) return { kind: "pending_edit", value, ledger, ledgerSHA256: sha256Text(input.ledger) };
    return { kind: "public_valid", value, index, ledger, ledgerSHA256: sha256Text(input.ledger) };
  } catch (error) {
    return { kind: "invalid", code: markdownCodeOf(error) ?? "markdown_format_invalid" };
  }
}
