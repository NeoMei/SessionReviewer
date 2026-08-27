import type { BrowserModel, EditableFieldName, HistoryEvent } from "../contracts/review-v2";
import type { EditHandler, ViewState } from "./render-shell";
import { button, definition, element } from "./dom";

export function renderEvolution(model: BrowserModel, state: ViewState, update: (patch: Partial<ViewState>) => void, onEdit?: EditHandler): HTMLElement {
  const panel = element("section", { className: "sr-tab-panel", attrs: { role: "tabpanel", "aria-label": "项目演进" } });
  const layout = element("div", { className: "sr-evolution" });
  const rail = element("aside", { className: "sr-timeline-rail" });
  const railHeader = element("div", { className: "sr-rail-header" }, [element("h2", { text: "项目演进" })]);
  const toggle = button(state.fullHistory ? "收起历史" : "查看全部历史", { "data-action": "toggle-history" });
  toggle.addEventListener("click", () => update({ fullHistory: !state.fullHistory }));
  railHeader.append(toggle);
  rail.append(railHeader);
  if (state.fullHistory) {
    const search = element("input", { attrs: { type: "search", placeholder: "搜索日期、类别或标题", value: state.historyQuery, "aria-label": "搜索项目历史" } });
    search.addEventListener("input", () => update({ historyQuery: search.value }));
    rail.append(search);
  }
  const events = visibleEvents(model.events, state);
  const list = element("div", { className: "sr-timeline", attrs: { role: "listbox", "aria-label": "项目演进节点" } });
  for (const event of events) {
    const selected = event.id === state.selectedEventId;
    const node = button("", {
      class: "sr-timeline-node",
      role: "option",
      "aria-selected": String(selected),
      "data-event-id": event.id
    });
    node.className = "sr-timeline-node";
    node.append(element("span", { className: "sr-node-date", text: event.occurredAt }), element("strong", { text: event.title }), element("span", { className: "sr-node-kind", text: event.kind }));
    node.addEventListener("click", () => update({ selectedEventId: event.id }));
    list.append(node);
  }
  if (events.length === 0) list.append(element("p", { className: "sr-empty", text: "没有匹配的历史节点。" }));
  rail.append(list);
  layout.append(rail, renderDetail(model, selectedEvent(model, state), onEdit));
  panel.append(layout);
  return panel;
}

function renderDetail(model: BrowserModel, event: HistoryEvent | undefined, onEdit?: EditHandler): HTMLElement {
  const detail = element("article", { className: "sr-detail", attrs: { "aria-live": "polite" } });
  if (!event) {
    detail.append(element("p", { className: "sr-empty", text: "选择一个演进节点查看详情。" }));
    return detail;
  }
  detail.append(
    element("div", { className: "sr-detail-kicker", text: `${event.occurredAt} · ${event.kind}` }),
    element("h2", { text: event.title, attrs: { "data-role": "detail-title" } })
  );
  const definitions = element("dl", { className: "sr-detail-grid" });
  definitions.append(
    definition("节点意义", event.meaning),
    definition("摘要", event.summary),
    definition("为什么会走到这里", event.why),
    definition("发生了什么", event.changes),
    definition("结果与验证", event.results)
  );
  if (event.decisionIds.length) {
    const linked = event.decisionIds.map((id) => model.review.decisions.find((decision) => decision.id === id)?.title ?? id);
    definitions.append(definition("关联决策", linked));
  }
  definitions.append(definition("留下的问题或下一步", event.next));
  detail.append(definitions);
  if (onEdit) {
    const actions = element("div", { className: "sr-edit-actions" });
    for (const [fieldName, label] of [["event.title", "标题"], ["event.meaning", "意义"], ["event.summary", "摘要"], ["event.why", "前因"], ["event.changes", "变更"], ["event.results", "结果"], ["event.next", "下一步"]] as const) {
      appendEdit(actions, model, event.id, fieldName, label, onEdit);
    }
    detail.append(actions);
  }
  return detail;
}

function appendEdit(parent: HTMLElement, model: BrowserModel, unitId: string, fieldName: EditableFieldName, label: string, onEdit: EditHandler): void {
  const field = model.events.length ? findHistoryField(model, unitId, fieldName) : undefined;
  if (!field) return;
  const action = button(`编辑${label}`, { "data-edit-field": fieldName });
  action.addEventListener("click", () => onEdit(field));
  parent.append(action);
}

function findHistoryField(model: BrowserModel, unitId: string, fieldName: EditableFieldName) {
  return model.historyFields?.find((field) => field.unitId === unitId && field.field === fieldName);
}

function selectedEvent(model: BrowserModel, state: ViewState): HistoryEvent | undefined {
  return model.events.find((event) => event.id === state.selectedEventId) ?? model.events[0];
}

function visibleEvents(events: HistoryEvent[], state: ViewState): HistoryEvent[] {
  if (!state.fullHistory) return events.slice(0, 5);
  const query = state.historyQuery.trim().toLocaleLowerCase();
  if (!query) return events;
  return events.filter((event) => `${event.occurredAt} ${event.kind} ${event.title}`.toLocaleLowerCase().includes(query));
}
