import type { PricingSnapshotV1, PricingSupplementV1 } from "../contracts/review-v4";
import { parsePricingSupplementV1 } from "../data/contracts-v4";
import { button, element } from "./dom";

export function pricingSupplementForm(price: PricingSnapshotV1, save: (input: PricingSupplementV1) => Promise<unknown>): HTMLElement {
  const container = element("div");
  const open = button("补充或纠正价格", { "data-action": "supplement-price" });
  container.append(open);
  open.addEventListener("click", () => {
    if (container.querySelector("form")) return;
    const form = element("form", { className: "sr-v4-pricing-form", attrs: { "aria-label": "确认补充价格" } });
    const inputs = new Map<string, HTMLInputElement>();
    const field = (name: string, label: string, value: string, required = false, type = "text"): void => {
      const input = element("input", { attrs: { name, type, "aria-label": label } });
      input.value = value;
      input.required = required;
      if (type === "number") { input.min = "0"; input.step = "any"; }
      inputs.set(name, input);
      form.append(element("label", { text: label }, [input]));
    };
    form.append(element("p", { text: `为 ${price.provider} / ${price.session_id} 的 ${price.billed_model_id} 确认价格。费用将由已记录的 Token 重新计算，旧价格保留供查阅。` }));
    field("billing_host", "实际计费主机（如 api.openai.com）", price.billing_host === "unknown" ? "" : price.billing_host, true);
    field("billing_mode", "调用模式（如 api）", price.billing_mode === "unknown" ? "" : price.billing_mode, true);
    field("region", "适用区域（无区域限制可留空）", price.region ?? "");
    field("effective_from", "生效时间（含时区）", "", true);
    field("effective_until", "失效时间（可留空）", "");
    for (const [key, label] of [["input", "输入"], ["cached_input", "缓存输入"], ["cache_write_input", "缓存写入"], ["output", "输出"], ["reasoning_output", "推理输出"]] as const) {
      field(key, `${label} 每百万 Token 美元单价（未知留空）`, price.rates[key]?.toString() ?? "", false, "number");
    }
    field("source_url", "价格来源链接", price.source_url ?? "", true, "url");
    field("detail_url", "价格详情链接（可留空）", "", false, "url");
    field("audit_reason", "确认依据或纠错原因", "", true);
    const status = element("p", { attrs: { role: "status" } });
    const confirm = button("确认并保存价格", { type: "submit" });
    const cancel = button("取消", {});
    cancel.addEventListener("click", () => { form.remove(); open.disabled = false; });
    form.append(confirm, cancel, status);
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      if (confirm.disabled || !form.reportValidity()) return;
      const value = (key: string) => inputs.get(key)!.value.trim();
      const rate = (key: string) => value(key) === "" ? null : Number(value(key));
      let supplement: PricingSupplementV1;
      try {
        supplement = parsePricingSupplementV1(JSON.stringify({
          schema_version: 1, minimum_reader_version: "0.4.0", project_id: price.project_id,
          provider: price.provider, session_id: price.session_id, usage_record_digest: price.usage_record_digest,
          billing_host: value("billing_host"), billed_model_id: price.billed_model_id,
          billing_mode: value("billing_mode"), billing_rule_version: price.billing_rule_version,
          region: value("region") || null, effective_from: value("effective_from"), effective_until: value("effective_until") || null,
          rates: { input: rate("input"), cached_input: rate("cached_input"), cache_write_input: rate("cache_write_input"), output: rate("output"), reasoning_output: rate("reasoning_output") },
          source_url: value("source_url"), detail_url: value("detail_url") || null, audit_reason: value("audit_reason"), supersedes_snapshot_id: price.snapshot_id
        }));
      } catch { status.textContent = "请检查路由、日期、价格与来源；未知价格请留空。"; return; }
      confirm.disabled = true; cancel.disabled = true;
      status.textContent = "正在保存价格…";
      void save(supplement).then(() => {
        status.textContent = "价格已保存；正在重新读取账本。";
      }).catch(() => {
        status.textContent = "保存结果未确认；请刷新账本检查，输入已保留。";
        confirm.disabled = false; cancel.disabled = false;
      });
    });
    open.disabled = true;
    container.append(form);
    inputs.get("billing_host")?.focus();
  });
  return container;
}
