---
id: decision-safe-evidence-boundary
entity_type: decision
project_id: project-269b8cab6cbf69dd
revision: 2
title: 只持久化脱敏且可定位的允许列表证据
status: accepted
tags:
  - privacy
  - security
evidence:
  - evidence_id: ev-8bc7cd1e81d9
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 105
    source_hash: becf0af86b12f66d476b4cf7cba17215cdccaaf20ad03dbbf6a6c5b21e6bbda3
    summary: |-
      好，采用“自动合并非冲突字段，冲突进入待确认区”。

      下一个关键边界是隐私与噪声。Session 日志里可能包含命令输出、环境变量、路径、错误日志，甚至临时凭据。建议默认：

      - 分析用户消息、助手结论、工具调用结果和代码变更
      - 不导出隐藏推理、系统提示和开发者指令
      - 对疑似密钥、Token、Cookie、连接串自动脱敏
      - Markdown 只保留证据摘要及原 session 定位，不复制大段原始日志

      是否按这个安全边界设计？
  - evidence_id: ev-c30658e3123b
    session_id: 01a02971-61d6-7251-bdcf-f999230f961d
    jsonl_line: 111
    source_hash: 85567110781e7ae31d52f1c615f940fc9a446a64d14d86ad3e3107cacf87f9f5
    summary: |
      同意
source_sessions:
  - 01a02971-61d6-7251-bdcf-f999230f961d
supersedes: []
sync_status: synced
---

# 只持久化脱敏且可定位的允许列表证据
## 快速理解

<!-- session-reviewer:generated=v1;owner=navigation;section=快速理解 -->

- **结论：** 只持久化脱敏且可定位的允许列表证据
- **状态：** 已接受
- **为什么重要：** 程序先做角色允许列表、脱敏、限量和证据定位，Markdown 不复制原始日志。

## Context

本地 session 可能包含凭据、环境信息、隐藏推理和大量无关日志。

## Alternatives

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "直接让模型读取整个原始日志"

## Rationale

程序先做角色允许列表、脱敏、限量和证据定位，Markdown 不复制原始日志。

## Rejected paths

<!-- session-reviewer:list-codec=v1 -->
- sr-string: "导出隐藏推理或系统开发者指令"
- sr-string: "复制大段原始工具输出"

## Evidence

- `ev-8bc7cd1e81d9` (01a02971-61d6-7251-bdcf-f999230f961d:105): 好，采用“自动合并非冲突字段，冲突进入待确认区”。  下一个关键边界是隐私与噪声。Session 日志里可能包含命令输出、环境变量、路径、错误日志，甚至临时凭据。建议默认：  - 分析用户消息、助手结论、工具调用结果和代码变更 - 不导出隐藏推理、系统提示和开发者指令 - 对疑似密钥、Token、Cookie、连接串自动脱敏 - Markdown 只保留证据摘要及原 session 定位，不复制大段原始日志  是否按这个安全边界设计？
- `ev-c30658e3123b` (01a02971-61d6-7251-bdcf-f999230f961d:111): 同意

## Consequences

Skill 只读取有界 evidence packet，不直接读取原始 JSONL。

## Conditions for reevaluation

若日志格式或威胁模型变化，需要扩展解析与脱敏规则。
