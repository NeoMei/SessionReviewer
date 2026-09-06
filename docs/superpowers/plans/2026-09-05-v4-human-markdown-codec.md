# v4 Human Markdown Codec Implementation Plan

> Current scope amendment (2026-09-06, user approved): native cross-platform CI and real legacy-project migration are no longer delivery gates for this batch; old projects may be rescanned separately. This does not authorize deletion, automatic migration, or overwriting human content. Existing compatibility and safety tests remain. Tasks 13–16 below close the three known Minor debts and the cold-start no-runtime verification gap. Earlier status entries are historical.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 v4 的两个 Markdown 真正可读、可编辑，并在扫描、双向同步、显式升级和恢复中保留人工内容与机器证据边界。

**Architecture:** 保留独立 JSON DTO，新增带稳定字段标记的无损 Markdown codec。ledger 的 `document_projection.presentation_base` 保存上次接受态，sync store 保存共同同步祖先；字段三方合并后复用既有 publication 锁、预像、journal 和恢复路径。CLI 是机器写入权威，插件负责读取、草稿提示和受信 CLI 调用。

**Tech Stack:** Go 1.26、现有 `gopkg.in/yaml.v3`、strictjson、syncdoc、publication、memorystore；TypeScript 5.8、Obsidian、Vitest。插件将现有 lockfile 中的 `yaml@2.9.0` 从开发传递依赖提升为直接运行时依赖，随插件构建打包；不再添加其他运行时依赖。

**Spec:** `docs/superpowers/specs/2026-09-05-v4-human-markdown-codec-design.md`，用户于 2026-09-05 确认；上位设计为 `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`。

## Global Constraints

- “正文直接编辑，结构显式操作；同一人工字段只有一个权威位置。”
- 格式为 `review-markdown-v1`；`document_projection.schema_version=1`；新格式外层最低 reader/writer 为 `0.4.1`，内层 Presentation 保持 `0.4.0` 语义合同。此能力版本不等于 npm/CLI 已发布版本。
- 旧 v4 JSON 不带扩展时保持原 canonical bytes；v2/v3/旧 v4 JSON 仅显式预览确认升级，普通扫描不自动迁移。
- 两个 Markdown 和 ledger 各自上限 64 MiB；index 继续使用 65,536 项/64 MiB。沿用字段字节限额，溢出拒绝，不截断。
- 未改变字段、未改变的文档壳、自定义段落、链接和代码块保持原字节；语义比较仅规范化 CRLF/LF，不 TrimSpace。
- `HumanPresentation > deterministic ProjectView`。共同同步 Base 不等于任一端最新的 presentation_base；失败不更新两种基线。
- 扫描写四文件；人工同步写两个 Markdown + ledger，事务开始和提交前校验未改 index。成功必须验证 Project/Vault 全集合与私有绑定。
- 不新建锁/journal/恢复引擎；不把四次 rename 描述为操作系统原子快照。混合世代拒绝读取或保留已验证旧快照。
- 默认零模型调用、零 Agent 子进程；代码/提示词/链接不执行。人工自定义内容仍经过既有敏感内容检查，拒绝时保留原文件，不回显秘密。
- 无有效 ledger/私有认证时普通 Markdown 可阅读，但不可冒充正式接受态或启动结构操作。
- 本补项不实现问题归位、价格请求、AI 候选、问答分段或新的结构命令。不存在的清除/归档等入口保持不可用。
- 每任务保存 RED、GREEN、审查证据；最终完整 Go/TS、零 Token、原生 Windows/macOS 与真实 Vault 验收分别记录。任何文档提交都不是实现完成证明。

---

## 0. 执行基线、依赖与文件分工

工作目录为 `.worktrees/codex-session-index-v1`，分支 `codex/session-index-v1`。代码基线 `2753827`，设计提交 `b413452`；从当前文档提交后的 HEAD 继续，不能重置回旧版本。主工作目录的 `.superpowers/brainstorm/` 不在本计划范围。

原 Session Index 计划 Tasks 1–2 已完成局部实现与审查；Task 2 后没有重新跑整分支完整套件，不得继承 main 的 CI 当作 feature HEAD 证据。原 Task 3 被本计划 M1–M8 替代其实现部分，M9 是该路径回归门；原 Tasks 4–7 与真实 Claude/OpenCode 前置能力保持未完成。

依赖顺序：M1 → M2 → M3 → M4 → M5 → M6 → M7 → M8 → M9。这里不是九次“全部重做”：M5 复用现有四文件事务，M6 接入现有索引，M7 接入现有显式迁移入口。

问答链/旧占位节点分类属于 `2026-09-04-conversation-chain-evolution-closure.md`。本计划消费该能力的已验证结果而不实现算法：M7 可以完成“有依赖成功、缺依赖阻止”的适配测试；真实旧数据需要该能力时，必须完成该前置计划的相关任务才允许确认迁移。M9 实验 Vault 可用无旧占位节点的已接受夹具，但不能据此声称真实旧项目迁移验收通过。此依赖不阻止 M1–M6 的本地开发。

| 文件 | 职责 |
|---|---|
| `schemas/review-markdown-v1.fields.json`（新） | 单一规范字段/区域目录；Go/TS 注册表由同一夹具一致性测试约束 |
| `internal/reviewv4/document_projection.go`（新） | ledger 扩展与版本组合；不做 IO |
| `internal/reviewv4/markdown_fields.go`、`markdown_errors.go`（新） | 固定可写映射、诊断，不接受任意字段路径 |
| `internal/reviewv4/markdown_lex.go`、`markdown_document.go`、`markdown_render.go`（新） | 有界词法、frontmatter/跨度、无损渲染 |
| `internal/reviewv4/markdown_draft.go`（新） | 草稿 diff 与受信人工字段应用；不接受正式状态、不写磁盘 |
| `internal/syncdoc/v4_units.go`、`internal/sync/v4_merge.go`（新） | 字段/自定义内容单位、共同 Base 合并 |
| `internal/publication/markdown.go`（新） | 已有事务对新格式、三文件人工更新的适配 |
| `internal/presentation/render_v4.go`、`internal/contextupdate/v4.go`（新） | 普通扫描新格式计划与路由 |
| `internal/migrationv4/markdown.go`、`internal/syncproject/markdown.go`（新） | 显式格式升级、认证、服务协调 |
| `obsidian-plugin/src/data/markdown-v4.ts`、`repository-v4.ts`（新） | 相同格式只读解析与状态判别，不写 ledger |

给现有入口加小型分发；不要把整个旧 service.go 移动或顺手重构。下面的新增接口是实施目标，不是对当前实现的描述。现有 `Accepted` 仅代表公共文件合同，通过私有 store 校验后才可升级为服务接受态。

## Task 1 (M1)：冻结扩展、版本组合与字段目录

状态：实现 `259e574`、补测 `3160649`，独立审查与修复复审通过。最终定向 Go/TS 验证通过；完整 Go 套件在最后夹具空格/哈希调整前通过，最终精确 HEAD 全套验收留给 M9。共享字段子集语义小项由 M2 接续。

**Files:**
- Modify: `internal/reviewv4/types.go`、`codec.go`、`validate.go`。
- Create: `internal/reviewv4/document_projection.go`、`document_projection_test.go`、`markdown_fields.go`、`markdown_fields_test.go`、`markdown_errors.go`。
- Modify: `schemas/machine-ledger-v4.schema.json`、`obsidian-plugin/src/contracts/review-v4.ts`、`obsidian-plugin/src/data/contracts-v4.ts`、`obsidian-plugin/tests/contracts-v4.test.ts`。
- Create: `schemas/review-markdown-v1.fields.json`；`testdata/contracts/v4/markdown/` 的 `review.md`、`history.md`、`ledger.json`、`index.json`、`cases.json`；保持旧 JSON fixtures 不变。

**Interfaces:** consumes `Presentation`、`MachineLedger`、`CanonicalLedgerSHA256`；produces：

