# SessionReviewer

SessionReviewer 已支持一条手动、无 watcher 的完整接受链路：Go CLI 把本机 Codex session JSONL 流式转换为有界、脱敏的 evidence packet，SessionReviewer Skill 基于该 packet 生成语义 proposal，CLI 验证并 apply 到可编辑 Markdown ledger。CLI 只负责确定性提取、验证、安全写入和 accepted-ledger 恢复，不会自行生成决策、结论或项目语义。

## 要求与支持范围

- macOS 13+：Intel 与 Apple Silicon
- Windows 10 22H2+ 或 Windows 11：x64；Windows ARM 不在 v1 范围内
- 从源码构建需要 Go 1.26
- 不需要管理员权限，也不需要单独配置 OpenAI API key

仓库 CI 配置在 macOS Intel x64、macOS Apple Silicon arm64 和 Windows x64 上执行基础测试、race 检查、`vet` 与原生构建。已推送到 `origin/main` 的提交 `a09088f` 取得了三个目标的原生 CI 通过回执；本地未提交变更仍需在推送后取得新的 CI 回执。这些回执证明自动化测试、竞态检查与构建通过，但不替代 Windows 10/11 与 macOS 13+ 最低版本上的人工端到端安装验收。

## 构建、测试与用户级安装

macOS：

```bash
go test ./...
go vet ./...
mkdir -p ./bin
go build -o ./bin/session-reviewer ./cmd/session-reviewer

mkdir -p "$HOME/.local/bin"
install -m 0755 ./bin/session-reviewer "$HOME/.local/bin/session-reviewer"
export PATH="$HOME/.local/bin:$PATH"
```

Windows PowerShell：

```powershell
go test ./...
go vet ./...
New-Item -ItemType Directory -Force .\bin | Out-Null
go build -o .\bin\session-reviewer.exe .\cmd\session-reviewer

$dest = Join-Path $env:LOCALAPPDATA "SessionReviewer\bin"
New-Item -ItemType Directory -Force $dest | Out-Null
Copy-Item .\bin\session-reviewer.exe $dest
$env:Path = "$dest;$env:Path"
```

最后一行只更新当前 PowerShell 会话。需要长期从任意终端调用时，请把该用户目录加入用户级 `PATH`；无需修改系统级 `PATH`。

## 初始化项目

项目根目录和 Obsidian vault 根目录必须已经存在、彼此独立，且不能通过符号链接、junction 或 reparse point 重定向。

macOS：

```bash
mkdir -p "/path/to/project" "/path/to/vault"
cd "/path/to/project"
session-reviewer init --project . --vault "/path/to/vault"
session-reviewer init --project . --vault "/path/to/vault" --write
```

Windows PowerShell：

```powershell
New-Item -ItemType Directory -Force C:\Work\Project | Out-Null
New-Item -ItemType Directory -Force C:\Users\Me\Vault | Out-Null
Set-Location C:\Work\Project
session-reviewer.exe init --project . --vault C:\Users\Me\Vault
session-reviewer.exe init --project . --vault C:\Users\Me\Vault --write
```

第一条 `init` 命令只预览 `action`、项目 ID、ledger 和配置路径，不写入文件；第二条相同命令增加 `--write` 后才执行写入。写入前会在事务锁下重新验证预览状态，状态变化则失败并要求重新预览。`init --write` 创建稳定的项目 ID、`docs/session-review/project-overview.md` 和本机配置映射。重复执行返回同一项目 ID，不重复映射；project 与 vault 任一方向的嵌套都会被拒绝。默认本机数据目录为：

- macOS：`~/.local/share/session-reviewer/`
- Windows：`%LOCALAPPDATA%\SessionReviewer\`

可用 `--data-dir <path>` 显式覆盖。

## 准备 evidence packet

macOS：

```bash
cd "/path/to/project"
session-reviewer prepare checkpoint \
  --sessions-root "$HOME/.codex/sessions" \
  --output ./evidence.json

session-reviewer prepare review \
  --session <session-id> \
  --sessions-root "$HOME/.codex/sessions" \
  --output ./evidence-from-start.json \
  --from-start
```

Windows PowerShell：

```powershell
Set-Location C:\Work\Project
session-reviewer.exe prepare checkpoint `
  --sessions-root "$HOME\.codex\sessions" `
  --output .\evidence.json

session-reviewer.exe prepare review `
  --session <session-id> `
  --sessions-root "$HOME\.codex\sessions" `
  --output .\evidence-from-start.json `
  --from-start
