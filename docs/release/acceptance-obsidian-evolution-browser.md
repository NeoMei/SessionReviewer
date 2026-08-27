# Obsidian 项目演进浏览器验收

> 候选版本：0.2.1
> 分支：`codex/session-reviewer-v2`
> 验收日期：2026-08-27

## 产品边界

插件以 `项目回顾.md`、`项目历史.md` 和同目录隐藏 `ledger.json` 为唯一数据源。页面默认只显示项目目标、阶段、当前状态、一个下一步和最近五个节点；旧细节通过完整历史搜索渐进展开。用量、哈希、evidence 与同步元数据不可编辑。

## 自动化验收

2026-08-27 本机门禁已执行：`go test ./... -count=1`、`go test -race ./...`（按计划跳过单个超大流式基础测试）、`go vet ./...`、`npm ci && npm run check` 全部退出 0。插件 clean-install 检查共 21 项 Vitest，并完成 TypeScript 和生产 bundle。

- Go/TypeScript 共享 fixture：合法 review/history/ledger 同时通过；重复 ID、非法 marker、未知 JSON key、非安全整数和伪造汇总被拒绝。
- 快照边界：人类 Markdown 更新后显示 `pending_edit`，但机器用量明确标为上次验收值；任一文件损坏时保留最后有效快照。
- 安全编辑：仅允许核心 allowlist 字段；基准 SHA-256 变化时拒绝 stale edit；机器字段不能写回。
- CLI 边界：绝对路径、语义版本和 review schema v2 必须验证；固定 argv 使用 `execFile`、`shell:false`；任意 shell 参数被拒绝。
- 交互：演进节点点击更新相邻详情；决策可逆向跳回节点；Tab 和时间线支持键盘导航。
- 大历史：20,000 节点模型的 DOM option 数不超过 80，保留搜索和分批导航。
- 打包：两次生成的 zip 字节与 `SHA256SUMS` 相同；归档恰好包含 `session-reviewer/main.js`、`manifest.json`、`styles.css`，无内联 sourcemap。
- 生产 bundle 静态检查：未发现本机绝对路径、内联 sourcemap、`eval` 或 `shell:true`；可执行调用保留为 `execFile` 与固定 argv。

## macOS 真实 UI 验收

- 环境：macOS，Obsidian 1.13.7，Vault 使用既有 SessionReviewer 配置映射。
- 安装与兼容性：候选 zip 的三个文件安装到 `.obsidian/plugins/session-reviewer` 并在 Obsidian 中启用；插件显示为 SessionReviewer 0.2.1。候选 CLI 安装为 `~/.local/bin/session-reviewer-v2`，保留既有 0.1.0 `session-reviewer` 不变；插件通过 `version --json` 验证 0.2.1 和 review schema 2 后保存该稳定路径。
- 默认恢复视图：打开后只显示目标、状态、当前阶段、一个下一步、五个最近演进节点及相邻详情。逐一点击五个节点后，选中标题与右侧详情标题全部一致。
- 决策与用量：从“只持久化脱敏且可定位的允许列表证据”成功逆跳到“截断后二次脱敏缺陷完成 TDD 修复并恢复 apply”。UI 显示总时长 3820 分 26 秒、573,135,757 tokens、$291.30、gpt-5.6-sol 100%，以及 2026-08-27 单价日期和官方来源；与隐藏账本一致。
- 双向编辑：在 Obsidian 编辑项目目标后，页面立即显示“等待同步到代码目录”和“机器用量仍来自上次验收”；CLI 状态可从页面刷新。真实同步后 Project/Vault 均出现验收标记并恢复 `in_sync=2`；随后从 Project 侧删除标记再同步，Obsidian 页面恢复原文。
- 冲突：Project 与 Obsidian 对同一目标写入不同验收标记后，真实 sync 生成一个隐藏冲突。插件同时展示 Base、Project、Obsidian 三份候选，选择 Project 并确认后显示“冲突已解决”；最终状态 `in_sync=2`、零冲突、零 pending，重复 dry-run 为零操作。
- 清理：临时内容标记全部移除。验收生成的两份已解决冲突记录移到可恢复备份 `/Users/neomei/.local/share/session-reviewer-v2-ui-acceptance-backups/20260827T070130Z/test-conflicts`，未留在真实 Project/Vault。
- 布局与键盘：全宽页面和已有左右分栏下的较窄内容区均可阅读和滚动。真实 Computer Use 可访问性桥接未暴露自定义时间线按钮的可观察键盘焦点，因此不把纯键盘实机项声明为通过；Tab/方向键行为仍由 21 项 Vitest 中的交互用例覆盖。

## Windows x64 真实 UI 验收

当前主机无 Windows x64 / Obsidian 运行环境，不能诚实声明原生 UI 验收已通过。Windows 的 PowerShell 打包、Node 22 插件检查、归档内容和 CLI 跨编译已纳入 CI；发布前仍需在 Windows 10 22H2+ 或 Windows 11 x64 上执行安装、页面编辑、代码侧编辑、冲突解决和 CLI 路径验证。

## 安全与隐私检查

- 生产 `main.js` 不应含本机绝对路径、内联 sourcemap、`eval` 或 shell 拼接。
- 冲突候选只从 Vault 隐藏冲突记录读取，校验 project/conflict ID 和三份候选哈希后，以 `textContent`/`pre` 显示。
- 手工合并使用 0600 临时文件，CLI 返回后在 `finally` 中删除。
