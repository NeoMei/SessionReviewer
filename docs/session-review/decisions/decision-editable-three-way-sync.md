---
id: decision-editable-three-way-sync
entity_type: decision
project_id: project-269b8cab6cbf69dd
revision: 2
title: 项目与 Obsidian 使用可编辑三方同步
status: accepted
tags:
  - obsidian
  - sync
evidence:
  - evidence_id: ev-4a74e0cf84fc
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 89
    source_hash: 1ce1fcf05d27e606e746dbffdb9458320d58ca95ca63cabe880186f81db82f27
    summary: |
      最好能编辑
  - evidence_id: ev-af651be39267
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 100
    source_hash: 217af19d423a25ce6f96f6b7ba356f3ed95460460601610921275d48c2bc1243
    summary: |
      按推荐走
  - evidence_id: ev-caf32c9ed2fe
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 78
    source_hash: 7b4592411724c326cd7d41c8c962865f842a30f27af962cd1c4efecb35755eaf
    summary: |
      我希望两边都写
source_sessions:
  - 01a02971-61d6-7251-bdcf-f999230f961d
supersedes: []
sync_status: synced
---

# 项目与 Obsidian 使用可编辑三方同步
## 快速理解

<!-- session-reviewer:generated=v1;owner=navigation;section=快速理解 -->

- **结论：** 项目与 Obsidian 使用可编辑三方同步
- **状态：** 已接受
- **为什么重要：** 以 Base、Project、Vault 三方比较自动合并非冲突字段，冲突显式待确认，避免最近写入覆盖造成静默丢失。

## Context

用户希望项目目录和 Obsidian 都保存记录，并能从 Obsidian 编辑。

## Alternatives

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "Obsidian 只读镜像"
- sr-string: "最近修改覆盖"
- sr-string: "每次手动选择一侧"

## Rationale

以 Base、Project、Vault 三方比较自动合并非冲突字段，冲突显式待确认，避免最近写入覆盖造成静默丢失。

## Rejected paths

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "整篇 Markdown 双向覆盖"

## Evidence

- `ev-4a74e0cf84fc` (01a02971-61d6-7251-bdcf-f999230f961d:89): 最好能编辑
- `ev-af651be39267` (01a02971-61d6-7251-bdcf-f999230f961d:100): 按推荐走
- `ev-caf32c9ed2fe` (01a02971-61d6-7251-bdcf-f999230f961d:78): 我希望两边都写

## Consequences

每个实体需要稳定 ID、revision、基线快照和可恢复冲突状态。

## Conditions for reevaluation

若 Obsidian 侧改为只读镜像，可简化为单向同步。