```

`checkpoint` 从已接受 cursor 的下一行准备证据；`review --from-start` 忽略 cursor，从第 1 行重放。`--from-start` 只适用于 `review`。输出为 schema v2：`expected_cursor` 精确绑定 prepare 读到的 accepted cursor，`next_cursor` 精确绑定 packet 已完整消费的最后一条 JSONL 记录；正数行的两个边界都携带该行的 64 位小写 SHA-256。packet digest 是对完整 packet 的确定性紧凑 JSON 字节计算的 `sha256:<hex>`。

Codex sessions root 按以下顺序解析：`--sessions-root`，`SESSION_REVIEWER_SESSIONS_ROOT`，`$CODEX_HOME/sessions`，最后是用户目录下的 `.codex/sessions`。当未传 `--session` 时，当前 session ID 依次取 `--current-session-id`、`CODEX_THREAD_ID`、`CODEX_SESSION_ID`；全部缺失时才使用 cwd 和时间窗口保守推断。完整的 ID 优先级是 `--session` 最高，然后才是上述当前-session 来源。

重要语义：`prepare` 永远不会创建、推进、修复或提交 accepted cursor。packet 满时会返回 `has_more: true`，`next_cursor` 和兼容字段 `to_cursor` 都停在最后一个已完整消费的记录，不会跨过因 packet 已满而被拒绝的记录。`apply` 会先验证 proposal 与该 packet 的 digest、evidence 和 revision，再渲染全部目标字节并持久化 ledger/receipt，最后才以 `expected_cursor` 做 CAS 提交 `next_cursor`。

## Obsidian 双向编辑同步

初始化会保存稳定的 Vault 目标目录：`<Vault>/Projects/<项目名--项目ID前缀>/Session Review/`。项目仓库中的 `docs/session-review/` 仍是可版本控制的 durable copy；Obsidian 中的副本也是可编辑视图。两边通过本机数据目录中的 per-entity Base 做确定性三方合并，不通过模型，也不执行 Git 命令。

```bash
# 只查看将发生什么；不创建目录、不写 Base
session-reviewer sync --dry-run

# 执行 Project ↔ Obsidian 同步
session-reviewer sync

# 查看只读状态
session-reviewer sync status
session-reviewer sync status --json
```

首次 `sync` 会把项目 ledger 镜像到 Vault。之后任一侧的单边编辑会同步到另一侧；不同 Markdown 单元的两边编辑会合并，accepted human merge 的 `revision` 只递增一次。删除文件不表示删除实体，缺失副本会恢复；逻辑删除必须显式写为 `status: archived`。同一单元冲突会显示稳定 conflict ID 并保持候选内容不被静默覆盖，可用 `sync resolve --action accept_project|accept_obsidian|manual_merge` 显式收敛。成功解决冲突后，同一次命令会释放冲突写锁并立即执行完整协调；没有其他阻塞时也会刷新首页、索引和演进图。如果后续协调仍发现损坏或敏感实体，命令会报告 `E_SYNC_PARTIAL` 并返回非零退出码。当前版本提供显式同步，尚不安装后台 watcher。

### 从项目首页恢复上下文

项目重拾时的推荐路径只有一条：

1. 在项目目录运行 `session-reviewer sync`。
2. 在代码仓库打开 `docs/session-review/project-overview.md`，或在 Obsidian 打开对应 `Session Review/project-overview.md`。
3. 先看五节点 Mermaid 主线：项目目标 → 关键决策汇总 → 最近已验证里程碑 → 当前状态 → 下一步。
4. 按首页链接进入决策、待办和 Session 中文目录，再打开具体记录的“快速理解”与完整正文。
5. 在 Project 或 Obsidian 一侧修改语义 frontmatter 或普通 Markdown 章节，然后再运行 `session-reviewer sync`。

首页同时显示项目总耗时、Token 总量、按每百万 Token 公开标价计算的总成本，以及各模型 Token/成本占比。`decisions/00-目录说明.md`、`open-loops/00-目录说明.md` 和 `sessions/00-目录说明.md` 是可导航索引。

这套导航不增加新 watcher：现有 Base/Project/Vault 三方合并先接受允许的人工编辑，只有当语义同步无冲突、无损坏文档后，派生发布阶段才会根据 accepted ledger 重建首页导航、“快速理解”、三个索引和项目演进图，并把相同字节发布到 Project、Vault 和实体 Base。这些生成内容手工修改后会被恢复，不增加实体 `revision`；需要保留的信息应写入普通章节。`sync --dry-run`和 `sync status --json` 会报告派生状态及文件数，而不打印生成摘要或本机绝对路径。已有实体存在尚未接受的语义编辑时，dry-run 会成功列出语义操作并报告 `derived=deferred files=0`，表示派生导航必须等实际同步接受语义 revision 后再计算；这不是错误。

## 手动 prepare → Skill proposal → apply

SessionReviewer Skill 中选择 `review` 或 `checkpoint`；Skill 会调用随包脚本准备一个 packet，读取必要的 accepted ledger 实体与 proposal schema/invariants，然后在本机临时目录中生成 proposal。也可将已生成的文件显式 apply：

```bash
session-reviewer apply \
  --proposal /private/tmp/session-reviewer/proposal.json \
  --evidence /private/tmp/session-reviewer/evidence.json \
  --project /path/to/project
