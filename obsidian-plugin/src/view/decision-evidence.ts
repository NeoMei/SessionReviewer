import type { DecisionCandidateEvidence, DecisionEvidenceRef } from "../cli/decision-evidence";
import type { ConversationLoader } from "./render-conversation";
import { validateConversationPage } from "../data/conversation-page-validation";
import { button, element } from "./dom";
export type DecisionEvidenceElement = HTMLElement & { dispose: () => void };
export function renderDecisionEvidence(projectId: string, generationId: string, evidence: DecisionCandidateEvidence | undefined, load: ConversationLoader | undefined): DecisionEvidenceElement {
  const root = element("section", { attrs: { "aria-label": "支持候选的事实" } }) as DecisionEvidenceElement;
  let disposed = false;
  root.dispose = () => { disposed = true; root.replaceChildren(); };
  if (!evidence || evidence.error_code || !evidence.evidence_refs.length) {
    root.append(element("p", { text: "候选来源暂不可校验；请刷新或重新提取，确认已停用。" }));
    return root;
  }
  for (const [index, ref] of evidence.evidence_refs.entries()) {
    const card = element("div");
    const control = button(`查看支持事实 ${index + 1} · ${ref.provider} / ${ref.session_id}`, { "data-action": "read-decision-evidence" });
    const body = element("div", { attrs: { role: "status" } });
    control.disabled = !load;
    if (!load) body.textContent = "CLI 不可用，暂不能读取证据。";
    control.addEventListener("click", () => {
      if (control.disabled || disposed || !load) return;
      control.disabled = true; body.textContent = "正在读取对应证据…";
      void readEvidence(projectId, generationId, ref, load, () => disposed).then(message => {
        if (disposed) return;
        body.textContent = `${message.role === "user" ? "用户原文" : "Agent 回答"}：${message.text ?? message.visible_excerpt}${message.text === null ? "\n仅保留认证摘录，完整正文不可用。" : ""}${message.truncated || message.text_truncated ? "\n证据已截断。" : ""}`;
      }).catch(() => { if (!disposed) { body.textContent = "未能读取对应证据；请刷新后重试。"; control.disabled = false; } });
    });
    card.append(control, body); root.append(card);
  }
  return root;
}
async function readEvidence(projectId: string, generationId: string, ref: DecisionEvidenceRef, load: ConversationLoader, disposed: () => boolean) {
  let cursor: string | undefined;
  const seen = new Set<string>();
  let count = 0;
  do {
    if (disposed()) throw new Error("disposed");
    const page = validateConversationPage(await load({ projectId, expectedGenerationId: generationId, provider: ref.provider, sessionId: ref.session_id, expectedSessionViewDigest: ref.session_view_digest, sessionViewDigest: ref.session_view_digest, turnUnitId: ref.turn_unit_id, limit: 20, ...(cursor ? { messageCursor: cursor } : {}) }));
    if (page.project_id !== projectId || page.generation_id !== generationId || page.provider !== ref.provider || page.session_id !== ref.session_id || page.session_view_digest !== ref.session_view_digest || page.turn_unit_id !== ref.turn_unit_id || page.mode !== "turn_messages") throw new Error("evidence response mismatch");
    const matches = page.messages.filter(message => message.revision_id === ref.revision_id);
    if (matches.length > 1) throw new Error("ambiguous evidence");
    if (matches.length === 1) return matches[0];
    cursor = page.next_cursor ?? undefined;
    count += page.messages.length;
    if (cursor && (seen.has(cursor) || count >= page.total || count > 65536)) throw new Error("evidence pagination mismatch");
    if (cursor) seen.add(cursor);
  } while (cursor);
  throw new Error("cited revision is unavailable");
}