```go
type DocumentProjection struct {
    SchemaVersion int          `json:"schema_version" required:"true"`
    Format string              `json:"format" required:"true"`
    PresentationBase Presentation `json:"presentation_base" required:"true"`
}
// Add to MachineLedger and to its canonical self-digest DTO:
// DocumentProjection *DocumentProjection `json:"document_projection,omitempty"`
type FieldKey struct { Entity, Name string }
type FieldSpec struct { EntityKind, Name, Document string }
func MarkdownFieldSpecs() []FieldSpec
func MarkdownCodeOf(error) string
// MarkdownError implements error and Unwrap; no raw content in Error().
type MarkdownError struct { Code, Relative, Entity, Field string; Cause error }
```

- [x] **1. RED：锁住旧字节与新扩展身份。** 在 reviewv4 包内使用已有 `minimumPresentation()`；旧 ledger 从冻结 JSON 读取。新增用例：同一 ledger 有无扩展、内外 revision/patch 不同、扩展自摘要篡改、0.4.0/0.4.1 拼接、扩展 null、未知键；断言错误代码和旧 canonical hash 不变。

```go
func TestMarkdownFieldCatalogHasOneOwner(t *testing.T) {
    seen := map[string]bool{}
    for _, f := range MarkdownFieldSpecs() {
        key := f.EntityKind + "/" + f.Name
        if seen[key] || (f.Document != "review" && f.Document != "history") {
            t.Fatalf("invalid field ownership: %+v", f)
        }
        seen[key] = true
    }
    if len(seen) != 24 { t.Fatalf("fields=%d, want 24", len(seen)) }
}
```

- [x] **2. 跑 RED。** `go test ./internal/reviewv4 -run 'DocumentProjection|MarkdownFieldCatalog' -count=1`。预期新接口缺失或扩展被旧严格解码拒绝；不是接受无关编译错误为 RED。
- [x] **3. 实现封闭扩展与目录。** 目录逐项复制 spec §6 的 24 字段和 §5.3 的四类生成区域；注册表返回副本。canonical 自摘要 DTO 使用可省略扩展，旧字段顺序/编码不变。JSON Schema 引用现有 Presentation schema；Go/TS 都检查内外项目、generation、digest、revision、patches/baselines 深度相同；旧 floor 只适用于无扩展组合，不能全局提高或放宽版本解析。

```go
// Canonical body adds exactly this optional field; absence must not emit null.
DocumentProjection *DocumentProjection `json:"document_projection,omitempty"`
// Enforce before accepting the extension:
if l.DocumentProjection != nil &&
   (l.MinimumReaderVersion != "0.4.1" || l.MinimumWriterVersion != "0.4.1") {
    return errors.New("markdown projection requires capability 0.4.1")
}
```

- [x] **4. 固定共享夹具。** `cases.json` 每项包含 `name`、输入文件相对路径、`expected_code`（成功为空）、期望 field values；包括变更扩展却重算公共自摘要的样本，后续 M5 必须仍拒绝未经私有认证的机器更改。Go/TS 测试直接读仓库这一份；不手工维护两份 Markdown 黄金文件。对照目录完整检查 24 字段，不仅检查数量。
- [x] **5. GREEN 与提交。** `go test ./internal/reviewv4 ./internal/strictjson -count=1`；在 `obsidian-plugin` 运行 `npm test -- tests/contracts-v4.test.ts`。记录旧 fixtures 的前后哈希。仅 stage 本任务文件，提交 `feat: define v4 markdown projection contract`。

## Task 2 (M2)：实现有界标记词法与无损文档壳

状态：实现 `6b1a3d3`、有界替换修复 `d5e1066`，独立审查和修复复审通过。实现提交通过完整 Go、TS 合同 67 项和 486,336 次模糊测试；修复提交通过定向回归。当前范围不包含正式接受态、同步和插件回读，完整 HEAD 验收仍留给 M9。

**Files:** Create `internal/reviewv4/markdown_lex.go`、`markdown_document.go`、`markdown_lex_test.go`、`markdown_document_test.go`；扩充 M1 的共享 cases。

**Interfaces:** consumes M1 FieldKey/目录/错误；produces：

```go
type MarkdownBlock struct {
    Key FieldKey
    Generated bool
    Start, ValueStart, ValueEnd, End int // 原 UTF-8 字节跨度，半开区间
}
type MarkdownDocument struct { raw []byte; blocks []MarkdownBlock }
func ScanMarkdownBlocks([]byte) ([]MarkdownBlock, error)
func ParseMarkdownDocument(relative string, raw []byte) (MarkdownDocument, error)
func (d MarkdownDocument) Bytes() []byte
func (d MarkdownDocument) Fields() map[FieldKey]string
func (d MarkdownDocument) ReplaceFields(map[FieldKey]string) ([]byte, error)
```

- [x] **1. RED：保留尾换行与代码中的标记。** 下例为可直接放入同包测试的最小用例；再表驱动加入 tilde/backtick fence、缩进代码、列表/引用内文字、空值、重复/嵌套/截断标记、裸 CR、无效 UTF-8、超过 64 MiB、YAML 重复键/alias/merge/深度溢出。

```go
func TestMarkdownLexKeepsValueBytes(t *testing.T) {
    src := []byte("<!-- session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\r\n" +
        "  中文\r\n\r\n" +
        "<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\r\n")
    spans, err := ScanMarkdownBlocks(src)
    if err != nil || len(spans) != 1 { t.Fatalf("spans=%v err=%v", spans, err) }
    s := spans[0]
    if string(src[s.ValueStart:s.ValueEnd]) != "  中文\r\n" { t.Fatal("lost whitespace") }
}
```

- [x] **2. 跑 RED。** `go test ./internal/reviewv4 -run 'MarkdownLex|MarkdownDocument' -count=1`。
- [x] **3. 实现单次跨度扫描。** 行状态跟踪 fence 字符/长度、缩进与容器；只有独占顶层行的精确保留语法才识别。匹配两端 entity/name，一块值只剥离两端各一个 LF 或 CRLF，保留其他字节。记录跨度后一次顺序写出替换，不为每字段反复扫全文件。规范化比较不修改原始 Bytes。

```go
var out bytes.Buffer
cursor := 0
for _, span := range d.blocks {
    value, changed := replacements[span.Key]
    if !changed || span.Generated { continue }
    out.Write(d.raw[cursor:span.ValueStart])
    out.WriteString(value)
    cursor = span.ValueEnd
}
out.Write(d.raw[cursor:])
```

- [x] **4. 收紧文档壳。** 复用现有 yaml.v3 的 Node 检查方式，不使用宽松 map 解码；只为新格式采用 64 MiB，旧 `syncdoc.MaxDocumentBytes=4 MiB` 不全局放宽。沿用 frontmatter 1 MiB、10,000 节点/100 深度上限。frontmatter 必备键、safe ID、保留前缀和版本校验按 spec；Bytes/Fields 都返回独立副本，防止调用者修改认证输入。
- [x] **5. GREEN 与提交。** `go test ./internal/reviewv4 -run Markdown -count=1`；新增 `FuzzMarkdownBlocks` 用同一 corpus 检查无 panic、跨度单调、不越界，执行 `go test ./internal/reviewv4 -run '^$' -fuzz '^FuzzMarkdownBlocks$' -fuzztime=10s`。提交 `feat: parse v4 markdown without losing human bytes`。

## Task 3 (M3)：渲染两份正文、识别草稿与受信字段应用

状态：实现 `1c6e9fc`、修复 `72819c1`，独立审查及六项修复复审通过。实现提交通过完整 Go；修复提交通过定向 Go、TS 合同 67 项，旧 JSON 夹具未变。私有认证、同步接入与最终 HEAD 全套验收仍待后续任务。

**Files:** Create `internal/reviewv4/markdown_render.go`、`markdown_draft.go`、`markdown_render_test.go`、`markdown_draft_test.go`；Modify `internal/reviewv4/codec.go`；扩充共享 cases。

**Interfaces:** consumes M1/M2；produces：

