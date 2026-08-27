---
id: open-loop-skill-installer-executable-bits
entity_type: open_loop
project_id: project-269b8cab6cbf69dd
revision: 1
title: 让 GitHub Skill 安装后保留 POSIX wrapper 可执行性
status: open
tags:
  - distribution
  - installer
  - posix
evidence:
  - evidence_id: ev-6ba835bb0c8e
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 27620
    source_hash: 30116a91d83d9efff5d0e564316d3068aedeb96aff25e5ba289c515b1815a755
    summary: 验证发现一个会影响实际调用的问题：Skill 的两个 POSIX wrapper 被安装为 `0644`，而 `SKILL.md` 要求直接执行它们；这会在 macOS 上触发权限拒绝。CLI 与只读功能本身正常。我会按系统化调试/TDD流程确认权限是在源码、GitHub 安装器还是 Release 打包环节丢失，先修好本机安装，再决定是否需要修补公共包。
  - evidence_id: ev-871cd3987511
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 27630
    source_hash: 7ccb3e23d52eb0890ccad347930bea88cdaec0d4e25efd967980bda6711e805d
    summary: 根因已确认：源码和 `v0.1.0` Release 归档中的 `.sh` 都是正确的 `0755`；丢失执行位发生在 GitHub Skill 安装器的下载/落盘环节。当前安装的最小正确修复是恢复两个 wrapper 的执行位，不需要改 CLI 或 ledger。刚才的红证据是直接执行返回 `126 permission denied`。
source_sessions:
  - 01a02971-61d6-7251-bdcf-f999230f961d
sync_status: synced
---

# 让 GitHub Skill 安装后保留 POSIX wrapper 可执行性
## 快速理解

<!-- session-reviewer:generated=v1;owner=navigation;section=快速理解 -->

- **问题：** 如何让用户从 GitHub 安装 session-reviewer Skill 后，无需手工 chmod 就能直接运行 POSIX wrappers？
- **状态：** 开放
- **当前阻塞：** GitHub Skill 安装器的下载或落盘过程未保留 POSIX 执行位。
- **下一实验：** 评估将 Skill 调用改为显式 sh wrapper，或提供可验证的安装后 chmod 机制，并在干净 macOS/Linux 环境复验。

## Question

如何让用户从 GitHub 安装 session-reviewer Skill 后，无需手工 chmod 就能直接运行 POSIX wrappers？

## Available evidence

- `ev-6ba835bb0c8e` (01a02971-61d6-7251-bdcf-f999230f961d:27620): 验证发现一个会影响实际调用的问题：Skill 的两个 POSIX wrapper 被安装为 `0644`，而 `SKILL.md` 要求直接执行它们；这会在 macOS 上触发权限拒绝。CLI 与只读功能本身正常。我会按系统化调试/TDD流程确认权限是在源码、GitHub 安装器还是 Release 打包环节丢失，先修好本机安装，再决定是否需要修补公共包。
- `ev-871cd3987511` (01a02971-61d6-7251-bdcf-f999230f961d:27630): 根因已确认：源码和 `v0.1.0` Release 归档中的 `.sh` 都是正确的 `0755`；丢失执行位发生在 GitHub Skill 安装器的下载/落盘环节。当前安装的最小正确修复是恢复两个 wrapper 的执行位，不需要改 CLI 或 ledger。刚才的红证据是直接执行返回 `126 permission denied`。

## Attempted paths

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "确认源码和 v0.1.0 Release 归档中的两个 .sh wrapper 均为 0755。"
- sr-string: "确认 GitHub Skill 安装器落盘后两个 wrapper 变为 0644，直接执行返回 126。"
- sr-string: "当前本机已手工恢复 0755，并完成 wrapper、checkpoint、resume、status 与 dry-run 验证。"

## Blocking condition

GitHub Skill 安装器的下载或落盘过程未保留 POSIX 执行位。

## Recommended next experiment

评估将 Skill 调用改为显式 sh wrapper，或提供可验证的安装后 chmod 机制，并在干净 macOS/Linux 环境复验。

## Completion criterion

从 GitHub 全新安装后，两个 POSIX wrapper 无需手工修复即可完成 prepare 与 apply 工作流。
