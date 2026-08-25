# Project evolution

This file is derived from the accepted project ledger. Manual edits are overwritten on the next accepted render.

## Recovery mainline

```mermaid
flowchart LR
  goal["项目目标<br/>交付可从长 session 恢复项目脉络、维护可编辑 Markdown 账本并与 Obsidian 三方同步的 SessionReviewer。"]
  decisions["关键决策汇总<br/>产品采用 Skill 加本地 CLI 引擎<br/>只持久化脱敏且可定位的允许列表证据<br/>覆盖沉淀、恢复与跨 session 聚合<br/>另有 3 项"]
  milestones["最近已验证里程碑<br/>GitHub 三平台 CI 全绿<br/>真实 Obsidian 同步与发行工程通过候选验证"]
  current["当前状态<br/>`v0.1.0` 已发布为公开 GitHub Release，标签固定在 `9c2397f`；macOS Intel、Apple Silicon、Windows x64 三个归档和 `SHA…"]
  next["下一步<br/>监测首批安装反馈；若发现影响运行时的缺陷，按语义化版本发布后续修复版。当前不移动已公开的 `v0.1.0` 标签。<br/>开放待办：0"]
  goal --> decisions --> milestones --> current --> next
```

## Causal evolution

```mermaid
flowchart LR
  event_162f912a71cb["2026-08-24T13:21:20.277Z · GitHub 三平台 CI 全绿"]
  event_4334d43609d1["2026-08-24T16:35:35.779Z · 真实 Obsidian 同步与发行工程通过候选验证"]
  decision_5dd62002d6a4["Decision: 覆盖沉淀、恢复与跨 session 聚合 · status: accepted · tags: continuity, product-scope"]
  decision_04220dd8a2c5["Decision: 项目与 Obsidian 使用可编辑三方同步 · status: accepted · tags: obsidian, sync"]
  decision_078fe83339c8["Decision: 采用混合触发与本地同步引擎 · status: accepted · tags: sync, workflow"]
  decision_ed466f029681["Decision: 记录 session 与项目级模型用量和公开标价成本 · status: accepted · tags: accounting, cost, models"]
  decision_9cd2795959d6["Decision: 只持久化脱敏且可定位的允许列表证据 · status: accepted · tags: privacy, security"]
  decision_757157859a2b["Decision: 产品采用 Skill 加本地 CLI 引擎 · status: accepted · tags: architecture, product"]
  loop_98b011131a63["Loop: 闭合原子写恢复状态机复审 · status: resolved · tags: atomic-write, recovery, review"]
  loop_a65fd34793b2["Loop: 闭合 GitHub 三平台 CI · status: resolved · tags: ci, github, windows"]
  loop_3012e18a6015["Loop: 完成真实端到端 history 可见性验收 · status: resolved · tags: e2e, history, ux"]
  loop_4ac845327a15["Loop: 完成 v0.1.0 可安装发行 · status: resolved · tags: distribution, github, release"]
  event_162f912a71cb --> event_4334d43609d1
  event_162f912a71cb --> loop_a65fd34793b2
  event_4334d43609d1 --> loop_4ac845327a15
```

## Project relationships

```mermaid
graph TD
  project_c45e22b0debf["Project: project-269b8cab6cbf69dd · goal: 交付可从长 session 恢复项目脉络、维护可编辑 Markdown 账本并与 Obsidian 三方同步的 SessionReviewer。"]
  session_b7e0d95aa083["Session: 01a02971-61d6-7251-bdcf-f999230f961d"]
  decision_5dd62002d6a4["Decision: 覆盖沉淀、恢复与跨 session 聚合 · status: accepted · tags: continuity, product-scope"]
  decision_04220dd8a2c5["Decision: 项目与 Obsidian 使用可编辑三方同步 · status: accepted · tags: obsidian, sync"]
  decision_078fe83339c8["Decision: 采用混合触发与本地同步引擎 · status: accepted · tags: sync, workflow"]
  decision_ed466f029681["Decision: 记录 session 与项目级模型用量和公开标价成本 · status: accepted · tags: accounting, cost, models"]
  decision_9cd2795959d6["Decision: 只持久化脱敏且可定位的允许列表证据 · status: accepted · tags: privacy, security"]
  decision_757157859a2b["Decision: 产品采用 Skill 加本地 CLI 引擎 · status: accepted · tags: architecture, product"]
  loop_98b011131a63["Loop: 闭合原子写恢复状态机复审 · status: resolved · tags: atomic-write, recovery, review"]
  loop_a65fd34793b2["Loop: 闭合 GitHub 三平台 CI · status: resolved · tags: ci, github, windows"]
  experiment_6da5c21c7d38["Next experiment: 无需继续；保留 CI 作为后续发布门禁。"]
  loop_3012e18a6015["Loop: 完成真实端到端 history 可见性验收 · status: resolved · tags: e2e, history, ux"]
  loop_4ac845327a15["Loop: 完成 v0.1.0 可安装发行 · status: resolved · tags: distribution, github, release"]
  blocker_59a6ee3695cc["Blocker: 无；发布完成条件已满足。"]
  experiment_266258fa93db["Next experiment: 监测首批安装与使用反馈；运行时修复使用后续语义化版本，不移动 v0.1.0 标签。"]
  loop_4ac845327a15 --> blocker_59a6ee3695cc
  loop_4ac845327a15 --> experiment_266258fa93db
  loop_a65fd34793b2 --> experiment_6da5c21c7d38
  project_c45e22b0debf --> decision_04220dd8a2c5
  project_c45e22b0debf --> decision_078fe83339c8
  project_c45e22b0debf --> decision_5dd62002d6a4
  project_c45e22b0debf --> decision_757157859a2b
  project_c45e22b0debf --> decision_9cd2795959d6
  project_c45e22b0debf --> decision_ed466f029681
  project_c45e22b0debf --> loop_3012e18a6015
  project_c45e22b0debf --> loop_4ac845327a15
  project_c45e22b0debf --> loop_98b011131a63
  project_c45e22b0debf --> loop_a65fd34793b2
  project_c45e22b0debf --> session_b7e0d95aa083
```