```go
type MarkdownPair struct { Review, History []byte }
type FieldEdit struct { Key FieldKey; Before, After string }
type MarkdownDraft struct {
    Documents MarkdownPair
    Edits []FieldEdit
    Presentation Presentation
}
func RenderMarkdown(p Presentation, ledger MachineLedger, previous *MarkdownPair) (MarkdownPair, error)
func ParseMarkdownDraft(pair MarkdownPair, ledger MachineLedger) (MarkdownDraft, error)
func ApplyMarkdownEdits(base Presentation, edits []FieldEdit) (Presentation, error)
// Keep existing DecodePresentation for JSON; LoadProjection dispatches by
// validated ledger extension + complete format combination, never extension alone.
```

- [ ] **1. RED：字段应用不得篡改验证。** 使用已有 `minimumPresentation()` 与 `NeutralClosedLoop()`，不需要临时机器文件。

```go
func TestMarkdownConclusionEditPreservesVerification(t *testing.T) {
    p := minimumPresentation()
    p.Timeline = []Timeline{{ID: "m1", GenerationID: p.GenerationID,
        OccurredAt: "2026-09-05", Kind: "milestone", Title: "进展",
        DecisionIDs: []string{}, ClosedLoop: NeutralClosedLoop()}}
    before := p.Timeline[0].ClosedLoop.Verification
    next, err := ApplyMarkdownEdits(p, []FieldEdit{{
        Key: FieldKey{Entity: "milestone:m1", Name: "conclusion"}, After: "人工确认结论",
    }})
    if err != nil { t.Fatal(err) }
    if next.Timeline[0].ClosedLoop.Conclusion.Kind != ConclusionHumanConfirmed { t.Fatal("missing human provenance") }
    if !reflect.DeepEqual(before, next.Timeline[0].ClosedLoop.Verification) { t.Fatal("verification changed") }
}
```

- [ ] **2. 跑 RED。** `go test ./internal/reviewv4 -run 'MarkdownConclusion|MarkdownRender|MarkdownDraft' -count=1`。上例的 `milestone` 和日期字符串已核对现有 validator；后续新增数据也先经 validator 检验，不放宽合同迎合示例。
- [ ] **3. 实现固定映射。** 先深拷贝 Presentation 的嵌套 slices/pointers，再按每种实体用显式 switch 访问类型化字段，验证 Before 等于认证基线、拒绝外来/重复键；失败不能污染输入 baseline。结论非空变更才变为 human_confirmed；空→空无变化；禁止清空非 missing 结论/原非空后续说明。原 source refs、partial、验证状态全部保留；不能借 patch 修改结构。

```go
if edit.Key.Name == "conclusion" && edit.After != edit.Before {
    if edit.After == "" && item.ClosedLoop.Conclusion.Kind != ConclusionMissing {
        return Presentation{}, &MarkdownError{Code: "markdown_structure_edit_requires_command", Entity: edit.Key.Entity, Field: edit.Key.Name}
    }
    if edit.After != "" {
        item.ClosedLoop.Conclusion.Text = edit.After
        item.ClosedLoop.Conclusion.Kind = ConclusionHumanConfirmed
        item.ClosedLoop.Conclusion.MissingReason = nil
    }
}
```

- [ ] **4. 渲染完整正文。** 回顾放 current/decision/risk/open_loop/problem；历史放全部 timeline。回顾近期/置顶/树区域只链接，生成区从旧认证基线检验后才更新。新文档固定 H1/H2 与 stable ID 锚点；标题字段只出现一次，不能把标题值再复制成第二个可编辑标题。完整闭环显示触发、结论、执行、验证、影响/后续及 coverage；零条也显示真实空态。使用 M2 跨度修改保留旧自定义内容，不走 v3 codec。
- [ ] **5. 验证公共接受态与草稿分离。** `LoadProjection` 新分支先 DecodeLedger，再 parse 两个 Markdown、比对完整字段集合与哈希、index 和内外基线。`ParseMarkdownDraft` 只校验格式及允许差异，不宣称私有认证；调用服务必须先认证基线。对每个 24 字段做 roundtrip；原文改名后链接 ID 不变；只有纯换行风格变化不提升来源；修改 generated/name/revision/id 均拒绝。
- [ ] **5a. 修订与 patch 测试。** 每批实际人工变更只推进一次 Presentation revision；只对有既存 revision 字段且实际变化的 decision/problem 推进实体修订，不给 timeline/risk 临时发明字段。使用现有 Patch/GeneratedBaseline 哈希规则记录原值和人工覆盖，active/orphan 与嵌入基线保持相同；零变更不新增 patch、不推进 revision。先以不含修订变化的内容比较确定是否发生变更，再统一设置新修订和哈希，避免自增触发自增。
- [ ] **6. GREEN 与提交。** `go test ./internal/reviewv4 -count=1`。保留旧 JSON 测试、增加混合格式拒绝测试。提交 `feat: render and read editable v4 review documents`。

## Task 4 (M4)：接入语义单元与共同祖先三方合并

状态：实现 `e410cd2`、修复 `f677d4b`，独立审查与两项修复复审通过。最终相关 Go 包（reviewv4/syncdoc/sync）回归通过；完整仓库、发布恢复与真实 Vault 验收尚待后续任务。通用扫描入口不推定 v4 信任，M5 先认证私有基线，再显式调用 v4 文档及敏感内容适配。

**Files:** Create `internal/syncdoc/v4_units.go`、`v4_units_test.go`、`internal/sync/v4_merge.go`、`v4_merge_test.go`；Modify `internal/syncdoc/document.go`、`scan.go`、`internal/sync/merge.go`（只接分发与共享单位算法）。

**Interfaces:** consumes MarkdownPair/MarkdownDraft；produces：

```go
// Explicit v4 parse carries the authenticated structural baseline.
func ParseV4(relative string, content []byte, ledger reviewv4.MachineLedger) (syncdoc.Document, error)
// In package sync, operate on human-only units, not machine frontmatter.
type V4MergeInput struct { Base, Project, Vault syncdoc.UnitSet; HasBase bool }
type V4MergeResult struct { Units syncdoc.UnitSet; Conflicts []UnitConflict }
func MergeV4Units(V4MergeInput) V4MergeResult
```

- [ ] **1. RED：不同字段可合并，同字段冲突。** 测试定义如下，复用既有 UnitSet 和 UnitConflict，不造第二种冲突 wire。

```go
func TestV4MergeUsesCommonAncestor(t *testing.T) {
    key := syncdoc.UnitKey{Kind: syncdoc.UnitSection, Name: "session-reviewer/v4/project-overview/goal"}
    set := func(v string) syncdoc.UnitSet { return syncdoc.UnitSet{key: {Present: true, Value: []byte(v)}} }
    got := MergeV4Units(V4MergeInput{Base: set("base"), Project: set("project"), Vault: set("vault"), HasBase: true})
    if len(got.Conflicts) != 1 { t.Fatalf("conflicts=%v", got.Conflicts) }
    same := MergeV4Units(V4MergeInput{Base: set("base"), Project: set("same"), Vault: set("same"), HasBase: true})
    if len(same.Conflicts) != 0 || string(same.Units[key].Value) != "same" { t.Fatal("equal edits did not converge") }
}
```

- [ ] **2. 跑 RED。** `go test ./internal/syncdoc ./internal/sync -run 'V4.*(Merge|Units|Shell)' -count=1`。
- [ ] **3. 实现 v4 文档状态。** 字段 unit key 为 `session-reviewer/v4/<entity>/<field>`；机器元数据/generated 不进入可写 UnitSet。自定义段落/frontmatter 复用既有 shell 语义单元，维护原始跨度；同一 shell 区双侧变化不使用整模板覆盖。添加/删掉正式 field unit 报格式错。v4 sensitive scan 只屏蔽已解析认证的机器 marker ID，代码样例与人工内容仍扫描。

