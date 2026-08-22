# SessionReviewer

SessionReviewer 的当前基础版本把本机 Codex session JSONL 流式转换为有界、脱敏的 evidence packet，供后续 Codex Skill 做项目历史整理。CLI 只负责确定性提取和文件安全，不会自行生成决策、结论或项目语义。

## 要求与支持范围

- macOS 13+：Intel 与 Apple Silicon
- Windows 10 22H2+ 或 Windows 11：x64；Windows ARM 不在 v1 范围内
- 从源码构建需要 Go 1.26
- 不需要管理员权限，也不需要单独配置 OpenAI API key

仓库 CI 配置在 macOS Intel x64、macOS Apple Silicon arm64 和 Windows x64 上执行基础测试、race 检查、`vet` 与原生构建。当前本地验收只包含 Windows x64 交叉编译；在获得当前提交的 `windows-latest` 原生运行回执前，Windows 原生运行验证仍为待完成。即使该 CI 回执通过，本仓库也不把它表述为完整的 Windows 端到端人工验收。

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

重要语义：`prepare` 永远不会创建、推进、修复或提交 accepted cursor。packet 满时会返回 `has_more: true`，`next_cursor` 和兼容字段 `to_cursor` 都停在最后一个已完整消费的记录，不会跨过因 packet 已满而被拒绝的记录。未来的 apply 阶段只有在语义变更成功持久化后才会以 `expected_cursor` 做 CAS 并提交 `next_cursor`。

## 输出与隐私边界

- 原始 session 文件只读打开，保持在本机；CLI 不复制或修改原始 JSONL。
- evidence 采用 allowlist：保留 user/assistant 可见消息、有限的工具调用/结果元数据和工作目录变化。
- developer、system、hidden reasoning、encrypted/opaque compaction 和未知记录类型不会进入 evidence。
- 文本在持久化前进行 likely-secret 脱敏，单条摘要、事件数和最终 packet 都有上限；CLI 不把事件内容打印到 stdout/stderr。
- 脱敏覆盖常见 token、命名 secret、连接 URL、私钥和高熵候选，但它不是对任意敏感信息的形式化证明。evidence 仍应作为本地敏感项目资料保管，并在分享前检查。
- 输出使用同目录原子替换；失败时保留原输出。输出不得位于 raw sessions 或本机 data 目录内，也不能经过符号链接、junction/reparse point 或非普通文件。
- Windows 上，已存在的目标通过打开目录句柄上的 `os.Root.Rename` 替换；目标不存在时通过同一 root 的 `Link` 发布并用 `Remove` 清理临时名，因此不会覆盖同时创建的目标。不支持硬链接的文件系统会安全失败，不回退到可覆盖的新文件 rename。这是可见性与 no-clobber 合同，不是对所有文件系统的目录元数据 crash durability 承诺。
- 该 Go CLI 不发起模型或 OpenAI API 调用，也不执行 Git commit、push、reset、checkout、switch、restore，不修改 Git 索引、分支、refs 或工作树。

## 常见错误与恢复

- `project is not initialized`：先在真实项目根目录运行 `init`，并确认使用同一个 `--data-dir`。
- `ambiguous current session`：使用 `--session <session-id>` 明确选择；工具不会静默猜测。
- session JSONL 损坏：已选定 session ID 时，无关损坏文件不会阻断处理；选中候选文件损坏，或同一 ID 的任一重复候选损坏，都会失败关闭。没有可靠 ID 而依赖 cwd/时间推断时，损坏候选也会阻止静默推断；请先修复文件或显式选择可验证的 session ID。
- cursor 损坏、已接受行被截断/改写，或需要完整复查：使用 `prepare review --from-start`。常规增量准备会校验 cursor 行的源哈希并在漂移时失败关闭；`--from-start` 绕过 cursor 读取与校验，且不修复它，便于后续显式恢复。
- `output path is inside a protected data root`：把 `--output` 改到项目工作目录等独立位置。
- root redirected/invalid：使用实际存在的物理目录，移除路径中的符号链接、junction 或 reparse point。
- packet 的 `has_more` 为 `true`：不要手工假定整段已接受；保留 packet，交给未来的验证/apply 流程成功提交 cursor 后再准备下一段。

准备失败不会推进 cursor，也不会用半成品覆盖既有 evidence 文件。进入诊断映射的 `init`/`prepare` 运行失败使用稳定错误码和 `recovery` 操作，不把原始 session 内容、内部原因或敏感路径复制到 stdout/stderr。命令语法和用法错误仍输出普通用法文本并以状态码 2 退出，不使用该稳定诊断格式。

## 当前限制与后续模型

当前仓库只完成 deterministic foundation。它尚不提供：

- semantic conclusions 或自动总结；
- proposal `apply` 与完整 Markdown ledger；
- Mermaid diagram、`resume`、`history` 或语义 session-review Skill；
- deterministic `session-reviewer sync`、three-way conflict engine、后台 watcher 或发布安装包。

计划中的 Obsidian 混合模型以 repository 内的 ledger 为 canonical source。普通编辑同步由确定性本地引擎处理：用户可显式运行 `session-reviewer sync`，无模型的后台 watcher 也可同步非冲突编辑。引擎对每个稳定实体比较 `Base`（上次成功同步）、`Project`（repository）和 `Vault`（Obsidian）；单边变更可直接应用到另一边，不同字段变更可自动合并，同一字段冲突则保留两边并生成显式 conflict note。

Skill/模型用于语义 session review，并生成交给引擎验证的 proposal/apply；普通的确定性 Obsidian 同步不需要模型。该模型不是无状态、逐文件互相覆盖的镜像，也不会自动执行 Git commit、push、reset、checkout 或其他 Git 变更。
