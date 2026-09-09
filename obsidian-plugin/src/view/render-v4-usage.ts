import type { LedgerAccountingV4, PricingSnapshotV1, PricingSupplementV1 } from "../contracts/review-v4";
import { pricingSupplementForm } from "./pricing-supplement-form";
import { element } from "./dom";
import { presentDateTime } from "./presentation";

export function renderV4Usage(accounting: LedgerAccountingV4, pricing: PricingSnapshotV1[], actions: { supplement?: (input: PricingSupplementV1) => Promise<unknown> } = {}): HTMLElement {
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
    for (const price of modelPrices) {
      card.append(renderPrice(price));
      if (actions.supplement) card.append(pricingSupplementForm(price, actions.supplement));
    }
    section.append(card);
  }
  section.append(element("p", { className: "sr-v4-pricing-note", text: "公开 API 标价估算；不含订阅额度、账单折扣、税费或企业协议价。" }));
  section.append(element("a", { text: "价格目录由 ModelPriceWatch 提供", attrs: { href: "https://modelpricewatch.com/" } }));
  return section;
}

function renderPrice(price: PricingSnapshotV1): HTMLElement {
  const block = element("div", { className: "sr-v4-pricing" }, [
    element("p", { text: `${price.billing_host === "unknown" ? "计费服务商未知" : price.billing_host} · ${priceStatuses[price.status]}` }),
    element("p", { text: `计价 ${presentDateTime(price.priced_at)} · 小计 ${money(price.known_subtotal_usd)} · ${price.pricing_complete ? "已完整定价" : "定价不完整"}` })
  ]);
  if (price.retrieved_at) block.append(metric("目录查询时间", presentDateTime(price.retrieved_at)));
  if (price.source_last_updated) block.append(metric("价格来源更新", price.source_last_updated));
  if (price.status === "stale_estimate" && price.retrieved_at) {
    const hours = Math.max(0, Math.floor((Date.parse(price.created_at) - Date.parse(price.retrieved_at)) / 3_600_000));
    block.append(metric("计价时缓存年龄", `${hours} 小时`));
  }
  if (price.promo) block.append(metric("促销截止", price.promo_until ?? "尚未明确"));
  if (price.missing_billing_dimensions.length) block.append(element("p", { text: `缺少价格：${price.missing_billing_dimensions.map((key) => dimensionLabels[key] ?? key).join("、")}` }));
  if (price.audit_reason) block.append(element("p", { text: priceReasons[price.audit_reason] ?? price.audit_reason }));
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
  for (const [label, href] of [["ModelPriceWatch 数据页", price.detail_url], [price.source_kind === "manual" ? "人工补充价格来源" : "服务商官方价格页", price.source_url]] as const) {
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

const priceStatuses: Record<PricingSnapshotV1["status"], string> = {
  pending: "价格待定", current: "当前价格", promotion: "促销价格", stale_estimate: "过期缓存估算", manual_supplement: "人工补充", ambiguous: "定价条件存在歧义", legacy_unverified: "历史价格尚未验证", superseded: "已由新快照替代"
};
const dimensionLabels: Record<string, string> = { input: "输入", cached_input: "缓存输入", cache_write_input: "缓存写入", output: "输出", reasoning_output: "推理输出" };
const priceReasons: Record<string, string> = {
  observed_billing_route_missing: "尚未记录实际计费路由；确认服务商与调用模式后才能定价。",
  catalog_unavailable: "价格目录不可用；已保留 Token 用量。",
  catalog_expired: "价格目录已超过有效期；等待刷新或人工补充。",
  usage_adapter_unavailable: "该来源的计费维度尚未确认。",
  no_exact_billing_route_alias: "尚无此计费路由的精确价格映射。",
  conflicting_exact_aliases: "此计费路由对应多个价格条目，需要确认。",
  ambiguous_billing_period: "Session 跨越价格变化，无法按时间拆分用量。",
  unstructured_price_conditions: "价格包含尚未确认的适用条件。"
};