```go
func MergeV4Units(in V4MergeInput) V4MergeResult {
    if in.HasBase {
        units, conflicts := mergeUnitSets(in.Base, in.Project, in.Vault)
        return V4MergeResult{Units: units, Conflicts: conflicts}
    }
    out := V4MergeResult{Units: syncdoc.UnitSet{}, Conflicts: []UnitConflict{}}
    for _, key := range sortedUnitKeys(in.Project, in.Vault) {
        p, v := in.Project[key], in.Vault[key]
        if p.Present != v.Present || !bytes.Equal(p.Value, v.Value) ||
           !bytes.Equal(p.KeyPresentation, v.KeyPresentation) ||
           !bytes.Equal(p.HeadingPresentation, v.HeadingPresentation) {
            out.Conflicts = append(out.Conflicts, UnitConflict{Key: key, Project: cloneUnit(p), Vault: cloneUnit(v)})
        } else if p.Present { out.Units[key] = cloneUnit(p) }
    }
    return out
}
```

- [ ] **4. 验证首次同步。** 独立测试缺项、单侧自定义新增、两端不同和两端全同；仅双方同值可以自动建立共同 Base，单侧差异进入初始化预览。比较前 Value 只做 CRLF/LF 语义规范化，重建仍使用原 physical spans。两端分别用自身认证接受态检查结构，再用共同 Base 合并人工单位，不能用新 Project revision 作为共同祖先。
- [ ] **5. GREEN 与提交。** `go test ./internal/syncdoc ./internal/sync -count=1`；追加 5 MiB 合法新格式文档对照旧格式上限、CRLF/混合换行/段落重排/marker 注入回归。提交 `feat: merge v4 human fields against the sync ancestor`。

## Task 5 (M5)：私有认证与人工三文件事务接入

状态：实现 `253118e`、修复 `aac65b1`，独立审查和修复复审通过。相关包与竞态检查通过；覆盖三文件17个崩溃检查点、同世代四文件指针前后恢复、真实锁等待后的编辑计划、无写入预览和私有凭据拒绝。新世代指针已推进但接受凭据缺失的混合状态明确拒绝读取，不声称健康回滚。接受校验未沿用调用方 context 的小项留待最终整分支审查。CLI/扫描/迁移/插件接入及最终整链验收仍待 M6–M9。

**Files:** Create `internal/syncproject/markdown.go`、`markdown_test.go`、`internal/publication/markdown.go`、`markdown_test.go`；Modify `internal/syncproject/service.go`、`internal/publication/service.go`、`journal.go`、`internal/sync/service.go`；测试扩展 `internal/publication/recovery_test.go`。

**Interfaces:** consumes `LoadProjection`、M4、既有 `memorystore.Store.LoadPublished/LoadObject`、`presentation.RenderPlan` 和 publication lock；produces：

```go
// In syncproject. Pure verification receives already loaded files/manifest.
func VerifyMarkdownBinding(a reviewv4.Accepted, manifest memory.GenerationManifest) error
type MarkdownSyncPlan struct {
    Plan presentation.RenderPlan
    Index []byte
    ExpectedGenerationID string
    ExpectedIndexDigest string
}
type MarkdownPublisher func(context.Context, MarkdownSyncPlan, *publicationlock.Owner) error
// Add optional PublishMarkdown MarkdownPublisher to syncproject.Options.
func RunMarkdown(context.Context, Options) (syncengine.Report, error)
// Publication entrypoint, retaining existing pin/owner validation:
func PublishMarkdownEditLocked(ctx context.Context, opts Options,
    edit syncproject.MarkdownSyncPlan, owner *publicationlock.Owner) (Result, error)
```

`syncproject` 不 import `publication`（后者已 import 前者）；像现有 `MigrationOptions.Publish` 一样由 CLI/上层注入发布 callback。不得添加 import cycle，亦不得因为 callback 可注入就跳过 owner/pin/预像校验。

- [ ] **1. RED：未绑定 index 不可接受。** 用常量最小对象即可覆盖这一认证入口；成功路径从已有 migration/publication fixtures 建立真正私有 store，不用 mock 的字符串当认证。

```go
func TestMarkdownBindingRejectsLoosePublicIndex(t *testing.T) {
    err := VerifyMarkdownBinding(reviewv4.Accepted{}, memory.GenerationManifest{})
    if err == nil { t.Fatal("accepted public files without a private binding") }
}
```

- [ ] **2. 跑 RED。** `go test ./internal/syncproject ./internal/publication -run 'Markdown.*(Binding|Edit|Recovery)' -count=1`。
- [ ] **3. 实现认证与锁内计划。** 从 pin 读取四文件、LoadPublished 验证 manifest/dependencies，检查 generation/ProjectView/index digest 及对象规范字节；只通过公共自摘要不够。从既有 sync store 取共同 Base 和已接受机器文档凭据，拒绝自行重算 self-hash 的账本修改。缺少既有凭据走显式初始化预览，不把未经接受的 ledger 当历史权威。解析草稿→M4 合并→M3 应用，失败无发布；锁内重读精确两端预像，避免锁外编辑丢失。`RunMarkdown` 在真实写入时要求 Options.PublishMarkdown 非 nil，dry-run 则只返回计划/冲突报告，不能调用 callback。

```go
if manifest.SessionIndexDigest == "" ||
   manifest.GenerationID != a.Review.GenerationID ||
   manifest.ProjectViewDigest != a.Review.ProjectViewDigest ||
   manifest.SessionIndexDigest != a.SessionIndex.Digest {
    return errors.New("markdown projection has no matching private index binding")
}
```

- [ ] **4. 扩展既有 journal 模式。** 人工更新明确记录三个写入文件 + index guard（精确 hash、digest、generation）；保留旧 journal 读取兼容，恢复同样检查 guard。不能调用逐实体 legacy Reconcile 写完 review 才写 history。共同 Base、presentation_base、接受 revision 只在全套字节验证后进入成功状态；恢复中若用户修改草稿则保留并报冲突，不执行旧 rollback 覆盖。
- [ ] **5. 故障矩阵。** 每个 Project/Vault 文件写入后、Vault 同步前后、guard 检查前后、公共验证后、接受指针前后注入故障；断言重新打开时一致接受或明确拒绝，index 原字节不变、generation 不变。新增并发等待锁期间编辑、人工更新时 index 改变、丢失 ledger、单侧机器 ledger 重算摘要测试。用已有 checkpoint seam 扩展枚举，不 sleep 猜时间。
- [ ] **6. GREEN 与提交。** `go test ./internal/syncproject ./internal/publication ./internal/sync -count=1`；`go test -race ./internal/publication ./internal/syncproject -count=1`。提交 `feat: publish v4 human edits through the guarded transaction`。

## Task 6 (M6)：接入普通扫描的四文件渲染

状态：实现 `a9bd8e4`、修复 `a7a62da`，独立审查及五项重要修复复审通过。相关六包回归与 Gate B 通过，覆盖 154→155 累积 Session、人工回顾/历史结论→扫描→同步→重开、精确预像、部分初始发布恢复、已认证费用保留及旧格式边界。取消传播小项留最终整分支审查；CLI 迁移/插件/完整仓库和真实 Vault 验收仍待 M7–M9。

**Files:** Create `internal/presentation/render_v4.go`、`render_v4_test.go`、`internal/contextupdate/v4.go`、`v4_test.go`；Modify `internal/contextupdate/service.go`；仅必要时修改 `internal/publication/service.go` 与 `internal/cli/scan_test.go`。

**Interfaces:** consumes 累积 `sessionindex.Document`、认证 M5 读取结果和 M3 codec；produces：

```go
// In presentation, ExpectedFiles contains exact Project preimages.
type V4RenderInput struct {
    Presentation reviewv4.Presentation
    Ledger reviewv4.MachineLedger
    Index sessionindex.Document
    Previous *reviewv4.MarkdownPair
    ExpectedFiles map[string][]byte
}
func RenderV4(V4RenderInput) (RenderPlan, error)
```

