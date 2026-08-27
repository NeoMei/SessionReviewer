---
id: decision-human-readable-navigation
entity_type: decision
project_id: project-269b8cab6cbf69dd
revision: 1
title: 采用导航型首页与 A1 Mermaid 项目演进主线
status: accepted
tags:
  - navigation
  - obsidian
  - usability
evidence:
  - evidence_id: ev-245928470c0e
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 25346
    source_hash: 7c6cdcc2767adfd57707834e54f3b240772116c37a8c0c5c19ffa10e341225f9
    summary: 真实 Obsidian UI 验收已通过：Mermaid 已渲染为五节点图；首页能看到目标、当前状态、下一步、风险、用量/成本；决策目录显示中文状态/标签/理由；决策详情的“快速理解”位于正文前。接下来进入第二、第三轮代码审查，重点查恢复事务、路径隔离、生成标记伪造、dry-run 不变性、边界规模与 Windows 路径，而不只重复跑同一批测试。
  - evidence_id: ev-47a6c49eb4ed
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 24058
    source_hash: ef91f697503743f91dc77456d456cf203909dd7b66fdea3bfc7c0e844df9f43c
    summary: |
      按推荐实施
  - evidence_id: ev-b41a9bed8caa
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 24010
    source_hash: f00ce43914dfac308f98b2a2e158dd3a2678d0399d82e7303719de4ba04e8b10
    summary: |
      &#x20;A，项目演进图可以用 merid 图形的方式呈现
source_sessions:
  - 01a02971-61d6-7251-bdcf-f999230f961d
supersedes: []
sync_status: synced
---

# 采用导航型首页与 A1 Mermaid 项目演进主线
## 快速理解

<!-- session-reviewer:generated=v1;owner=navigation;section=快速理解 -->

- **结论：** 采用导航型首页与 A1 Mermaid 项目演进主线
- **状态：** 已接受
- **为什么重要：** 把 project-overview.md 作为统一入口，以目标、关键决策、已验证里程碑、当前状态、下一步组成固定五节点主线，并提供中文目录索引和详情快速理解区，能够在保持账本可编辑与证据可追溯的同时降低恢复成本。

## Context

已有项目台账虽然保存了结构化实体和关系图，但普通回顾缺少清晰入口，细分目录也要求读者理解内部 ID。

## Alternatives

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "只增加目录索引"
- sr-string: "仅保留独立关系图"
- sr-string: "在每个实体文件中手工维护摘要"

## Rationale

把 project-overview.md 作为统一入口，以目标、关键决策、已验证里程碑、当前状态、下一步组成固定五节点主线，并提供中文目录索引和详情快速理解区，能够在保持账本可编辑与证据可追溯的同时降低恢复成本。

## Rejected paths

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "要求用户从内部实体 ID 和技术字段自行恢复上下文"
- sr-string: "让派生导航内容参与普通三方语义合并"

## Evidence

- `ev-245928470c0e` (01a02971-61d6-7251-bdcf-f999230f961d:25346): 真实 Obsidian UI 验收已通过：Mermaid 已渲染为五节点图；首页能看到目标、当前状态、下一步、风险、用量/成本；决策目录显示中文状态/标签/理由；决策详情的“快速理解”位于正文前。接下来进入第二、第三轮代码审查，重点查恢复事务、路径隔离、生成标记伪造、dry-run 不变性、边界规模与 Windows 路径，而不只重复跑同一批测试。
- `ev-47a6c49eb4ed` (01a02971-61d6-7251-bdcf-f999230f961d:24058): 按推荐实施
- `ev-b41a9bed8caa` (01a02971-61d6-7251-bdcf-f999230f961d:24010): &#x20;A，项目演进图可以用 merid 图形的方式呈现

## Consequences

首页、索引、快速理解和 Mermaid 图由 accepted ledger 确定性派生并同步到 Obsidian；语义实体继续通过三方同步编辑，派生内容不参与 revision 合并。

## Conditions for reevaluation

五节点首页无法容纳项目恢复主线，或用户需要按角色、里程碑、版本等维度提供不同入口时。
