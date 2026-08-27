import type { BrowserModel, EditableFieldName } from "../contracts/review-v2";
import type { EditHandler, ViewState } from "./render-shell";
import { button, definition, element } from "./dom";
import { presentDateTime, presentStatus } from "./presentation";

export function renderDecisions(model: BrowserModel, update: (patch: Partial<ViewState>) => void, onEdit?: EditHandler): HTMLElement {
  const panel = element("section", { className: "sr-tab-panel", attrs: { role: "tabpanel", "aria-label": "关键决策" } });
  const heading = element("div", { className: "sr-section-heading" });
  heading.append(element("h2", { text: "关键决策" }), element("p", { text: "只保留结论、理由和影响，需要时可跳回当时的演进节点。" }));
  panel.append(heading);
  const cards = element("div", { className: "sr-card-grid" });
  for (const decision of model.review.decisions) {
    const decisionStatus = presentStatus(decision.status);
    const card = element("article", { className: "sr-card" });
    card.append(element("span", { className: "sr-card-meta", text: [presentDateTime(decision.occurredAt), decisionStatus.label].filter(Boolean).join(" · ") }), element("h3", { text: decision.title }));
    const details = element("dl", { className: "sr-detail-grid" });
    details.append(definition("原因", decision.rationale), definition("影响", decision.impact));
    card.append(details);
    if (onEdit) {
      const editActions = element("div", { className: "sr-edit-actions" });
      for (const [fieldName, label] of [["decision.title", "标题"], ["decision.rationale", "原因"], ["decision.impact", "影响"]] as const) {
        appendEdit(editActions, model, decision.id, fieldName, label, onEdit);
      }
      card.append(editActions);
    }
    const event = model.events.find((candidate) => candidate.decisionIds.includes(decision.id));
    if (event) {
      const action = button(`回到演进节点：${event.title}`, { "data-action": "open-related-event", "data-event-id": event.id });
      action.addEventListener("click", () => update({ view: "evolution", selectedEventId: event.id }));
      card.append(action);
    }
    cards.append(card);
  }
  panel.append(cards);
  return panel;
}

function appendEdit(parent: HTMLElement, model: BrowserModel, unitId: string, fieldName: EditableFieldName, label: string, onEdit: EditHandler): void {
  const field = model.review.fields.find((candidate) => candidate.unitId === unitId && candidate.field === fieldName);
  if (!field) return;
  const action = button(`编辑${label}`, { "data-edit-field": fieldName });
  action.addEventListener("click", () => onEdit(field));
  parent.append(action);
}