- [ ] **1. RED：无认证基线不能渲染普通更新。** 新建项目允许明确的无旧文件路径；任何“有旧文件但 ledger 缺失”不能走新建逻辑。

```go
func TestRenderV4RejectsMissingBaselineForExistingDocuments(t *testing.T) {
    _, err := RenderV4(V4RenderInput{ExpectedFiles: map[string][]byte{
        "docs/session-review/项目回顾.md": []byte("保留人工原文"),
    }})
    if err == nil { t.Fatal("render would replace unauthenticated human content") }
}
```

- [ ] **2. 跑 RED。** `go test ./internal/presentation ./internal/contextupdate -run 'RenderV4|V4Scan' -count=1`。
- [ ] **3. 渲染按依赖顺序算哈希。** 先合并人工值、产生 next Presentation；render review/history，更新两份精确 SHA；装入相同的 presentation_base；附 index digest；最后 RenderLedger 自摘要，再 LoadProjection + M5 绑定验证。保留已有 Patches/Baselines 适配，不能把 v4 特有 graph/chain/pricing 丢回 v3 类型。

```go
pair, err := reviewv4.RenderMarkdown(in.Presentation, in.Ledger, in.Previous)
if err != nil { return RenderPlan{}, err }
// Hash pair, bind the unchanged typed Presentation and canonical index,
// then use reviewv4.RenderLedger. File order is fixed:
paths := []string{
    "docs/session-review/项目回顾.md", "docs/session-review/项目历史.md",
    "docs/session-review/.session-reviewer/ledger.json",
    "docs/session-review/.session-reviewer/session-index.json",
}
```

- [ ] **4. 接入格式路由。** 新建项目和已迁移 Markdown 项目使用新路径；v2/v3 保留既有行为/迁移提示，不在 scan 自动升级；旧 v4 JSON 提示格式升级。普通扫描先读取与合并草稿，按 M5 锁内预像复核后复用 Publish；facts 更新与人工字段优先规则分开，不重建人类父子关系，不新增候选模型调用。
- [ ] **5. 测试真实链路。** 用现有 Codex fixture 跑 scan→改 review goal/history conclusion→scan→sync→重开；总 Session 数超过最近列表长度仍完整。增加相同输入但 Now 前进的重复扫描、既有 154 Session cumulative fixture、人工孤儿字段与旧 pricing/chain/graph 保留断言。新生成机器内容不误报 readonly edit，真正手改生成区则停止发布。
- [ ] **6. GREEN 与提交。** `go test ./internal/presentation ./internal/contextupdate ./internal/publication ./internal/sessionindex -count=1`；`go test ./test/zerotoken -run '^TestGateB' -count=1`。提交 `feat: publish editable markdown on the v4 scan path`。

## Task 7 (M7)：显式升级与旧私有 index 认证衔接

状态：实现 `b411e51`，修复 `e6166d5`、`139aede`、`6edc664`，独立审查及三轮限范围复审通过。实际 CLI 预览/确认/恢复、旧 v4 同世代与缺索引绑定迁移、历史重复字段裁决、六类私有证据变化拒绝已有测试。相关五包及最后受影响两包回归通过；v2/v3 缺已认证分类/因果链的生产迁移仍明确阻止，不宣称真实旧项目已完成迁移。

**Files:** Create `internal/migrationv4/markdown.go`、`markdown_test.go`；Modify `internal/migrationv4/types.go`、`plan.go`、`internal/syncproject/migration.go`、`internal/cli/sync.go`；各自相邻测试。既有 `internal/migrationv4/migrate.go` 和 `internal/memorystore/store.go` 是复用接口，仅发现具体缺陷时修改；已有 JSON BuildPreview 和 durable AdvancePrepared/图校验不要求为新格式增加无效改动或旁路。

**Interfaces:** consumes M3 render、M5 publisher/认证、既有 `MigrationPreview`/`Input`/`Result`；produces：

```go
// In migrationv4. Reconstruction is an already verified deterministic result,
// never a callback that starts a model or a boolean claiming evidence exists.
type MarkdownMigrationInput struct {
    Source Input
    Reconstructed *reviewv4.Presentation
}
func BuildMarkdownPreview(MarkdownMigrationInput) (Result, error)
// Extend MigrationPreview with source_format, target_format,
// preserved_custom_hashes (map[string]string), blocking_reasons ([]string).
// Include every new field in PreviewDigest; old preview canonical encoding
// remains supported, but cannot authorize a new target format.
```

上述 preview 字段的 Go 名称为 `SourceFormat string`、`TargetFormat string`、`PreservedCustomHashes map[string]string`、`BlockingReasons []string`，JSON tags 为对应 snake_case 且 `omitempty`；新目标必须显式包含前两项，旧 preview 序列化省略四项保持兼容。验证器分别检查 informative-blocked preview 与可发布 preview，不让 blocked preview 通过普通 Result 接受校验。

- [ ] **1. RED：无完整源不能预览确认。** 再增加读取四种版本的表驱动夹具：v2、v3、旧 v4 JSON、新 Markdown；旧四文件缺项在 SourceHashes 中显式为 absent。

```go
func TestMarkdownMigrationRejectsUnauthenticatedSource(t *testing.T) {
    _, err := BuildMarkdownPreview(MarkdownMigrationInput{Source: Input{
        Review: []byte("只有问题文本，没有接受账本"),
    }})
    if err == nil { t.Fatal("invented migration baseline") }
}
```

- [ ] **2. 跑 RED。** `go test ./internal/migrationv4 ./internal/syncproject ./internal/cli -run 'Markdown.*Migration|Migration.*Markdown' -count=1`。
- [ ] **3. 无损内存升级。** 先完整验证旧 parser 与人工 patch，再构建类型化源；v2 的中间转换只能在内存。旧 v4 JSON 解码仍保留所有字段；history 文本未知部分留在历史保留段。相同重复字段归一、可证明单侧人工修改保留、无证据不同值报迁移冲突。Reconstructed 必须与认证 project/generation/dependencies 匹配；需要分类/chain 而未提供时返回带 blocking_reasons 的不可确认预览，绝不用空数组顶替。

```go
if len(result.Preview.BlockingReasons) != 0 {
    // Return the informative preview, but no publishable four-file Result.
    result.Preview.TargetHashes = ArtifactHashes{}
    result.Preview.PreviewDigest = MigrationPreviewDigest(result.Preview)
    return result, nil
}
```

`RunMigration` 的 confirm 分支必须先检查 `len(plan.Preview.BlockingReasons) == 0` 和四个 target hashes 全部存在，再比较 digest、检查 publisher；即使用户提交了 blocked preview 的正确 digest，也必须拒绝发布。

- [ ] **4. 确认才建立私有绑定。** 若旧 manifest 缺 SessionIndexDigest，dry-run 用当前认证依赖和 cumulative builder 在内存生成目标 index/manifest，不保存对象、不 PrepareGeneration。确认在同一现有锁下重新生成相同 preview，写不可变对象并用已存在 prepared-generation 衔接更新机制提交认证目标，再交既有 publication。不得改写已接受 immutable manifest 或建立无认证旁路指针；失败恢复上一 published 世代。目标 generation 若因新增认证事实变化，必须写进 preview，不能谎称只是正文 revision。
- [ ] **5. 无写入与 stale 验证。** 从 `newMigrationServiceFixture`/`seedMigrationPreparedGeneration` 扩展夹具；在 dry-run 前后比较 Project/Vault/private 全树文件清单、内容和 prepared/published 指针。不能只比较四个公共文件；不得创建新的持久锁文件作为 dry-run 副作用。dry-run 从不调用有创建副作用的 Acquire/Prepare/repair/recovery：只读现有对象，构建前后重新验证全套精确哈希及指针；观察到变化则返回 stale，不循环重试。confirm 才使用既有锁内重建路径。修改正文、index、依赖或目标预像后用旧 preview confirm 必须失败。模拟确认过程中崩溃，恢复后 M5 私有绑定可验证。
- [ ] **6. GREEN 与提交。** `go test ./internal/migrationv4 ./internal/memorystore ./internal/syncproject ./internal/publication ./internal/cli -count=1`。提交 `feat: migrate legacy projections to authenticated markdown`。保留缺分类能力的明确状态，不把迁移适配测试成功当作实际旧项目完成迁移。

