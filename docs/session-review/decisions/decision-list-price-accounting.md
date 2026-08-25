---
id: decision-list-price-accounting
entity_type: decision
project_id: project-269b8cab6cbf69dd
revision: 2
title: 记录 session 与项目级模型用量和公开标价成本
status: accepted
tags:
  - accounting
  - cost
  - models
evidence:
  - evidence_id: ev-7fb129b0617e
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 14715
    source_hash: 5ff49be0080b4c2298435c60f52f16c6c280f0458948fa4239dbbde5f54e6af5
    summary: |
      另外需要在session 记录里面加上，session 耗时，token 消耗，成本（如果多模型切换，可以统计每个模型的消耗和成本，以及总成本，token 成本一律用 每百万 token 标价计算，不用考虑订阅情况），最终项目级记录里面也要有总耗时，token 消耗，和总成本。以及不同模型使用占比。
  - evidence_id: ev-bc76f08521d0
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 14721
    source_hash: 76209158df8e9a687f802417bea280e65cac85a349f38566433b3351b013ed80
    summary: |-
      收到，这项会并入“全部任务完成”的验收范围，不只补文档：每个 session 记录耗时、输入/输出/缓存等 token、按模型和每百万 token 单价计算的成本；项目级汇总总耗时、总 token、总成本及模型使用占比。订阅状态不参与成本计算。

      这是新增数据模型与计费口径，我先按 brainstorming skill 把字段、来源、精度和缺失数据策略收敛，再把它落入 ledger/session、project overview/聚合、CLI/测试与最终文档；Task 6 的原子恢复修复继续并行，不受影响。
source_sessions:
  - 01a02971-61d6-7251-bdcf-f999230f961d
supersedes: []
sync_status: synced
---

# 记录 session 与项目级模型用量和公开标价成本
## 快速理解

<!-- session-reviewer:generated=v1;owner=navigation;section=快速理解 -->

- **结论：** 记录 session 与项目级模型用量和公开标价成本
- **状态：** 已接受
- **为什么重要：** 每个 session 固化耗时、token 分类、每模型公开每百万 token 单价与成本；项目级累加并展示模型占比，不考虑订阅。

## Context

项目需要量化长 session 的时间和模型消耗，并支持多模型汇总。

## Alternatives

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "只统计总 token"
- sr-string: "按订阅费用估算成本"
- sr-string: "查看时按最新价格覆盖历史"

## Rationale

每个 session 固化耗时、token 分类、每模型公开每百万 token 单价与成本；项目级累加并展示模型占比，不考虑订阅。

## Rejected paths

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "忽略缓存 token 和多模型占比"
- sr-string: "用订阅价格代替公开 API 单价"

## Evidence

- `ev-7fb129b0617e` (01a02971-61d6-7251-bdcf-f999230f961d:14715): 另外需要在session 记录里面加上，session 耗时，token 消耗，成本（如果多模型切换，可以统计每个模型的消耗和成本，以及总成本，token 成本一律用 每百万 token 标价计算，不用考虑订阅情况），最终项目级记录里面也要有总耗时，token 消耗，和总成本。以及不同模型使用占比。
- `ev-bc76f08521d0` (01a02971-61d6-7251-bdcf-f999230f961d:14721): 收到，这项会并入“全部任务完成”的验收范围，不只补文档：每个 session 记录耗时、输入/输出/缓存等 token、按模型和每百万 token 单价计算的成本；项目级汇总总耗时、总 token、总成本及模型使用占比。订阅状态不参与成本计算。  这是新增数据模型与计费口径，我先按 brainstorming skill 把字段、来源、精度和缺失数据策略收敛，再把它落入 ledger/session、project overview/聚合、CLI/测试与最终文档；Task 6 的原子恢复修复继续并行，不受影响。

## Consequences

evidence packet、proposal、session report、project overview 与 history 都必须携带并验证 accounting。

## Conditions for reevaluation

模型定价来源或 token 事件格式变化时更新版本化价格与提取规则。
