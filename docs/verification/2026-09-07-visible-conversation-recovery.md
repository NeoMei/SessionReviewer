# 可见问答恢复：验证记录

## 范围

修复本 Session 扫描误报和缺少 Agent 回答的展示链路。工作分支为 `codex/v4-scan-display`；不合并 main、不推送、不发布。真实 Obsidian 为 NeoMei-Docs / Obsidian 1.13.7。

## 修复前证据

- 公开 generation：`scan-2b8d92add5716473ff69758efed4bb64`。
- 项目：`project-269b8cab6cbf69dd`，逻辑 Session：`01a06a33-fe42-77c3-b850-c4eeaa4c13fa`。
- 5 个物理分段按逻辑顺序拼接，读取已发布边界的 119,283,846 字节；SHA-256 与目录记录一致：`a1f4f010916c206984b9b0204586b7adbdd3b375d9c72de7bf64d4ea6894d475`。
- 该前缀 34,198 条记录，损坏 JSON 为 0；4,684 条 `token_usage_record` 恰好对应全部未解码报告。它们是补充用量元数据，既不是问答，也不能与 `event_msg/token_count` 重复计费。
- 78 条 user-role 消息、1,372 条 assistant-role 可见消息。后者为 1,311 条 commentary 和 61 条 final_answer。纯环境记录不等于真实人类提问，正式问答统计需确定性排除已知包装。
- 可见消息记录最大 51,780 字节，均未超 64 KiB；另有 242 条非可见记录超过此上限。内部推理、工具原始输出、加密内容不应借恢复功能显示。
- 原生面板只能看到 83 个机器事实索引项，末页 `76–83 / 83`；Agent 回答未展示。`sync status` 为 in_sync=1、conflicted=0、blocked=0，证明文件同步成功不等于问答恢复。

## 自动化验证

兼容性修复 `efa7d6f` 已有 RED/GREEN 回归及独立审查。问答后端、插件集成与最终全量验证仍在执行；本记录当前不构成发布或最终验收证明。

后端开发中候选只读遍历已通过：73 组问答、73 条用户消息、1,372 条 Agent 消息、61 条 final_answer，共 1,445 个唯一修订，无分页缺口；正文截断为 0，2 条超过 4,096 字节的正文可完整读取。89 次 CLI 查询，最大响应 64,171 字节，单次最长 2,013 ms。查询前后项目及 Vault 的公开文件哈希、published generation 指针不变。该次尚非最终构建验收。

后端提交 `a46e828` 完成独立审查，修复回合 `6f549b7` 补齐异常时间字段验证和 channel-only 过程消息状态；复审两项均关闭。修复后再次全量只读遍历，全部 1,445 条返回消息的源记录哈希、角色及阶段均与实际源记录匹配，最大查询时间 1,105 ms；计数、正文和文件哈希不变。

控制器 `go test ./...` 全量通过（CLI 74.854s、zero-token 126.343s），`go vet ./...` 和 `go mod tidy -diff` 通过。后端审查后增补的定点回归通过；最终整分支与原生 UI 验收尚未完成。

覆盖边界：已确定性排除 5 条纯上下文 user 包装；认证源前缀仍有 242 条超过 64 KiB 的记录，查询只报告它们被省略，不把它们误称为缺失回答。独立源统计确认它们是非可见记录；查询侧保守 `complete=false`，不以关闭限制换取“全量”标记。

## 最终验收（2026-09-07）

以上为阶段性记录；本节为最终结果。生产代码固定在 `84a1b3b4a15e168b84f04acc08e29a62a6adacfb`。整分支审查及单次最终修复后的定点复审全部关闭，无未处理 Critical/Important finding。

- 最终 `go test ./...` 退出 0：CLI 75.277s、inspect 25.072s、zero-token 104.927s；`go vet ./...`、`go mod tidy -diff` 退出 0。
- 最终插件 `npm run check` 退出 0：22 个测试文件、322 项测试通过，lint、TypeScript 和生产构建通过。
- 最终修复补充了真正的 Go 响应 → TypeScript 解析回归：索引预览不继承完整正文的截断标记；完整正文与覆盖计数仍保留真实截断信息。Codex 解码回归同时要求恰好两个预期观测且用户内容不变。

### 安装、扫描与同步

- 已安装本地 CLI 0.4.1 候选，构建参数绑定上述代码提交；SHA-256：`bea375c3cdf6cf3e6503b533a66f20cd663e9f95b15d3d0929c57095375c502b`。
- 已安装插件 `main.js` 与最终构建哈希一致：`ce2bffb29059f5a8cbb253727334da6072ae8a363d02ea82fdb1b822d152858f`。manifest 与用户配置保留，不伪装成新 GitHub 版本。
- 回滚备份：`/Users/neomei/.local/share/session-reviewer-install-backups/visible-conversation-repair.RRtzFf`，含旧 CLI、插件、目标项目私有状态、发布日志、项目与 Vault 文档、目录记录和配置。
- 从该备份目录的 `selected-source`（目标逻辑 Session 的五份物理分段副本）限定重扫，没有扫描其他项目或 Session。
- 新 generation：`scan-4535e7a655a4f9d09395b30f55293de5`；38,063 条源记录，1 个 Session 完整、0 个问题 Session，88 个事实索引项，未解码／未投影／截断均为 0，`review_run_tokens=0`。
- 扫描发布结果中项目与 Vault 的四个对应文件全部同哈希；`sync status` 为 in_sync=1、conflicted=0、malformed=0、queued=0、blocked=0、machine_state=current。

### 真实问答遍历与原生界面

- 对新 generation 遍历全部 78 组问答：78 条用户消息、1,528 条 Agent 消息（含 66 条最终回答），共 1,606 个唯一消息修订；无重复／分页缺口，全部源记录引用、哈希、角色与阶段独立校验匹配。
- 95 次 CLI 查询，单次最大 1,169ms、最大响应 64,171 字节；2 条正文超过摘录上限但可展开，正文截断 0。查询前后公开 Markdown 文件哈希和发布指针不变；私有状态只读、追加不跨越已接受前缀另由集成回归验证。
- Obsidian 实测首页 `1–20 / 78`、中页 `21–40 / 78`、末页 `61–78 / 78`；第 61 组问答的消息末页 `61–80 / 80` 能看到最终回答及末段正文，截图确认内容可到达且没有遮挡。
- 真实项目切换 SessionReviewer → AgentWiki → SessionReviewer 成功，回到目标项目后问答重新加载；仅重载 SessionReviewer 插件后，覆盖状态、问题列表和 Agent 最终回答仍可见。
- 加载失败与重试、旧异步响应丢弃、分页期间旧问题禁用、身份与范围不符拒绝，均有插件自动化回归。未在真实 Vault 人为破坏 CLI 或源文件以制造故障。

### 保留的诚实边界

扫描完整不等于语义总结已获人工接受；Agent 回答也不等于执行已验证。查询仍保留 64 KiB 单记录上限，当前 269 条超限源记录使问答覆盖保守标记为不完整。独立检查确认它们是 210 条 item_completed、29 条 compacted、20 条 function_call_output、10 条 custom_tool_call_output，用户／Agent 可见消息为 0；不将此数冒充缺失回答，也不展示隐藏推理或原始工具输出。已排除 5 条纯上下文包装，无孤立消息。

本轮批准的扫描／展示恢复任务完成；按范围不实现跨平台迁移、旧项目迁移或新的问题脑图功能。主分支未合并、远端未推送、GitHub 未发布；本地候选验收不等于官方发布。原 main 的用户改动保留，目标 review 文件仅由此次授权扫描更新。
