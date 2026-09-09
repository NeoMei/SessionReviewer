import type { ReviewPresentationV4, SessionIndexV1, SourceTurnRefV4, TimelineEntryV4 } from "../contracts/review-v4";
import { button, element } from "./dom";
import { renderConversation, type ConversationElement, type ConversationIdentity, type ConversationLoader } from "./render-conversation";

export type V4AnswerElement = HTMLElement & { dispose: () => void };

type AnswerChoice = {
  ref: SourceTurnRefV4;
  view: string;
  label: string;
};

let answerSequence = 0;

export function renderV4Answer(
  presentation: ReviewPresentationV4,
  milestone: TimelineEntryV4,
  index: SessionIndexV1 | undefined,
  load: ConversationLoader | undefined
): V4AnswerElement {
  const root = element("div", { className: "sr-v4-answer" }) as unknown as V4AnswerElement;
  const bodyId = `sr-v4-answer-body-${++answerSequence}`;
  const body = element("div", { className: "sr-v4-answer-body", attrs: { id: bodyId } });
  const choices = answerChoices(presentation, milestone.closed_loop.conclusion.source_turn_refs);
  let selected = 0;
  let viewer: ConversationElement | undefined;
  let expanded = false;
  let disposed = false;

  const collapse = (): void => {
    viewer?.dispose();
    viewer = undefined;
    expanded = false;
    body.replaceChildren();
  };

  const draw = (): void => {
    if (disposed) return;
    const nodes: Node[] = [];
    if (choices.length > 1) {
      const label = element("label", { className: "sr-v4-answer-source-label", text: "回答来源" });
      const selector = element("select", { attrs: { "aria-label": "选择回答来源" } });
      for (const [choiceIndex, choice] of choices.entries()) {
        const option = element("option", { text: choice.label, attrs: { value: String(choiceIndex) } });
        option.selected = choiceIndex === selected;
        selector.append(option);
      }
      selector.addEventListener("change", () => {
        collapse();
        selected = Number(selector.value);
        draw();
      });
      label.append(selector);
      nodes.push(label);
    } else if (choices[0]) {
      nodes.push(element("p", { className: "sr-v4-answer-source", text: choices[0].label }));
    }

    const reason = unavailableReason(presentation, index, load, choices[selected]);
    const control = button(expanded ? "收起回答正文" : "查看回答正文", {
      "data-action": "expand-milestone-answer",
      "aria-expanded": String(expanded),
      "aria-controls": bodyId
    });
    control.disabled = reason !== undefined;
    const activate = (): void => {
      if (reason !== undefined || disposed) return;
      if (expanded) {
        collapse();
        draw();
        return;
      }
      const choice = choices[selected];
      const requestIdentity: ConversationIdentity = {
        projectId: presentation.project_id,
        provider: choice.ref.provider,
        sessionId: choice.ref.session_id,
        expectedGenerationId: presentation.generation_id,
        expectedSessionViewDigest: choice.view,
        sessionViewDigest: choice.view
      };
      viewer = renderConversation(requestIdentity, load!, { turnUnitId: choice.ref.turn_unit_id });
      expanded = true;
      draw();
    };
    control.addEventListener("click", activate);
    control.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      event.preventDefault();
      activate();
    });
    nodes.push(control);
    if (reason !== undefined) nodes.push(element("p", { className: "sr-v4-answer-unavailable", text: reason }));
    root.replaceChildren(...nodes, body);
    if (viewer) body.replaceChildren(viewer);
  };

  root.dispose = () => {
    if (disposed) return;
    disposed = true;
    collapse();
    root.replaceChildren();
  };
  draw();
  return root;
}

function answerChoices(presentation: ReviewPresentationV4, refs: SourceTurnRefV4[]): AnswerChoice[] {
  const dependencies = new Map<string, ReviewPresentationV4["chain_dependencies"]>();
  for (const dependency of presentation.chain_dependencies) {
    for (const turnUnitId of dependency.turn_unit_ids) {
      const key = sourceKey(dependency.provider, dependency.session_id, turnUnitId);
      const values = dependencies.get(key) ?? [];
      values.push(dependency);
      dependencies.set(key, values);
    }
  }
  const choices: AnswerChoice[] = [];
  const seen = new Set<string>();
  for (const ref of refs) {
    const candidates = (dependencies.get(sourceKey(ref.provider, ref.session_id, ref.turn_unit_id)) ?? [])
      .filter((dependency) => ref.session_view_digest === undefined || dependency.session_view_digest === ref.session_view_digest);
    if (candidates.length !== 1) continue;
    const view = candidates[0].session_view_digest;
    const canonical = `${sourceKey(ref.provider, ref.session_id, ref.turn_unit_id)}\0${view}`;
    if (seen.has(canonical)) continue;
    seen.add(canonical);
    choices.push({ ref, view, label: `${ref.provider} / Session ${ref.session_id} / turn ${ref.turn_unit_id} / view ${view}` });
  }
  return choices;
}

function sourceKey(provider: string, sessionId: string, turnUnitId: string): string {
  return `${provider}\0${sessionId}\0${turnUnitId}`;
}

function unavailableReason(
  presentation: ReviewPresentationV4,
  index: SessionIndexV1 | undefined,
  load: ConversationLoader | undefined,
  choice: AnswerChoice | undefined
): string | undefined {
  if (!choice) return "无法读取回答正文：结论没有唯一、完整绑定的回答来源。";
  if (!load) return "无法读取回答正文：CLI 或问答读取器不可用。";
  if (!index || index.project_id !== presentation.project_id || index.generation_id !== presentation.generation_id ||
    index.project_view_digest !== presentation.project_view_digest) {
    return "无法读取回答正文：Session 索引与当前项目快照不匹配。";
  }
  const sessions = index.sessions.filter((session) => session.provider === choice.ref.provider && session.session_id === choice.ref.session_id);
  if (sessions.length !== 1) return "无法读取回答正文：找不到唯一匹配的 Session 索引记录。";
  return undefined;
}
