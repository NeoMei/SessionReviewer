import type { LedgerAccountingV4, PricingSnapshotV1 } from "../contracts/review-v4";
import { element } from "./dom";
import { presentDateTime } from "./presentation";

export function renderV4Usage(accounting: LedgerAccountingV4, pricing: PricingSnapshotV1[]): HTMLElement {
  const section = element("section", { className: "sr-v4-usage", attrs: { "data-v4-panel": "usage", role: "tabpanel" } }, [
    element("div", { className: "sr-v4-usage-total" }, [metric("总 Token", accounting.total_tokens.toLocaleString("en-US")), metric("总费用", money(accounting.total_cost_usd)), metric("总时长", duration(accounting.total_duration_ms))])
  ]);
  if (accounting.models.length === 0) section.append(element("p", { className: "sr-empty", text: "帐本尚无可用的模型用量记录。" }));
  for (const model of accounting.models) {
    const modelPrices = pricing.filter((item) => item.billed_model_id === model.model && item.status !== "superseded");
    const card = element("article", { className: "sr-v4-model-card" }, [
      element("h3", { text: model.model }), metric("总 Token", model.total_tokens.toLocaleString("en-US")), metric("费用", money(model.total_cost_usd))
    ]);
    if (modelPrices.length === 0) card.append(element("p", { className: "sr-empty", text: "定价待定；不会用 Session 数量推测 Token 或成本。" }));
    for (const price of modelPrices) card.append(renderPrice(price));
    section.append(card);
  }
  section.append(element("p", { className: "sr-v4-pricing-note", text: "公开 API 标价估算；不含订阅额度、账单折扣、税费或企业协议价。" }));
  return section;
}

function renderPrice(price: PricingSnapshotV1): HTMLElement {
  const block = element("div", { className: "sr-v4-pricing" }, [
    element("p", { text: `${price.billing_host} · ${price.status}` }),
    element("p", { text: `计价 ${presentDateTime(price.priced_at)} · 小计 ${money(price.known_subtotal_usd)} · ${price.pricing_complete ? "已完整定价" : "定价不完整"}` })
  ]);
  const dimensions = [
    ["输入", "input"], ["缓存输入", "cached_input"], ["缓存写入", "cache_write_input"], ["输出", "output"], ["推理输出", "reasoning_output"]
  ] as const;
  const usage = element("div", { className: "sr-v4-price-lines" });
  for (const [label, key] of dimensions) {
    usage.append(element("div", { className: "sr-v4-price-line" }, [
      metric(`${label} Token`, price.billable_quantities[key].toLocaleString("en-US")),
      metric("每百万 Token", rate(price.rates[key])),
      metric("估算费用", money(price.line_costs_usd[key]))
    ]));
  }
  block.append(usage);
  for (const [label, href] of [["ModelPriceWatch 数据页", price.detail_url], ["服务商官方价格页", price.source_url]] as const) {
    if (href) block.append(element("a", { text: label, attrs: { href } }));
  }
  return block;
}

function rate(value: number | null): string {
  return value === null ? "待定" : `$${value.toLocaleString("en-US", { maximumFractionDigits: 6 })}`;
}

function metric(label: string, value: string): HTMLElement {
  return element("div", { className: "sr-definition" }, [element("dt", { text: label }), element("dd", { text: value })]);
}

function money(value: number | null): string {
  return value === null ? "待定" : `$${value.toLocaleString("en-US", { maximumFractionDigits: 6 })}`;
}

function duration(value: number): string {
  return `${Math.round(value / 1000).toLocaleString("en-US")} 秒`;
}
