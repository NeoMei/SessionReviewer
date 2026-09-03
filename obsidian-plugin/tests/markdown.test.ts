import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { sha256Text } from "../src/data/hash";
import { parseHistory, parseReview } from "../src/data/markdown";

const fixture = (name: string): Promise<string> =>
  readFile(resolve(dirname(fileURLToPath(import.meta.url)), "../../testdata/review-v3", name), "utf8");

describe("review v2 human Markdown contract", () => {
  it("accepts shared Go fixtures and ignores marker-looking text in fences", async () => {
    const reviewSource = await fixture("项目回顾.valid.md");
    const historySource = await fixture("项目历史.valid.md");
    const review = parseReview(reviewSource);
    const history = parseHistory(historySource);

    expect(review.projectId).toBe(history.projectId);
    expect(review.name).toBe("SessionReviewer v2");
    expect(review.decisions.map((decision) => decision.id)).toEqual(["decision-local-cli"]);
    expect(history.events.map((event) => event.id)).toEqual(["timeline-trust-chain"]);
    expect(history.events[0]?.decisionIds).toEqual(["decision-local-cli"]);
    expect(review.fields.some((field) => field.field === "next_action")).toBe(true);
    expect(history.fields.some((field) => field.field === "event.next")).toBe(true);
    for (const field of [...review.fields, ...history.fields]) {
      expect(field.range.start).toBeLessThanOrEqual(field.range.end);
      expect(field.range.end).toBeLessThanOrEqual(field.document === "review" ? reviewSource.length : historySource.length);
    }
  });

  it("rejects duplicate event identities and malformed markers", async () => {
    const duplicate = await fixture("项目历史.invalid-duplicate-event.md");
    expect(() => parseHistory(duplicate)).toThrow(/duplicate event identity/);
    const valid = await fixture("项目历史.valid.md");
    expect(() => parseHistory(valid.replace("<!-- /session-reviewer:event -->", ""))).toThrow(/unterminated event marker/);
  });

  it("accepts empty top-level fields emitted by a zero-token scan", async () => {
    const source = (await fixture("项目回顾.valid.md"))
      .replace("让长会话的决策、当前态和演进历史可编辑、可验证地延续。", "")
      .replace("v2 核心迁移", "")
      .replace("正在实现无损 Markdown codec。", "")
      .replace("在真实 Vault 中验证双向同步。", "")
      .replace("`go test ./...` 与真实 dry-run 收敛。", "");

    const review = parseReview(source);

    expect({
      goal: review.goal,
      stage: review.stage,
      status: review.status,
      nextAction: review.nextAction,
      lastVerification: review.lastVerification
    }).toEqual({ goal: "", stage: "", status: "", nextAction: "", lastVerification: "" });
  });

  it("hashes exact UTF-8 text synchronously", () => {
    expect(sha256Text("项目回顾\n")).toBe("6c3800f9d5b7bed0a895ff33b1e812cbb7d959a9011f5f0191055a9913682e22");
  });
});