```

`apply` 成功时输出 `changed_files`、`cursor_advanced` 和 `already_applied`。如果 packet 为 `has_more: true`，必须等该次 apply 成功且 `cursor_advanced: true` 后才能 prepare 下一包；下一包的 `expected_cursor` 必须与上一包的 `next_cursor` 完全相等。只有显式要求从 session 开头复查时，第一包才可使用 `review --from-start`；后续包和 `already_applied: true` 后的重试都必须省略 `--from-start`。

packet 的 `session_usage` 从 session 起点累计到本包 `next_cursor`：包含会话起止时间、耗时、每个模型的 input/cached-input/cache-write/output/reasoning-output/total tokens 和总 tokens。Skill 必须把这些计数原样写入 session report，并为每个模型记录当前公开的 USD/百万 Token 标价、来源、日期与计算成本；订阅包含量不参与计算。CLI 会校验 usage、单价结构、逐模型成本和总成本。每次 accepted session 更新都会在 session Markdown 中写入会话统计，并在 `project-overview.md` 的 `Project accounting` 章节汇总项目总耗时、总 Token、总成本，以及各模型的 Token/成本占比；`history` 也显示同一份项目级汇总。

重复 apply 同一个已接受 proposal 会返回 `already_applied: true`，不重写 ledger 或派生图，也不改变字节、哈希或修改时间。如果在写入后、cursor CAS 前中断，receipt 会用于校验并恢复该次接受；任何中间用户编辑或边界不匹配都会失败关闭。

ledger 是可编辑 Markdown。未知 frontmatter 字段和 CLI 不拥有的自定义章节会在后续 apply 中保留；已接受的 title、status、tags 和 narrative 是后续 proposal 的当前基线，只能通过 revision/evidence 验证的显式变更更新。`docs/session-review/diagrams/project-evolution.md` 中的五节点恢复主线、因果附图和关系附图都由 accepted ledger 派生，不是独立的语义来源，不应手工编辑。

session report 形成一条可恢复的双向链。首个报告的 `previous_session_id` 和 `next_session_id` 都为空；后续新报告必须把 `previous_session_id` 指向当前 accepted 终点并保持 `next_session_id` 为空。apply 会在同一事务中自动提升上一报告的 revision 并写入互惠的 `next_session_id`，因此第二个及后续 session 的 `changed_files` 会同时包含上一份 session report。已有报告的链接不能由后续 proposal 改写。

## accepted-ledger-only 恢复

```bash
session-reviewer resume --ledger-only --project /path/to/project
session-reviewer history --ledger-only --project /path/to/project
```

`resume` 只渲染 accepted current state 与恢复信息，`history` 只渲染已接受的跨 session 历史。它们都不会读取、解释或接受 pending session evidence；如果恢复后还要纳入新证据，再进入上述 review/checkpoint 流程。

## 输出与隐私边界

- 原始 session 文件只读打开，保持在本机；CLI 不复制或修改原始 JSONL。
- evidence 采用 allowlist：保留 user/assistant 可见消息、有限的工具调用/结果元数据和工作目录变化。
- developer、system、hidden reasoning、encrypted/opaque compaction 和未知记录类型不会进入 evidence。
- 文本在持久化前进行 likely-secret 脱敏，单条摘要、事件数和最终 packet 都有上限；CLI 不把事件内容打印到 stdout/stderr。
- 脱敏覆盖常见 token、命名 secret、连接 URL、私钥和高熵候选，但它不是对任意敏感信息的形式化证明。evidence 仍应作为本地敏感项目资料保管，并在分享前检查。
- 输出使用同目录原子替换；失败时保留原输出。输出不得位于 raw sessions 或本机 data 目录内，也不能经过符号链接、junction/reparse point 或非普通文件。
- Windows 上，已存在的目标通过打开目录句柄上的 `os.Root.Rename` 替换；目标不存在时通过同一 root 的 `Link` 发布并用 `Remove` 清理临时名，因此不会覆盖同时创建的目标。不支持硬链接的文件系统会安全失败，不回退到可覆盖的新文件 rename。这是可见性与 no-clobber 合同，不是对所有文件系统的目录元数据 crash durability 承诺。
- 该 Go CLI 不发起模型或 OpenAI API 调用，也不执行 Git commit、add、push、reset、checkout、switch、restore，不修改 Git 索引、分支或 refs。`apply` 会按请求更改 ledger 工作树文件，这些是普通的未提交文件变更，不是 Git 操作。

## 常见错误与恢复

- `project is not initialized`：先在真实项目根目录运行 `init`，并确认使用同一个 `--data-dir`。
- `ambiguous current session`：使用 `--session <session-id>` 明确选择；工具不会静默猜测。
- session JSONL 损坏：已选定 session ID 时，无关损坏文件不会阻断处理；选中候选文件损坏，或同一 ID 的任一重复候选损坏，都会失败关闭。没有可靠 ID 而依赖 cwd/时间推断时，损坏候选也会阻止静默推断；请先修复文件或显式选择可验证的 session ID。
- cursor 损坏、已接受行被截断/改写，或需要完整复查：使用 `prepare review --from-start`。常规增量准备会校验 cursor 行的源哈希并在漂移时失败关闭；`--from-start` 绕过 cursor 读取与校验，且不修复它，便于后续显式恢复。
- `output path is inside a protected data root`：把 `--output` 改到项目工作目录等独立位置。
- root redirected/invalid：使用实际存在的物理目录，移除路径中的符号链接、junction 或 reparse point。
- packet 的 `has_more` 为 `true`：不要手工假定整段已接受；保留 packet，交给当前验证/apply 流程成功提交 cursor 后再准备下一段。

准备失败不会推进 cursor，也不会用半成品覆盖既有 evidence 文件。进入诊断映射的 `init`/`prepare` 运行失败使用稳定错误码和 `recovery` 操作，不把原始 session 内容、内部原因或敏感路径复制到 stdout/stderr。命令语法和用法错误仍输出普通用法文本并以状态码 2 退出，不使用该稳定诊断格式。

## 发布包与许可证状态

候选包通过 Go 标准库生成确定性的 macOS Intel、macOS Apple Silicon 和 Windows x64 归档，并生成统一 `SHA256SUMS`。每个归档包含 CLI、README 和完整的 `skill/session-reviewer` 包。源码树干净时可运行：

```bash
./scripts/build-release.sh 0.1.0 dist
```

Windows PowerShell 使用：

```powershell
.\scripts\build-release.ps1 -Version 0.1.0 -Dist dist
```

本项目使用 Apache License 2.0，版权声明为 `Copyright 2026 NeoMei and QUUKK`。tag-triggered GitHub Release workflow 会验证根目录 `LICENSE`、`NOTICE` 与 tag/commit 一致性，再构建并发布 `v0.1.0` 的确定性归档。

## 当前限制与后续模型

当前仓库已完成 deterministic prepare/apply/ledger-only recovery 和语义 session-review Skill。它尚不提供：

- 脱离 Skill proposal 的 CLI semantic conclusions 或自动总结；
- 后台 watcher；
- 独立于当前 live Base/Project/Vault 状态的历史 conflict-note 归档；
- 已通过最低版本实体机器安装验收的公开发行包。

当前 Obsidian 混合模型以 repository 内的 ledger 为 durable source，并对每个稳定实体比较 `Base`（上次成功同步）、`Project`（repository）和 `Vault`（Obsidian）。显式 `sync` 已可处理首次镜像、单边编辑和不同单元合并；后台 watcher 与完整的持久冲突解决工作流仍是后续工作。

Skill/模型用于语义 session review，并生成交给引擎验证的 proposal/apply；普通的确定性 Obsidian 同步不需要模型。该模型不是无状态、逐文件互相覆盖的镜像，也不会自动执行 Git commit、push、reset、checkout 或其他 Git 变更。
