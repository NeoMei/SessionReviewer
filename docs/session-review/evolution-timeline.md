---
id: evolution-timeline
entity_type: timeline
project_id: project-269b8cab6cbf69dd
revision: 6
events:
  - id: timeline-cross-platform-ci-closed
    occurred_at: "2026-08-24T13:21:20.277Z"
    revision: 1
    class: verified
    title: GitHub 三平台 CI 全绿
    summary: macOS Intel、macOS Apple Silicon 和 Windows x64 目标全部通过，Windows 还完成 20 次原生替换压力测试。
    evidence:
      - evidence_id: ev-1704b8b6e214
        session_id: 01a02971-61d6-7251-bdcf-f999230f961d
        jsonl_line: 21343
        source_hash: 7484373d20baa2c9ea78fabda694eaafdc3ab89ca913c7d9e17e9cab175b5ff4
        summary: 发布验收已完成：GitHub CI 三个目标全部全绿，Windows 还额外通过了 20 次原生替换压力测试。最后我会核对公开可见性、远端 `main`、本地工作区洁净度和 CI 结论，然后给你仓库链接及当前发布边界。
    decision_ids: []
    open_loop_ids:
      - open-loop-github-ci-cross-platform
  - id: timeline-real-sync-and-release-engineering
    occurred_at: "2026-08-24T16:35:35.779Z"
    revision: 1
    class: verified
    title: 真实 Obsidian 同步与发行工程通过候选验证
    summary: 真实 Vault 三方同步达到零 pending、零 conflict、零 error；三平台归档、校验和、版本元数据和同步中断恢复均通过候选验证，并确定 Apache-2.0 许可。
    evidence:
      - evidence_id: ev-08fc3c48cb70
        session_id: 01a02971-61d6-7251-bdcf-f999230f961d
        jsonl_line: 22296
        source_hash: dda943d82c62881f753873641379746d6396cb6b6feaecaa67612b363af41139
        summary: 第 4 项已经在真实配置上完成：`sync --dry-run` 计划出 1 个 `add_vault` 且不落盘，正式 `sync` 随后把项目概览写到了已配置的 Obsidian Vault，`sync status --json` 返回零 pending、零 conflict、零 error。现在进入第 5 项：我会先补跨平台可复现打包、校验和、安装说明和发布 CI；许可证与版权主体属于法律声明，我会把其余发布条件全部准备好后，只就这一个不可替你推断的选择确认。
      - evidence_id: ev-d629d2d78539
        session_id: 01a02971-61d6-7251-bdcf-f999230f961d
        jsonl_line: 22599
        source_hash: fc6a4c2806ed3cb129ddf28714f1074f9d9d0334d387ca6df3d2c396f1eb526f
        summary: 收到，我按推荐落地为 Apache-2.0，版权声明使用：`Copyright 2026 NeoMei and QUUKK`。本轮我会先加入官方许可证文本和 NOTICE，然后重新开始一条干净的 `session-reviewer` 自举接受链；该 skill 会禁止同一轮进行 Git commit/push/tag，所以本轮完成台账与成本记录，下一轮再执行 GitHub 发布。
      - evidence_id: ev-dcaed2dd3e78
        session_id: 01a02971-61d6-7251-bdcf-f999230f961d
        jsonl_line: 22476
        source_hash: 68fcb7d28aa4113cca46de3b86d045064510d0422bff5a7aa1118bb82819e764
        summary: 发布工程也已跑通候选验证：3 个归档（macOS Intel、macOS Apple Silicon、Windows x64）都通过 `SHA256SUMS`，包内含 CLI、README 和完整 Skill；解包后的 macOS 二进制报告 `0.1.0 + 40 位 commit + RFC3339 build time`。我还补了中断恢复：如果 Project 已写、Vault 写失败，下一次同步会根据无内容 journal 恢复两边和 Base，而不是把 `revision` 差异误判为冲突。现在开始全量测试、race、vet 和 Windows 交叉编译门禁。
    decision_ids: []
    open_loop_ids:
      - open-loop-v0.1.0-release
  - id: timeline-human-readable-navigation-ui-accepted
    occurred_at: "2026-08-24T21:17:27.916Z"
    revision: 1
    class: verified
    title: 人类可读项目导航通过真实 Obsidian UI 验收
    summary: 导航型项目首页、A1 Mermaid 五节点主线、中文目录索引和详情快速理解区已在真实 Vault 中渲染并可读。
    evidence:
      - evidence_id: ev-245928470c0e
        session_id: 01a02971-61d6-7251-bdcf-f999230f961d
        jsonl_line: 25346
        source_hash: 7c6cdcc2767adfd57707834e54f3b240772116c37a8c0c5c19ffa10e341225f9
        summary: 真实 Obsidian UI 验收已通过：Mermaid 已渲染为五节点图；首页能看到目标、当前状态、下一步、风险、用量/成本；决策目录显示中文状态/标签/理由；决策详情的“快速理解”位于正文前。接下来进入第二、第三轮代码审查，重点查恢复事务、路径隔离、生成标记伪造、dry-run 不变性、边界规模与 Windows 路径，而不只重复跑同一批测试。
    decision_ids:
      - decision-human-readable-navigation
    open_loop_ids: []
  - id: timeline-concurrent-edit-protection-verified
    occurred_at: "2026-08-24T21:31:36.445Z"
    revision: 1
    class: verified
    title: 同步与恢复并发编辑保护闭环
    summary: 语义同步与中断恢复均绑定 Project/Vault 预映像；并发编辑会重新合并或失败关闭，不再静默覆盖人工修改。
    evidence:
      - evidence_id: ev-ed856a7145fa
        session_id: 01a02971-61d6-7251-bdcf-f999230f961d
        jsonl_line: 25711
        source_hash: 26b4cc31ec299b7ddd2101ba57f7d75bcfae73b558914155a0e4c2b769bcfbfa
        summary: |-
          并发保护现在覆盖了两条链路：

          - 普通语义同步：扫描时记录 Project/Vault 精确 preimage 哈希，写入前再校验；若第一次尚未写入任何一侧，就丢弃 planned journal、重新三方合并，因此两个并发且不冲突的编辑都保留。
          - 已写入一侧后的恢复：journal 绑定两侧原始哈希；另一侧若在中断后又改过，恢复失败并保留新编辑，不再自动覆盖。

          相关恢复、重命名、事务严格校验和派生发布测试均已通过。现在进行最终新鲜门禁；若还有失败，继续按同样方式修复。
    decision_ids:
      - decision-editable-three-way-sync
    open_loop_ids: []
  - id: timeline-v0.1.0-public-release
    occurred_at: "2026-08-25T02:29:30.221Z"
    revision: 1
    class: verified
    title: v0.1.0 完成公开发布
    summary: GitHub Release、三平台归档、SHA256SUMS、标签和 main CI 均完成外部验证；标签固定在 9c2397f，main 包含后续确定性测试维护。
    evidence:
      - evidence_id: ev-2c6bfa83edca
        session_id: 01a02971-61d6-7251-bdcf-f999230f961d
        jsonl_line: 27527
        source_hash: f1122f847cd764135961a182afc91fda8a061dbcd3117fcf179335e02c2985c7
        summary: |-
          已完成公开发布：

          - [GitHub Release v0.1.0](https://github.com/NeoMei/SessionReviewer/releases/tag/v0.1.0)
          - 标签提交：`9c2397f`
          - 最新 `main`：`6f44605`
          - macOS Intel、Apple Silicon、Windows x64 三个平台包均已上传
          - `SHA256SUMS` 下载复验全部通过
          - [Release 工作流](https://github.[REDACTED:HIGH_ENTROPY_TOKEN])、[标签三平台 CI](https://github.[REDACTED:HIGH_ENTROPY_TOKEN])、[最新 main CI](https://github.[REDACTED:HIGH_ENTROPY_TOKEN]) 均已全绿
          - 公开二进制自检确认：`version=0.1.0`、commit=`9c2397f`
          - 发布期间发现并修复了一个偶发计时测试，未影响运行时代码或已发布二进制
          - Project/Obsidian 仍为 14/14 同步，重复 dry-run 为零操作
          - 本地与远端 `main` 完全一致，工作树干净

          README 已加入面向用户的下载、平台选择和校验说明。

          <oai-mem-citation>
          <citation_entries>
          MEMORY.md:99-109|note=[SessionReviewer prior source release CI and final release requirements]
          </citation_entries>
          <rollout_ids>
          </rollout_ids>
          </oai-mem-citation>
    decision_ids: []
    open_loop_ids:
      - open-loop-v0.1.0-release
  - id: timeline-local-skill-installation-verified
    occurred_at: "2026-08-25T02:59:15.485Z"
    revision: 1
    class: verified
    title: 本机 Skill 与 CLI 安装通过真实调用验证
    summary: v0.1.0 Skill 和 CLI 已安装；checkpoint、resume、sync status 与 dry-run 均真实运行成功，本机 wrapper 执行位已恢复。
    evidence:
      - evidence_id: ev-1ddf2eebacc9
        session_id: 01a02971-61d6-7251-bdcf-f999230f961d
        jsonl_line: 27662
        source_hash: 4bd7336edb5a72b5d03cd9d1ac17c07483ee16084ccb383c960223a311e4e713
        summary: |-
          已安装并验证可用。

          - Skill：[session-reviewer](/Users/neomei/.codex/skills/session-reviewer/SKILL.md)
          - CLI：`/Users/neomei/.local/bin/session-reviewer`
          - 版本：`v0.1.0`
          - Commit：`9c2397f`
          - Release 校验和全部通过
          - checkpoint、resume、sync status、dry-run 均真实运行成功
          - Project/Obsidian：14/14 同步，零待处理操作
          - 安装器丢失的 wrapper 执行权限已恢复
          - 测试产生的敏感临时 packet 已删除
          - 项目工作树保持干净

          Skill 将从下一轮开始被 Codex 发现。你可以直接说：

          > 使用 session-reviewer 回顾本项目

          或：

          > 使用 session-reviewer 为当前项目建立 checkpoint

          如果下一轮仍未显示该技能，重启一次 Codex 即可。
    decision_ids:
      - decision-skill-plus-local-cli
    open_loop_ids:
      - open-loop-skill-installer-executable-bits
  - id: timeline-post-truncation-redaction-repaired
    occurred_at: "2026-08-25T03:45:44.447Z"
    revision: 1
    class: verified
    title: 截断后二次脱敏缺陷完成 TDD 修复并恢复 apply
    summary: prepare 截断后新形成高熵候选导致 apply 拒绝的问题已通过失败用例、最小修复、重生成安全 packet 和成功 apply 闭环。
    evidence:
      - evidence_id: ev-7409bb8aab5e
        session_id: 01a02971-61d6-7251-bdcf-f999230f961d
        jsonl_line: 28161
        source_hash: f7fa2f0ec8391098e5d7c904bbbccf0eb96a17891bcce2d77271370e2c9ca9e9
        summary: 第二包已成功接受，cursor 推进到 `27538`；发行待办现在使用真实 Release 证据，并新增并发保护、公开发布两条里程碑。由于该包仍为 `has_more=true`，我会清理成功重试文件并继续第三包；旧失败文件暂留到整个回顾结束，便于核对修复闭环。
      - evidence_id: ev-f746282bd3b9
        session_id: 01a02971-61d6-7251-bdcf-f999230f961d
        jsonl_line: 28107
        source_hash: 46cc42f2f77a5fdab3b565b55ca6107126d4d1bbf394ebf3aa53d260220bd3ad
        summary: GREEN 已通过聚焦用例和整个 evidence 包。因为旧 packet 已经固化了不安全截断结果，不能原地修改；我会保留它作为诊断证据，使用修复后的本地二进制从同一 accepted cursor 重新 prepare 一份 `02b` packet（不使用 `--from-start`），再重新绑定 proposal。这样既不篡改证据，也保持 cursor 边界。
    decision_ids:
      - decision-safe-evidence-boundary
    open_loop_ids: []
  - id: timeline-applied-receipt-trust-sync-converged
    occurred_at: "2026-08-25T04:11:09.847Z"
    revision: 2
    class: verified
    title: 受信任 apply receipt 链与 dry-run/Obsidian 同步闭环
    summary: 完整 applied-receipt 链只在 Vault 未偏离 Base 时桥接受信任边界；dry-run 对待写 Base 正确延后派生规划，真实 16/16 同步、全仓/race/vet 与 Windows/macOS 交叉门禁全部通过。
    evidence:
      - evidence_id: ev-2e25fbe9864a
        session_id: 01a02971-61d6-7251-bdcf-f999230f961d
        jsonl_line: 28765
        source_hash: 4f1c29a5623ac5db552f11b0da38ae44663aad8e9ff5de7aa32a885f03ea08f1
        summary: 第二个 dry-run 边界也已修复并在真实映射验证：应用前 status 能正常显示 3 个可信更新且派生状态 deferred，正式同步后恢复为 16/16、二次 dry-run 0 operations、Project/Vault diff 为 0，`resume` 已显示正确停点。现在跑最终全量、race 与 macOS/Windows 交叉编译门禁。
      - evidence_id: ev-51e4b470746b
        session_id: 01a02971-61d6-7251-bdcf-f999230f961d
        jsonl_line: 28778
        source_hash: 5fcc08f6490f626fefb5e02570b97f275224fc7971e4b70c6374b8849f584b02
        summary: 最终门禁全部通过：全仓测试、vet、module tidy、关键包 race，以及 Windows amd64 / macOS amd64 交叉测试构建均为绿。为避免恢复卡遗漏刚发现的 dry-run 边界修复，我再写入一个很小的最终 checkpoint；之后只做同步复验和清理，不再扩展实现范围。
      - evidence_id: ev-a7d0b85e0b73
        session_id: 01a02971-61d6-7251-bdcf-f999230f961d
        jsonl_line: 28729
        source_hash: 2c850334ed39fd9501bd6951619a898c3933d6608e8abd6286262cd770c9a23c
        summary: 定位到了第二个边界 bug：正式同步路径本身可收敛，但 `status/dry-run` 在 receipt-trusted 变更尚未写 Base 时，错误地提前规划派生导航，于是把“预期中的旧 Base”当成异常。修复原则是：只要 dry-run 发现 accepted 结果会改变 Project 语义或 Base 语义，就把派生规划标为 deferred；不放松任何校验。
    decision_ids: []
    open_loop_ids: []
sync_status: synced
---

# Evolution timeline
## Events

- **2026-08-24T13:21:20.277Z** `verified` `timeline-cross-platform-ci-closed` — GitHub 三平台 CI 全绿: macOS Intel、macOS Apple Silicon 和 Windows x64 目标全部通过，Windows 还完成 20 次原生替换压力测试。
- **2026-08-24T16:35:35.779Z** `verified` `timeline-real-sync-and-release-engineering` — 真实 Obsidian 同步与发行工程通过候选验证: 真实 Vault 三方同步达到零 pending、零 conflict、零 error；三平台归档、校验和、版本元数据和同步中断恢复均通过候选验证，并确定 Apache-2.0 许可。
- **2026-08-24T21:17:27.916Z** `verified` `timeline-human-readable-navigation-ui-accepted` — 人类可读项目导航通过真实 Obsidian UI 验收: 导航型项目首页、A1 Mermaid 五节点主线、中文目录索引和详情快速理解区已在真实 Vault 中渲染并可读。
- **2026-08-24T21:31:36.445Z** `verified` `timeline-concurrent-edit-protection-verified` — 同步与恢复并发编辑保护闭环: 语义同步与中断恢复均绑定 Project/Vault 预映像；并发编辑会重新合并或失败关闭，不再静默覆盖人工修改。
- **2026-08-25T02:29:30.221Z** `verified` `timeline-v0.1.0-public-release` — v0.1.0 完成公开发布: GitHub Release、三平台归档、SHA256SUMS、标签和 main CI 均完成外部验证；标签固定在 9c2397f，main 包含后续确定性测试维护。
- **2026-08-25T02:59:15.485Z** `verified` `timeline-local-skill-installation-verified` — 本机 Skill 与 CLI 安装通过真实调用验证: v0.1.0 Skill 和 CLI 已安装；checkpoint、resume、sync status 与 dry-run 均真实运行成功，本机 wrapper 执行位已恢复。
- **2026-08-25T03:45:44.447Z** `verified` `timeline-post-truncation-redaction-repaired` — 截断后二次脱敏缺陷完成 TDD 修复并恢复 apply: prepare 截断后新形成高熵候选导致 apply 拒绝的问题已通过失败用例、最小修复、重生成安全 packet 和成功 apply 闭环。
- **2026-08-25T04:11:09.847Z** `verified` `timeline-applied-receipt-trust-sync-converged` — 受信任 apply receipt 链与 dry-run/Obsidian 同步闭环: 完整 applied-receipt 链只在 Vault 未偏离 Base 时桥接受信任边界；dry-run 对待写 Base 正确延后派生规划，真实 16/16 同步、全仓/race/vet 与 Windows/macOS 交叉门禁全部通过。