## Task 8 (M8)：Go/TypeScript 格式一致与插件草稿/降级状态

状态：实现 `8117d27`、修复 `327f4a0`，独立审查及一轮限范围复审通过。插件完整检查 186 项测试、lint、构建通过，Go 共享样本通过；覆盖原生文档入口、草稿/只读/过期状态、四文件监听、旧编辑器拒绝及 Go/TS 基线与深树边界一致性。文件读取失败诊断的小项留最终整分支审查。真实 Vault、私有认证实机与原生 CI 仍待 M9/最终验收。

**Files:** Create `obsidian-plugin/src/data/markdown-v4.ts`、`repository-v4.ts`、`obsidian-plugin/tests/markdown-v4.test.ts`、`repository-v4.test.ts`；Modify `obsidian-plugin/src/data/repository.ts`、`editor.ts`、`vault-port.ts`、`obsidian-plugin/src/view/presentation.ts`、`obsidian-plugin/src/main.ts`（仅路由/状态）、`obsidian-plugin/package.json`、`package-lock.json`；Create `internal/reviewv4/markdown_corpus_test.go`。

**Interfaces:** consumes M1 共享目录/cases + M2/M3 语法；produces：

```ts
import type { ReviewPresentationV4, MachineLedgerV4, SessionIndexV1 } from "../contracts/review-v4";
export interface MarkdownPair { review: string; history: string }
export interface MarkdownRead {
  presentation: ReviewPresentationV4;
  changedFields: readonly { entity: string; name: string }[];
}
export function parseMarkdownV4(pair: MarkdownPair, ledger: MachineLedgerV4): MarkdownRead;
export type MarkdownSnapshot =
  | { kind: "public_valid"; value: MarkdownRead; index: SessionIndexV1 }
  | { kind: "pending_edit"; value: MarkdownRead }
  | { kind: "unverified"; reason: "cli_unavailable" | "baseline_missing" | "private_binding_unavailable" }
  | { kind: "invalid"; code: string };
```

`public_valid` 仅是公共文件校验，不等于 CLI 私有接受证明。没有现成认证状态 API 时显示只读“待验证”；不得通过新造隐藏写入或把 public_valid 改名 ready 来绕过。新状态保留 v4 原始类型；旧 v3 BrowserModel 不承接 v4 闭环/图/价格字段，不做降级转换。

- [ ] **1. RED：TS 使用同一非法样本。** cases 文件数据 shape 已在 M1 定义；遍历每个共享 corpus 比较字段值和错误码，node 测试读取使用 `new URL("../../testdata/contracts/v4/markdown/", import.meta.url)` 时需以测试文件所在目录精确计算：从 `obsidian-plugin/tests` 到根 testdata 为 `../../testdata`。输入 ledger 调用既有 `parseMachineLedgerV4`，禁止 `as MachineLedgerV4` 绕过。

```ts
import { readFileSync } from "node:fs";
import { parseMachineLedgerV4 } from "../src/data/contracts-v4";
import { parseMarkdownV4 } from "../src/data/markdown-v4";
it("does not promote a local edit to an accepted machine state", () => {
  const root = new URL("../../testdata/contracts/v4/markdown/", import.meta.url);
  const ledger = parseMachineLedgerV4(readFileSync(new URL("ledger.json", root), "utf8"));
  const pair = {
    review: readFileSync(new URL("review.md", root), "utf8"),
    history: readFileSync(new URL("history.md", root), "utf8")
  };
  expect(pair.review.split("项目目标夹具")).toHaveLength(2);
  const before = structuredClone(ledger);
  const result = parseMarkdownV4({ ...pair, review: pair.review.replace("项目目标夹具", "人工编辑目标") }, ledger);
  expect(result.changedFields).toContainEqual({ entity: "project-overview", name: "goal" });
  expect(ledger).toEqual(before);
});
```

测试局部 `ledger`、`pair` 在每例由共享 `ledger.json`（严格 parse）和 `review.md/history.md` 读取；M1 的 goal 固定为“项目目标夹具”，替换前断言仅出现一次，避免没有真正编辑的假绿。

- [ ] **2. 跑 RED。** 在 `obsidian-plugin`：`npm test -- tests/markdown-v4.test.ts tests/repository-v4.test.ts`。
- [ ] **3. 实现只读解析与渲染状态。** 在插件目录 `npm install --save-exact yaml@2.9.0`（只在实施时执行），核查锁文件 delta 仅为该依赖树归属；采用 `parseAllDocuments` 严格 AST，不用 Obsidian 宽松缓存代替认证。TS 按 UTF-8 字节计限、保持原字符串/换行、相同 marker/fence/YAML 限制。schema4 路由到独立 v4 repository，监听四文件，短暂混合世代展示先前校验快照并标明过期；没有旧快照显示拒绝原因。

```ts
import { parseAllDocuments } from "yaml";
const docs = parseAllDocuments(frontmatter, {
  strict: true, uniqueKeys: true, keepSourceTokens: true, prettyErrors: false
});
if (docs.length !== 1 || docs[0]!.errors.length !== 0) {
  throw new Error("markdown_format_invalid");
}
```

