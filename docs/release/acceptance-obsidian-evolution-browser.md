# Obsidian 项目演进浏览器验收

> 候选版本：0.2.0
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
- 安装前读取检查：Obsidian 已运行，Vault 中尚未安装 `session-reviewer` 插件。
- 安装、启用、五节点比对、决策逆跳、用量比对、页面编辑、代码侧编辑、冲突处理、宽/窄屏与纯键盘：**待用户确认安装本地未发布候选插件后执行**。

## Windows x64 真实 UI 验收

当前主机无 Windows x64 / Obsidian 运行环境，不能诚实声明原生 UI 验收已通过。Windows 的 PowerShell 打包、Node 22 插件检查、归档内容和 CLI 跨编译已纳入 CI；发布前仍需在 Windows 10 22H2+ 或 Windows 11 x64 上执行安装、页面编辑、代码侧编辑、冲突解决和 CLI 路径验证。

## 安全与隐私检查

- 生产 `main.js` 不应含本机绝对路径、内联 sourcemap、`eval` 或 shell 拼接。
- 冲突候选只从 Vault 隐藏冲突记录读取，校验 project/conflict ID 和三份候选哈希后，以 `textContent`/`pre` 显示。
- 手工合并使用 0600 临时文件，CLI 返回后在 `finally` 中删除。
