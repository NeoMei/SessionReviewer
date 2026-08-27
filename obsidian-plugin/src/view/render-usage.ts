import type { BrowserModel } from "../contracts/review-v2";
import { definition, element } from "./dom";

export function renderUsage(model: BrowserModel): HTMLElement {
  const panel = element("section", { className: "sr-tab-panel", attrs: { role: "tabpanel", "aria-label": "使用与成本" } });
  const heading = element("div", { className: "sr-section-heading" });
  heading.append(element("h2", { text: "使用与成本" }), element("p", { text: "费用来自已验收的机器账本，人类文档不能修改这些数字。" }));
  panel.append(heading);
  const total = element("dl", { className: "sr-usage-total", attrs: { "data-role": "usage-total" } });
  total.append(
    definition("总时长", formatDuration(model.accounting.totalDurationMs)),
    definition("总 tokens", model.accounting.totalTokens.toLocaleString()),
    definition("总成本", formatUsd(model.accounting.totalCostUsd)),
    definition("模型数", String(model.accounting.models.length))
  );
  panel.append(total);
  const cards = element("div", { className: "sr-card-grid" });
  for (const summary of model.accounting.models) {
    const card = element("article", { className: "sr-card" });
    card.append(element("h3", { text: summary.model }));
    const details = element("dl", { className: "sr-detail-grid" });
    details.append(
      definition("Tokens", summary.totalTokens.toLocaleString()),
      definition("费用", formatUsd(summary.totalCostUsd)),
      definition("Token 占比", `${formatNumber(summary.tokenSharePct)}%`),
      definition("费用占比", `${formatNumber(summary.costSharePct)}%`)
    );
    const priceRows = model.sessions.flatMap((session) => session.accounting?.models ?? []).filter((entry) => entry.model === summary.model);
    const latest = priceRows.at(-1);
    if (latest) {
      details.append(
        definition("输入价格", `$${formatNumber(latest.pricing.inputPerMillion)} / 百万 tokens`),
        definition("缓存输入", `$${formatNumber(latest.pricing.cachedInputPerMillion)} / 百万 tokens`),
        definition("输出价格", `$${formatNumber(latest.pricing.outputPerMillion)} / 百万 tokens`),
        definition("价格日期", latest.pricing.asOf)
      );
      const source = element("a", { text: latest.pricing.source, attrs: { href: latest.pricing.source } });
      const sourceRow = element("div", { className: "sr-definition" });
      sourceRow.append(element("dt", { text: "价格来源" }), element("dd", {}, [source]));
      details.append(sourceRow);
    }
    card.append(details);
    cards.append(card);
  }
  panel.append(cards);
  return panel;
}

function formatDuration(milliseconds: number): string {
  const seconds = Math.round(milliseconds / 1000);
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return minutes ? `${minutes} 分 ${rest} 秒` : `${rest} 秒`;
}

function formatUsd(value: number): string {
  return `$${value.toFixed(Math.max(2, value < 0.01 ? 4 : 2))}`;
}

function formatNumber(value: number): string {
  return Number.isInteger(value) ? String(value) : value.toFixed(2).replace(/0+$/, "").replace(/\.$/, "");
}
