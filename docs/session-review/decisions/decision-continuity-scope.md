---
id: decision-continuity-scope
entity_type: decision
project_id: project-269b8cab6cbf69dd
revision: 2
title: 覆盖沉淀、恢复与跨 session 聚合
status: accepted
tags:
  - continuity
  - product-scope
evidence:
  - evidence_id: ev-9f9306df6687
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 62
    source_hash: 92c0e55152ff43a80a09adb55c700734620bf371b6867e3d4eb0c499fea590de
    summary: |
      abc 都需要
source_sessions:
  - 01a02971-61d6-7251-bdcf-f999230f961d
supersedes: []
sync_status: synced
---

# 覆盖沉淀、恢复与跨 session 聚合
## 快速理解

<!-- session-reviewer:generated=v1;owner=navigation;section=快速理解 -->

- **结论：** 覆盖沉淀、恢复与跨 session 聚合
- **状态：** 已接受
- **为什么重要：** 同一底座需要同时支持当前 session 沉淀、重新进入时恢复和跨 session 聚合。

## Context

长 session、多日中断和多个 session 都会让项目脉络难以恢复。

## Alternatives

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "只生成当前 session 摘要"

## Rationale

同一底座需要同时支持当前 session 沉淀、重新进入时恢复和跨 session 聚合。

## Rejected paths

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "仅依赖上下文压缩后的聊天摘要"

## Evidence

- `ev-9f9306df6687` (01a02971-61d6-7251-bdcf-f999230f961d:62): abc 都需要

## Consequences

数据模型必须保存稳定实体、游标、证据与项目级聚合信息。

## Conditions for reevaluation

若只保留单 session 使用场景，可收窄跨 session 索引。