在 AST 上拒绝 Alias、`<<` 合并键、重复 key；限制 10,000 节点/100 深度与 frontmatter 1 MiB；保留用户字段的源码，不 stringify。保留身份字段按 Go 的标量合同校验，数字必须 safe integer；不以 JS 自动类型转换解释 ID。解析选项与 AST 能力依据 [yaml 官方文档](https://eemeli.org/yaml/#parse-options)；2.9.0 版本来自当前项目锁文件，不宣称为最新版本。

```ts
if (read.changedFields.length > 0) return { kind: "pending_edit", value: read };
// After exact public hashes and snapshot bindings pass only:
return { kind: "public_valid", value: read, index };
```

- [ ] **4. 接入现有 UI 的最小边界。** 从 repository 分发 v4 状态；能打开两份 Markdown、展示“有未同步修改”/只读证据/缺 CLI 原因。完整五 Tab 与问题树视觉由原前端计划接入，不在此重做。现有 editor 不得将 v4 文档送进 v3 写入器；对无 v4 支持的按钮禁用并说明，通过 Obsidian 原生正文编辑可完成白名单文本修改。源码/阅读模式都保留稳定锚点。结构按钮只有已有受信 CLI 支持时才可用。
- [ ] **5. GREEN 与提交。** `go test ./internal/reviewv4 -run MarkdownCorpus -count=1`；插件 `npm run check`。共享 fixtures 全覆盖同一语义/错误码，测试无 `vault.modify`/ledger 写入。提交 `feat: read v4 markdown drafts safely in Obsidian`。

## Task 9 (M9)：整链回归、真实 Vault 验收与原 Task 3 交接

**Files:** Create `test/zerotoken/markdown_v4_test.go`、`docs/verification/2026-09-05-v4-human-markdown.md`；Modify `test/zerotoken/gate_b_test.go`、`.github/workflows/ci.yml`（仅补原矩阵中的测试选择）、本计划和原 Session Index 计划状态。

**Interfaces:** consumes M1–M8 已验证入口；produces 实际 HEAD 绑定的验收报告，不增加产品 API。

- [ ] **1. 写整链失败断言。** 复用 Gate B 的私有 store/Project/Vault fixture，增加 `TestMarkdownV4EndToEnd`；预置 >15 条已接受里程碑，并扫描 154 个 Session 夹具，人工编辑 goal/decision rationale/history conclusion/custom 内容，跑 scan→sync→重开；断言四文件精确绑定、人工保留、结论来源更新但 verification/graph 不变。里程碑为经校验的测试接受态，不声称此处已实现问答链的自动晋升算法。夹具初始化沿用 gate_b_test.go，不通过真实用户 Vault 获得单测数据。

```go
// Assertions inside the existing Gate B harness after edit + scan + sync:
if accepted.Review.CurrentState.Goal != "人工目标" { t.Fatal("lost human edit") }
if len(accepted.SessionIndex.Sessions) != 154 { t.Fatal("truncated cumulative index") }
if reviewRunTokens != 0 || agentProcesses != 0 { t.Fatal("deterministic path invoked an agent") }
```

这段断言的 `accepted` 是 `reviewv4.LoadProjection` 结果且已经 M5 验证；`reviewRunTokens` 从 contextupdate.Result 读取；`agentProcesses` 从现有零 Token harness 的拒绝式进程记录计数，不由业务代码自报。

- [ ] **2. 跑聚焦测试。** `go test ./test/zerotoken -run 'MarkdownV4|GateB' -count=1`；同输入第二次逐字节比较和 Now 前进用例同时通过。Gate A 若新增 import/capability 仅逐项审查真实差异，禁止盲目重生成基线消除失败。
- [ ] **3. 稳定 HEAD 完整验证。** 依次执行以下命令，原样记录退出码；不可把早先版本结果拼成当前 HEAD 全绿。

```sh
go test ./...
go vet ./...
go mod tidy -diff
go test ./test/zerotoken -run '^TestGateAZeroTokenCore$' -count=1
go test -race -timeout 30m ./... -skip '^TestFoundationLargeSessionReachesBoundedPacketAfterStreamingPast20MiB$'
git diff --check
```

在 `obsidian-plugin` 执行 `npm run check`。race 的既有大 Session 排除项不代表该用例免测，第一条完整 `go test ./...` 必须包含它。原生 Windows/macOS 验证使用现有 CI 矩阵；未获 push/PR 权限时记录“未执行”，不能以交叉编译替代。

- [ ] **4. 真实 Obsidian 验收。** 用独立实验 Vault 和可恢复备份，不替换用户正式安装或正式账本。打开 >15 条完整历史；在原生编辑器改目标、理由、结论、自定义段落，确认只有一个权威位置；执行本地 CLI sync/scan 后关闭重开。制造同字段双侧冲突，验证双方文本仍在；手改机器区拒绝同步；模拟无 CLI 后仍可阅读且结构/接受操作禁用。记录截图、文件哈希、命令与重开结果。实际旧项目迁移需要前置分类能力时单列未完成，不用实验夹具掩盖。
- [ ] **5. 完成报告与交接。** 报告分列：实现/本地全套/原生 CI/实验 Vault/真实旧数据迁移/GitHub 状态。只有 M1–M7 的可执行集成和 M9 本地回归通过，才给原 Task 3 写“实现与本地回归完成”；可交付需 spec 全部验收（包括 M8、真实 Vault 与所需迁移能力）。未满足项保留开放；然后继续原 Tasks 4–7，不自动推送、合并、发版。
- [ ] **6. 提交测试和报告。** 精确 stage 本任务文件，提交 `test: verify v4 markdown editing and recovery end to end`；报告的后续 CI/UI 证据补充另作文档提交，不改写旧结果。

## 覆盖自审与执行交接

| spec 要求 | 实施任务 |
|---|---|
| §1–4 权威分工、单正文、ledger 基线 | M1、M3、M5 |
| §5 marker/frontmatter/跨度/只读区 | M1–M3、M8 |
| §6 24 字段及闭环来源规则 | M1、M3、M8 |
| §7 草稿/共同 Base/孤儿/修订幂等 | M3–M6、M9 |
| §8 四文件/三文件事务和恢复 | M5、M6、M9 |
| §9 四种格式显式升级、私有绑定 | M1、M7、M9 |
| §10 有界解析、敏感内容和错误码 | M1、M2、M4、M8、M9 |
| §11 模块分工与 Go/TS 一致性 | M1–M8 |
| §12.1–6 读取/编辑/冲突/唯一位置 | M2–M6、M8、M9 |
| §12.7–11 迁移/认证/故障/幂等 | M5–M7、M9 |
| §12.12–13 平台与真实 Vault | M8、M9 |

本计划不把代码片段中的接口当现成实现。所有测试例子需使用真实当前合同值，新增夹具必须先经现有 validators 验证，禁止放宽验证器使测试变绿。M7 的外部分类能力、M9 的原生 CI/真实 Vault 是明确检查点；M8 的 YAML 依赖已指定现有锁定版本，实施时检查打包结果与许可。发现需要扩大合同或授权时报告具体差异。

用户于 2026-09-05 授权开始执行，采用逐任务子代理实现与独立审查。从 M1 开始，每任务 RED→实现→GREEN→审查→提交；状态与证据保存在本计划专属 SDD ledger。此处记录启动，不代表任何任务实现/测试已通过。

## 2026-09-06 用户确认的纠偏批次

用户确认修复四项阻塞后完整回归；基线 `9bf9565`，不合并、不推送、不发布。M1–M8 不重做，M9 仍开放。本批依次执行 Task 10–12，随后重新执行 M9 稳定 HEAD 全套与实验 Vault 状态验收。此前三个 Minor 保留在最终审查中，不当作已解决。

## Task 10：恢复值后的跨世代编辑与历史列表基线

**Files:** Modify `internal/contextupdate/v4.go`、`internal/reviewv4/markdown_render.go` 及相邻测试；Test `test/zerotoken/gate_b_test.go`、`obsidian-plugin/tests/markdown-v4.test.ts`；仅必要时修改 `internal/migrationv4/markdown.go` 及相邻测试以保留历史 orphan 世代。

**Interfaces:** consumes 已认证 Presentation/GeneratedBaselines/HumanPatches/OrphanPatches；produces 原 `MapV4Presentation` 和 authenticated render bridge 的正确跨世代行为，无新增 wire 合同。

- [ ] RED：真实编辑→恢复生成值（patch 删除、baseline 保留）→新 Session 导致新 generation→再次编辑→sync→reopen，验证人工值、原始 baseline value/hash 和新的 live generation；Go/TS 同样接受最终草稿，不放宽 stale/hash 拒绝。
- [ ] RED：合法旧 v4 list orphan 经认证迁移后普通扫描成功；原 values/hash/历史 generation 不变；重复、畸形 scalar/list shape、错误 hash 和 patch linkage 仍失败。先运行定向测试并保存实际失败输出。
- [ ] 实现：按支持的 scalar/list 形状验证历史元数据；以固定 Markdown 白名单和真实实体存在性识别 live scalar 字段，而不是以是否有 active patch 判断。仅将认证当前 generation 的 eligible live baseline 绑定推进，保留原 value/hash；历史 orphan 不提升世代、不转成 scalar。渲染 bridge 使用同一资格规则，禁止任意 stale baseline 被洗成新绑定。
- [ ] GREEN：`go test ./internal/reviewv4 ./internal/contextupdate ./internal/migrationv4 -count=1`；`go test ./test/zerotoken -run 'MarkdownV4|GateB' -count=1`；插件 `npm test -- tests/markdown-v4.test.ts`。记录 RED/GREEN、覆盖边界与自审，精确 stage 并提交；独立任务审查通过后进入 Task 11。

## Task 11：迁移扫描完整解码后的目标正文

**Files:** Modify `internal/migrationv4/markdown.go`、`markdown_test.go`；Test `internal/syncproject/migration_test.go` 或同包 Markdown 迁移测试；复用 `internal/syncdoc/v4_units.go` 现有敏感内容入口，不另造规则。

**Interfaces:** consumes 已认证旧输入及渲染后的完整 Markdown pair；produces 通过相同 marker-aware 策略验证的可发布 preview，错误仍有界且不回显源内容。

- [ ] RED：旧 JSON goal 通过 JSON Unicode escape 表达测试用的敏感字符串，源字节未直接出现 token、解码后正文出现；预览必须拒绝。覆盖原始历史、自定义保留段和含合法机器 ID 的安全成功例。
- [ ] 实现：完成历史 preservation wrapping 后，对完整 prospective pair 使用认证 marker-aware 扫描，在返回 publishable Result 前拒绝敏感内容。原始输入不得修改，不通过扫描 raw JSON 代替检查目标正文。
- [ ] GREEN：运行 `go test ./internal/migrationv4 ./internal/syncproject -count=1`，实际 coordinator 的 preview/confirm 路径不得发布拒绝对象、不得推进 Base/receipt/private pointers；报告具体覆盖测试与 RED/GREEN，精确提交并独立审查。

## Task 12：接通 v4 只读 sync status 与插件解释

**Files:** Modify `internal/cli/sync.go`、相邻测试；Create `internal/syncproject/markdown_status.go`、`markdown_status_test.go`（如必要）；Modify 现有同步只读公共 helper，仅提取真实共享语义；Modify `obsidian-plugin/src/main.ts` 或实际状态处理模块及相邻测试。

**Interfaces:** consumes 既有 Markdown 认证/只读 dry-run 和既有 sync Status wire；produces typed readonly status。必须保持旧 v2/v3 status 行为；不得把 Report 冒充 Status，不新增私有接受证明 API。

- [ ] 先跟踪 CLI status 路由、Status 必填字段和插件消费，记录明确映射；status 不调用创建式锁、恢复、repair、Prepare 或 publish，观察不稳定绑定/journal 时失败关闭。
- [ ] RED：真实认证 v4 fixture 上 CLI `sync status --json --project-id` 成功输出既有类型；完整 Project/Vault/private 文件清单和字节在查询前后不变，缺 lock/目录时也不创建。覆盖 clean、pending edit、same-field conflict、坏 generated region、缺私有绑定、pending journal 和旧格式回归。
- [ ] 实现最小 v4 只读路由；插件区分运行时不可用与命令/状态失败，不因 status 成功将 public_valid 升为 accepted 或启用结构操作；pending/stale 保持原合同。
- [ ] GREEN：受影响 Go 包、插件 `npm run check`；记录 RED/GREEN，提交并独立审查。随后控制器在获准的独立实验 Vault 上更新候选 CLI/plugin，验证真实 status 和 UI 无误导警告、草稿提示、冲突/机器区拒绝与运行时消失降级；不触碰正式安装、正式 Vault 或生产映射。

本批完成条件：三项任务审查、整分支复审、最终同一源 HEAD 的 M9 全套本地命令与限定原生 UI 验证。平台 CI 和真实旧项目迁移前置条件仍独立列明，不由本地通过推定。

### 2026-09-06 执行结项检查点

Task10–12 实现/任务审查完成；整分支发现的四项 Important 经一次统一修复及一次限定复审全部关闭。最终源码6ad0c05，在只追加证据文档的冻结HEADda3d2df上按序完成全部七项检查（包括完整Go、大Session、race既定排除、GateA、插件223项测试），全部exit0。最终实验Vault验证同一已打开页面编辑→同步自动恢复、冲突/机器区拒绝、失联后原生阅读与手动刷新、154Session零token扫描及完整历史重开。原始待办条目保留用于追溯，执行结论以本检查点和验收报告为准。

本纠偏批次的实现与本地验证完成，原Session Index Task3可据此进入后续Task4；本次尚未执行Task4–7。M9整体交付仍保留平台CI、真实旧数据迁移/分类前置能力和冷启动无任何CLI候选的验收；原3项Minor未解决。没有推送、合并、发布、正式插件/Vault修改，也不删除本地实验与审查证据。详细命令、失败历史和最终证据见`docs/verification/2026-09-05-v4-human-markdown.md`。

## Task 13: Accurate bounded Vault read diagnostics (M1 debt)

Context: current `repository.ts:loadV4` collapses every four-file read failure to baseline_missing. Fix only this diagnostic path and consumers/tests.
Files: plugin data/repository.ts, repository-v4.ts and necessary snapshot/presentation types or view helpers; tests/repository-v4.test.ts and focused related tests.
Requirements: identify the failing relative document (one of the fixed four filenames), and a bounded safe category such as missing, permission-denied or read-failed. Never echo exception messages, absolute paths, arbitrary codes or document contents. Preserve first-load and same-project stale-snapshot diagnostics through the UI; retain stale read-only behavior and do not promote public validation to private acceptance. Parser validation failures must not be mislabeled IO failures. Read-only: no Vault/ledger writes or new acceptance API.
TDD: actual RED for each of four file reads, category mapping, hostile exception text, first-load and stale UI; GREEN focused tests then full plugin npm run check. Self-review and commit only task files. Full report must include exact RED/GREEN output, files, commit, concerns.

## Task 14: Preserve YAML binding presentation (M2 debt)

Context: `replaceMarkdownFrontmatterBindings` matches bare literal keys and rewrites complete lines, losing quoted keys, spacing and comments.
Files: internal/reviewv4/markdown_render.go, markdown_document.go or a focused scalar-span helper as necessary; adjacent tests and shared corpus only when needed.
Requirements: use validated YAML scalar spans to replace only changed revision/generation values. Preserve all other bytes, including quoted keys, comments, spacing, CRLF/mixed line endings and unchanged-generation spelling. Respect accepted scalar styles, escapes, block scalars and tags; no global YAML pretty-print or broadened validator/sensitive-scan exemptions. Preserve bounds and malformed/duplicate/alias rejection. Do not change canonical JSON or fixed field contracts.
TDD: actual failing valid quoted-key and comment/spacing examples through real render/parse, no-op byte equality, generation/revision changes, CRLF and supported scalar presentation boundaries; regress existing identity sensitivity tests. GREEN complete reviewv4 plus affected syncdoc tests, record output and self-review; precise source/test commit.

## Task 15: Indexed bulk Markdown edits (M3 debt)

Context: ApplyMarkdownEdits and recordMarkdownPatch repeatedly search entity/baseline/patch collections; stored semantic alias resolution repeats entity scans.
Files: internal/reviewv4/markdown_draft.go, markdown_baseline.go and a focused internal lookup helper if useful; adjacent tests/benchmarks.
Requirements: build reusable per-operation live-field/entity and semantic metadata indexes, reusing them across validation/application and carry where relevant. Preserve fixed field ownership, legacy-alias ambiguity and duplicate rejection, historical/orphan semantics, original stored identity/hash, restore-default behavior, provenance, entity revisions, no-op bytes and input immutability. Avoid persistent caches, schema changes or broad unrelated refactors.
TDD: establish real pre-fix failing/scaling evidence, retain behavior regressions, add bounded large valid fixtures and reproducible benchmark sizes to measure growth. Do not use brittle wall-clock CI thresholds or assertions on source text. Report before/after latency and allocations, and remaining sorting/validation costs honestly; demonstrate full ApplyMarkdownEdits and baseline carry, not a helper-only speedup. GREEN reviewv4, contextupdate, syncdoc and relevant zero-token integration; exact outputs and self-review, precise commit.

## Task 16: Cold-start no-runtime gate and final verification

Files: necessary plugin discovery/startup tests and minimal source corrections only if reproduced; docs/verification/2026-09-05-v4-human-markdown.md and plan checkpoints are controller-owned.
Requirements: verify fresh discovery with no CLI candidates, no stale selected runtime/cache fallback, native Markdown reading remains available, structural/acceptance actions stay unavailable with a clear diagnostic; no downloads/installs, model calls, production Vault/config edits or scanning real projects. Prefer existing injectable discovery boundaries and an isolated Obsidian experiment process/environment for native evidence. Any source fix follows RED/GREEN and independent review.
After Tasks 13–15 task reviews, final whole-branch review and one stable-source ordered M9 local sequence (full Go including ordinary large-Session test; vet; tidy -diff; exact Gate A; existing race command/exclusion; diff --check; full plugin check). Keep scope exclusions explicit as user decisions, not passed tests. No push, merge, release, production replacement or automatic rescan. Original Session Index Tasks 4–7 remain separate feature work.
