import type { DecisionStatus, ReviewPresentationV4 } from "../contracts/review-v4";
import { button, element } from "./dom";
import { presentDateTime } from "./presentation";

export function renderV4Decisions(presentation: ReviewPresentationV4, openReview: () => void): HTMLElement {
  const section = element("section", { className: "sr-v4-decisions", attrs: { "data-v4-panel": "decisions", role: "tabpanel" } });
  const toolbar = element("div", { className: "sr-v4-filter" });
  const active = button("生效中", { "aria-pressed": "true" });
  const historic = button("已替代 / 已归档", { "aria-pressed": "false" });
  toolbar.append(active, historic);
  const cards = element("div", { className: "sr-card-grid" });
  const draw = (statuses: DecisionStatus[]): void => {
    cards.replaceChildren();
    const values = presentation.decisions.filter((decision) => statuses.includes(decision.status));
    if (values.length === 0) {
      cards.append(element("p", { className: "sr-empty", text: statuses.includes("active")
        ? "尚无已确认的决策与约定。扫描已经保存项目事实，但不会替你判断项目意图。"
        : "尚无已替代或已归档的决策与约定。" }));
      return;
    }
    for (const decision of values) {
      cards.append(element("article", { className: "sr-card" }, [
        element("span", { className: "sr-card-meta", text: `${decision.kind === "agreement" ? "约定" : "决策"} · ${presentDateTime(decision.occurred_at)}` }),
        element("h3", { text: decision.title }),
        field("理由", decision.rationale), field("影响范围", decision.impact), field("重新评估条件", decision.reevaluate_when)
      ]));
    }
  };
  active.addEventListener("click", () => { active.setAttribute("aria-pressed", "true"); historic.setAttribute("aria-pressed", "false"); draw(["active"]); });
  historic.addEventListener("click", () => { active.setAttribute("aria-pressed", "false"); historic.setAttribute("aria-pressed", "true"); draw(["superseded", "archived", "legacy_unmapped"]); });
  const native = button("在原生 Markdown 中新增或编辑", { "data-v4-open": "review" });
  native.addEventListener("click", openReview);
  section.append(toolbar, cards, native);
  draw(["active"]);
  return section;
}

function field(label: string, value: string): HTMLElement {
  return element("div", { className: "sr-definition" }, [element("strong", { text: label }), element("p", { text: value.trim() || "未填写" })]);
}
