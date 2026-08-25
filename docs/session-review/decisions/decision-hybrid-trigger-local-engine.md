---
id: decision-hybrid-trigger-local-engine
entity_type: decision
project_id: project-269b8cab6cbf69dd
revision: 2
title: 采用混合触发与本地同步引擎
status: accepted
tags:
  - sync
  - workflow
evidence:
  - evidence_id: ev-6be616ea732d
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 122
    source_hash: 1e66d31cd52a5329e88576aa24784897a4c15cad06115b9b845e8a5ad26d0e28
    summary: |
      混合模式，我想知道 Obsidian 这边编辑了，通过什么机制同步到代码目录？
  - evidence_id: ev-a1abd45c3b7d
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 153
    source_hash: 63c4b16f2a694844051e131dfa8396886c22e12bce23b98168305031de15430c
    summary: |
      好的第三种，符合预期
source_sessions:
  - 01a02971-61d6-7251-bdcf-f999230f961d
supersedes: []
sync_status: synced
---

# 采用混合触发与本地同步引擎
## 快速理解

<!-- session-reviewer:generated=v1;owner=navigation;section=快速理解 -->

- **结论：** 采用混合触发与本地同步引擎
- **状态：** 已接受
- **为什么重要：** 按需手动回顾与阶段提醒结合；同步由本地引擎在文件系统层执行。

## Context

Skill 只有被调用时运行，无法独立持续监听 Obsidian 文件变化。

## Alternatives

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "全手动"
- sr-string: "尽量自动"
- sr-string: "依赖 Obsidian 插件"

## Rationale

按需手动回顾与阶段提醒结合；同步由本地引擎在文件系统层执行。

## Rejected paths

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "把持续监听职责放进一次性 Skill 调用"

## Evidence

- `ev-6be616ea732d` (01a02971-61d6-7251-bdcf-f999230f961d:122): 混合模式，我想知道 Obsidian 这边编辑了，通过什么机制同步到代码目录？
- `ev-a1abd45c3b7d` (01a02971-61d6-7251-bdcf-f999230f961d:153): 好的第三种，符合预期

## Consequences

CLI 与 Skill 共用同一确定性同步服务，后台 watcher 可作为后续能力。

## Conditions for reevaluation

若运行环境不允许本地进程或文件监听。
