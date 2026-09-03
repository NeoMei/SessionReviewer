# SessionReviewer（中文）

SessionReviewer 默认使用零 Token 的整项目扫描：Go CLI 找出与当前项目关联的全部 Codex Session，分别生成有界、脱敏的确定性记忆，再汇总为项目级视图并同步到可编辑 Markdown。原始 Session 不会被复制，正常扫描不调用 Agent。SessionReviewer Skill 保留为按需语义调整和补齐路径，不是整项目扫描的前置条件。

## 要求与支持范围

- macOS 13+：Intel 与 Apple Silicon
- Windows 10 22H2+ 或 Windows 11：x64；Windows ARM 不在 v1 范围内
- 从源码构建需要 Go 1.26
- 不需要管理员权限，也不需要单独配置 OpenAI API key

仓库 CI 配置在 macOS Intel x64、macOS Apple Silicon arm64 和 Windows x64 上执行基础测试、race 检查、`vet` 与原生构建。`v0.2.1` 是首个包含 review schema v2 和 Obsidian 项目演进浏览器的发行版；`v0.1.0` 保留为 legacy schema 版本。自动回执不替代 Windows 10/11 与 macOS 13+ 最低版本上的人工端到端安装验收。

## 构建、测试与用户级安装

无需 Go 工具链时，从 [最新 GitHub Release](https://github.com/NeoMei/SessionReviewer/releases/latest) 下载与平台对应的归档、Obsidian 插件和 `SHA256SUMS`。CLI 归档解压后包含 CLI、README、许可证以及完整的 `skill/session-reviewer` 包。当前源码候选版本为 `0.3.5`：

- Apple Silicon Mac：`session-reviewer_0.3.5_darwin_arm64.tar.gz`
- Intel Mac：`session-reviewer_0.3.5_darwin_amd64.tar.gz`
- Windows x64：`session-reviewer_0.3.5_windows_amd64.zip`
- Obsidian：`session-reviewer-obsidian-0.3.5.zip`

macOS/Linux 终端可把四个文件放在同一目录后执行 `shasum -a 256 -c SHA256SUMS`。Windows 可用 `Get-FileHash -Algorithm SHA256` 计算归档摘要，并与 `SHA256SUMS` 中对应值比较。

以下命令用于从源码构建和安装。

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

第一条 `init` 命令只预览 `action`、项目 ID、ledger 和配置路径，不写入文件；第二条增加 `--write` 后才执行写入。全新项目创建稳定 ID、`docs/session-review/项目回顾.md`、`docs/session-review/项目历史.md`、机器所有的 `docs/session-review/.session-reviewer/ledger.json` 和本机映射。重复执行不会换 ID 或重复映射。默认本机数据目录为：

- macOS：`~/.local/share/session-reviewer/`
- Windows：`%LOCALAPPDATA%\SessionReviewer\`

可用 `--data-dir <path>` 显式覆盖。

新的项目映射以只含 mapping、不含凭证的 `projects.d/<project-id>.toml` 独立片段发布；`config.toml` 中已有的旧映射仍会一起加载，`init` 不会重写该共享文件。手工恢复时不要合并、改名或复制 fragment：文件名、内部 ID、根路径或同 ID 内容发生冲突时，加载会 fail closed。

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

# 插件/多项目场景：以稳定 ID 选择配置映射
session-reviewer sync --dry-run --project-id project-0123456789abcdef
```

`--project-id` 与 `--cwd` 互斥。它只从本机配置中选择一个稳定 ID，再固定并验证其 Project root；绝对路径不会进入 Vault Markdown 或 `ledger.json`。Windows PowerShell 使用相同参数和 `session-reviewer.exe`。

旧项目的首次 `sync --dry-run` 会显示 migration creates/archives，但不写入任何文件。真实 sync 在 Project 内 `.session-reviewer/backups` 保留内容寻址的 migration backup 和 manifest；它不会发布到 Vault，也不会自动删除。

### 安装项目演进浏览器

发布包中的 `session-reviewer-obsidian-0.3.5.zip` 只包含三个可安装文件。解压后，将整个 `session-reviewer` 目录放到：

- macOS/Linux：`<Vault>/.obsidian/plugins/session-reviewer/`
- Windows：`<Vault>\.obsidian\plugins\session-reviewer\`

目录中应当恰好有 `main.js`、`manifest.json` 和 `styles.css`。在 Obsidian 的“设置 → 第三方插件”中启用 SessionReviewer，然后从命令面板运行“SessionReviewer: 打开项目脉络”。

页面默认只呈现项目目标、阶段、一个下一步、最近五个演进节点，以及选中节点的详情。“决策”可跳回相关演进节点；“用量”显示已验收账本中的时长、Token、成本、模型占比、单价来源与日期。需要旧细节时再展开全部历史并搜索。

页面可编辑目标、阶段、状态、下一步、风险、决策和事件叙述。保存后会立即显示新的人类内容，同时标记“等待同步到代码目录”；在同步成功前，页面不会把旧的机器用量冒充为当前数据。

插件不再要求填写 SessionReviewer 或 Codex 路径。启动时只会从常用用户级安装目录和 `PATH` 自动发现并验证 SessionReviewer；旧版保存的路径只用于一次升级迁移，发现成功后即删除。若未发现，请先安装或运行一次 SessionReviewer，再重新加载插件。

插件只接受兼容 review schema v3 的语义版本 CLI，并且只会执行固定白名单动作；不会从 Markdown 读取可执行路径，也不会通过 shell 执行任意参数。看到“项目需要迁移”时先做 dry-run 预览；看到“两边修改了同一内容”时比较 Base/Project/Obsidian 三个候选再确认；看到“机器账本被改动”时，只在确认 Project 副本为权威字节后执行修复。

`项目回顾.md` 和 `项目历史.md` 可在 Project 或 Obsidian 编辑。可编辑内容包括目标、阶段、状态、下一步、风险、决策和事件叙述；ID、revision、schema、hash、evidence、用量/单价和 sync metadata 不可编辑。两边不同语义单元会自动合并；同单元冲突会生成隐藏 conflict ID，用 `accept_project`、`accept_obsidian` 或带 `--file` 的 `manual_merge` 显式收敛。

Vault 中的机器账本被修改时，普通 sync 会以 `machine_ledger_modified` 停止。确认 Project 副本为权威字节后，执行 `session-reviewer sync repair-machine-ledger --project-id <id>`；该命令不接受任意目标路径。

首次 `sync` 会把两份人类 Markdown 和隐藏机器账本发布到 Vault。之后任一侧的单边编辑会同步到另一侧；不同 Markdown 单元的两边编辑会合并，accepted human merge 的 `revision` 只递增一次。删除文件不表示删除实体，缺失副本会恢复；逻辑删除必须显式写为 `status: archived`。同一单元冲突会显示稳定 conflict ID 并保持候选内容不被静默覆盖，可用 `sync resolve --action accept_project|accept_obsidian|manual_merge` 显式收敛。成功解决后会立即再做一次完整协调；如果仍发现损坏或敏感内容，命令会报告 `E_SYNC_PARTIAL` 并返回非零退出码。当前版本提供显式同步，尚不安装后台 watcher。

### 零 Token 更新整个项目

在已初始化的项目目录运行：

```bash
session-reviewer scan --json
```

该命令扫描与项目关联的全部 Codex Session，分别更新确定性 Session 记忆，再汇总为项目级视图、生成简洁的人类可读内容并同步到 Obsidian。它不调用 Agent，结果中的 `review_run_tokens` 固定为 `0`。人类已编辑字段和未知 Markdown 章节保持最高呈现优先级；机器生成的统计区由新扫描结果刷新。

Obsidian 中的“更新项目脉络”执行同一流程，但使用可恢复的后台任务。对应的手工命令是：

```bash
session-reviewer scan start --json
session-reviewer scan status --json
```

终态分为完整成功 `completed`、扫描完成但有隔离源问题 `completed_with_issues`、失败 `failed`。同一项目已有 queued/running 任务时不会重复启动 worker。

### 恢复项目上下文

项目重拾时的推荐路径只有一条：

1. 在项目目录运行 `session-reviewer scan --json`，或在 Obsidian 点击“更新项目脉络”。
2. 已安装 Obsidian 项目脉络浏览器时优先打开它；否则先读 `项目回顾.md`。
3. 需要旧细节时再打开 `项目历史.md`；它按时间倒序保留事件流。
4. 在 Project 或 Obsidian 编辑允许的人类字段，再运行 `session-reviewer sync`。

项目总耗时、Token、公开 USD/百万 Token 标价、按模型成本与占比保存在隐藏机器账本中；订阅包含量不会减少记录成本。

`sync --dry-run`、普通 `sync status` 和 `sync status --json` 都会报告 migration、machine 和 pending 状态，不打印人类文档摘要或本机绝对路径。dry-run 列出实际 sync 将做的语义、Base 和机器账本操作，但不写入 Project、Vault 或本机状态。机器账本操作是稳定的语义计划：由真实提交时间决定的账本最终哈希不会在只读计划中预测；事务内部仍会对最终字节和哈希做精确校验。

## 手动 prepare → Skill proposal → apply

SessionReviewer Skill 中选择 `review` 或 `checkpoint`；Skill 会调用随包脚本准备一个 packet，读取必要的 accepted ledger 实体与 proposal schema/invariants，然后在本机临时目录中生成 proposal。也可将已生成的文件显式 apply：

```bash
session-reviewer apply \
  --proposal /private/tmp/session-reviewer/proposal.json \
  --evidence /private/tmp/session-reviewer/evidence.json \
  --project /path/to/project
```

`apply` 成功时输出 `changed_files`、`cursor_advanced` 和 `already_applied`。如果 packet 为 `has_more: true`，必须等该次 apply 成功且 `cursor_advanced: true` 后才能 prepare 下一包；下一包的 `expected_cursor` 必须与上一包的 `next_cursor` 完全相等。只有显式要求从 session 开头复查时，第一包才可使用 `review --from-start`；后续包和 `already_applied: true` 后的重试都必须省略 `--from-start`。

packet 的 `session_usage` 从 session 起点累计到本包 `next_cursor`：包含会话起止时间、耗时、每个模型的 input/cached-input/cache-write/output/reasoning-output/total tokens 和总 tokens。Skill 必须把这些计数原样写入 proposal，并记录当前公开的 USD/百万 Token 标价、来源、日期与计算成本；订阅包含量不参与计算。CLI 校验 usage、单价结构、逐模型成本和总成本，再把完整 session/evidence/accounting 存入隐藏 `ledger.json`。

重复 apply 同一个已接受 proposal 会返回 `already_applied: true`，不重写两份人类文档或机器账本，也不改变字节、哈希或修改时间。如果在写入后、cursor CAS 前中断，receipt 会用于校验并恢复该次接受；任何中间用户编辑或边界不匹配都会失败关闭。

两份人类 Markdown 保留未知普通章节和允许的字段编辑；隐藏 marker ID 使标题改名后仍能进行语义合并。proposal/apply 仍通过 revision/evidence 验证修改，不会把机器所有字段暴露为人类编辑面。

session report 的双向链、revision、evidence 和 accounting 保存在隐藏 `ledger.json` 中，不再作为独立 Markdown 发布。apply 会在同一事务中更新这条链以及两份人类视图；已有 session 链接不能由后续 proposal 任意改写。

### 兼容的 Agent review job

旧的 Agent 编排链仍作为兼容能力保留，但 Obsidian 的正常“更新项目脉络”不会调用它。需要 Agent 语义调整时，由用户在 Agent 中显式执行 SessionReviewer Skill，再沿用 prepare/proposal/apply 验证链。

agent 可执行文件默认 fail-closed：当前只接受经过审查并通过能力探测的 Codex `0.150.1`。固定调用会忽略用户配置与规则、禁用可关闭的外部能力、在私有只读沙箱运行，并拒绝任何观察到的工具事件。由于 Codex 保留一个不可关闭的核心执行能力，SessionReviewer 明确报告受限只读隔离，而不声称工具注册表为空。其他 Codex 版本在完成审查前保持阻止。端到端验收测试使用专用 fake agent 驱动同一编排：

```bash
go test ./test/reviewjob -count=1
```

覆盖跨 session 的 happy path、失败与重试、取消以及 kill 后的重启恢复；不支持的平台上自动跳过。

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
./scripts/build-release.sh 0.3.5 dist
```

Windows PowerShell 使用：

```powershell
.\scripts\build-release.ps1 -Version 0.3.5 -Dist dist
```

Obsidian 插件包可独立构建：

```bash
./scripts/build-obsidian-plugin.sh 0.3.5 dist
```

```powershell
.\scripts\build-obsidian-plugin.ps1 -Version 0.3.5 -Dist dist
```

两个脚本都会核对 `package.json`、`manifest.json` 与 `versions.json`，并且只打包三个安装资产。

本项目使用 Apache License 2.0，版权声明为 `Copyright 2026 NeoMei and QUUKK`。tag-triggered GitHub Release workflow 会验证根目录 `LICENSE`、`NOTICE` 与 tag/commit 一致性，再构建归档并发布 GitHub Release。发布包包含三个平台 CLI 归档、Obsidian 插件 ZIP、可供社区市场直接下载的三个独立安装文件和统一 `SHA256SUMS`。

## 当前限制与后续模型

当前仓库已完成零 Token 整项目扫描、deterministic prepare/apply/ledger-only recovery 和语义 session-review Skill。它尚不提供：

- 脱离 Skill proposal 的 CLI semantic conclusions 或自动总结；
- 后台 watcher；
- 独立于当前 live Base/Project/Vault 状态的历史 conflict-note 归档；
- macOS 13 与 Windows 10 22H2 最低版本实体机器上的人工端到端安装验收。

当前 Obsidian 混合模型以 repository 内的 review v3 为 durable source，并对每个稳定语义单元比较 `Base`（上次成功同步）、`Project`（repository）和 `Vault`（Obsidian）。显式 `sync` 已可处理首次发布、单边编辑、不同单元合并，以及使用隐藏 conflict ID 的三种显式解决动作；后台 watcher 仍是后续工作。

Skill/模型用于语义 session review，并生成交给引擎验证的 proposal/apply；普通的确定性 Obsidian 同步不需要模型。该模型不是无状态、逐文件互相覆盖的镜像，也不会自动执行 Git commit、push、reset、checkout 或其他 Git 变更。
