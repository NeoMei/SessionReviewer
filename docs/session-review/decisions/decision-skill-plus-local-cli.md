---
id: decision-skill-plus-local-cli
entity_type: decision
project_id: project-269b8cab6cbf69dd
revision: 2
title: 产品采用 Skill 加本地 CLI 引擎
status: accepted
tags:
  - architecture
  - product
evidence:
  - evidence_id: ev-653d42c6a4f4
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 169
    source_hash: 220398a1f3c7a79140f7bb4c95cdc343754d91b08a541e5d73b801300c26f8e9
    summary: |
      二
source_sessions:
  - 01a02971-61d6-7251-bdcf-f999230f961d
supersedes: []
sync_status: synced
---

# 产品采用 Skill 加本地 CLI 引擎
## 快速理解

<!-- session-reviewer:generated=v1;owner=navigation;section=快速理解 -->

- **结论：** 产品采用 Skill 加本地 CLI 引擎
- **状态：** 已接受
- **为什么重要：** Skill 负责语义判断，本地 CLI 负责确定性解析、脱敏、游标、账本与同步。

## Context

纯 Skill 难以可靠处理超长日志和后台同步，独立桌面应用第一版成本过高。

## Alternatives

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "纯 Skill"
- sr-string: "独立桌面应用或 Obsidian 插件"

## Rationale

Skill 负责语义判断，本地 CLI 负责确定性解析、脱敏、游标、账本与同步。

## Rejected paths

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "第一版直接建设完整 GUI"

## Evidence

- `ev-653d42c6a4f4` (01a02971-61d6-7251-bdcf-f999230f961d:169): 二

## Consequences

第一版按 review、checkpoint/resume、sync、history 分层交付。

## Conditions for reevaluation

底层协议稳定且确有独立 GUI 需求时。
