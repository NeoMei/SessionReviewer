import type { BrowserModel } from "../contracts/review-v3";
import { definition, element } from "./dom";

export function renderUsage(model: BrowserModel): HTMLElement {
  const panel = element("section", { className: "sr-tab-panel", attrs: { role: "tabpanel", "aria-label": "使用与成本" } });
  const heading = element("div", { className: "sr-section-heading" });
  heading.append(element("h2", { text: "使用与成本" }), element("p", { text: "费用来自已验收的机器账本，人类文档不能修改这些数字。" }));
  panel.append(heading);
  const total = element("dl", { className: "sr-usage-total", attrs: { "data-role": "usage-total" } });
  total.append(
    definition("总时长", formatDuration(model.accounting.totalDurationMs)),
    definition("总 tokens", formatTokenCount(model.accounting.totalTokens)),
    definition("总成本", model.accounting.pricingComplete ? formatUsd(model.accounting.totalCostUsd) : "费用暂不可用"),
    definition("模型数", String(model.accounting.models.length))
  );
  panel.append(total);
  const cards = element("div", { className: "sr-card-grid sr-model-grid" });
  for (const summary of model.accounting.models) {
    const card = element("article", { className: "sr-card sr-model-card" });
    const header = element("div", { className: "sr-model-header" });
    header.append(element("h3", { text: summary.model }), element("span", { className: "sr-model-kicker", text: "模型用量与定价" }));
    const priceRows = model.sessions.flatMap((session) => session.accounting?.models ?? []).filter((entry) => entry.model === summary.model);
    const pricingComplete = priceRows.every((entry) => entry.pricing !== undefined);
    const metrics = element("dl", { className: "sr-model-metrics" });
    metrics.append(
      definition("Tokens", formatTokenCount(summary.totalTokens)),
      definition("费用", pricingComplete ? formatUsd(summary.totalCostUsd) : "费用暂不可用"),
      definition("Token 占比", `${formatNumber(summary.tokenSharePct)}%`),
      definition("费用占比", pricingComplete ? `${formatNumber(summary.costSharePct)}%` : "费用暂不可用")
    );
    card.append(header, metrics);
    const latest = [...priceRows].reverse().find((entry) => entry.pricing !== undefined);
    if (latest?.pricing) {
      const pricing = element("dl", { className: "sr-model-pricing" });
      pricing.append(
        definition("输入价格", `$${formatNumber(latest.pricing.inputPerMillion)} / 百万 tokens`),
        definition("缓存输入", `$${formatNumber(latest.pricing.cachedInputPerMillion)} / 百万 tokens`),
        definition("输出价格", `$${formatNumber(latest.pricing.outputPerMillion)} / 百万 tokens`)
      );
      const meta = element("div", { className: "sr-model-meta" });
      const source = element("span", { className: "sr-model-source-row" });
      source.append(
        element("span", { className: "sr-model-source-label", text: "定价来源" }),
        element("a", { className: "sr-model-source", text: latest.pricing.source, attrs: { href: latest.pricing.source } })
      );
      meta.append(
        element("span", { text: `价格日期 ${latest.pricing.asOf}` }),
        source
      );
      card.append(pricing, meta);
    }
    cards.append(card);
  }
  panel.append(cards);
  return panel;
}

function formatDuration(milliseconds: number): string {
  let remaining = Math.round(milliseconds / 1000);
  const units = [
    { label: "月", seconds: 30 * 24 * 60 * 60 },
    { label: "天", seconds: 24 * 60 * 60 },
    { label: "时", seconds: 60 * 60 },
    { label: "分", seconds: 60 },
    { label: "秒", seconds: 1 }
  ];
  const parts: string[] = [];
  for (const unit of units) {
    const value = Math.floor(remaining / unit.seconds);
    remaining %= unit.seconds;
    if (value > 0) parts.push(`${value} ${unit.label}`);
  }
  return parts.join(" ") || "0 秒";
}

function formatUsd(value: number): string {
  return `$${value.toFixed(Math.max(2, value < 0.01 ? 4 : 2))}`;
}

function formatTokenCount(value: number): string {
  const exact = value.toLocaleString();
  return value >= 100_000_000 ? `${formatNumber(value / 100_000_000)} 亿（${exact}）` : exact;
}

function formatNumber(value: number): string {
  return Number.isInteger(value) ? String(value) : value.toFixed(2).replace(/0+$/, "").replace(/\.$/, "");
}
