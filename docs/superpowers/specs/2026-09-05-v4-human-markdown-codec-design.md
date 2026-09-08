# v4 人类 Markdown、编辑合并与迁移补充设计

> 2026-09-06 用户确认的范围调整：本轮不再以跨平台 CI、真实旧项目迁移作为交付门槛；旧项目可另行重新扫描。此调整不等于这些测试通过，不授权自动重扫、删除旧记录或覆盖人工内容，也不移除已有兼容性与拒绝不安全写入的保护。剩余三项已知技术债及冷启动无 CLI 降级验证仍须完成，执行记录见实施计划 Tasks 13–16。以下保留原设计和历史验收要求供追溯。

- 状态：2026-09-05 用户已确认完整书面设计；进入实施计划编写，尚未进入实现。
- 日期：2026-09-05。
- 实施基线：隔离分支 `codex/session-index-v1`，代码提交 `2753827`。
- 上位设计：`2026-09-04-obsidian-project-context-navigation-design.md`。
- 接入位置：Session Index Publication and Query 计划 Task 2 之后、Task 3 普通四文件发布之前。

## 1. 已确认的决定与范围

用户已确认：**正文直接编辑，结构显式操作；同一人工字段只有一个权威位置。**

本补项只解决两个 Markdown 的物理格式、无损回读、人工修改合并、版本识别和显式迁移。它不重新设计五个 Tab，不实现问答分段、问题归位算法、价格服务或 Agent 候选服务；不把全部 Session 原文搬进文档。

采用「可编辑 Markdown + 隐藏机器账本与合并基线」。未采用把完整结构数据嵌入 Markdown 的方案：后者虽可提高单文件独立性，却会使源码臃肿并增加正文与隐藏副本的歧义。选择的代价是：失去隐藏账本时，两个 Markdown 仍能阅读，但不能猜测结构后继续写入，需要从已验证备份恢复。

本文件提出的是对 Gate 0 的**显式补充**，不是声称旧合同已经覆盖 Markdown。书面设计确认后，要同步更新 Go、JSON Schema、TypeScript、迁移与版本夹具；完成补充合同门禁后再接普通发布。

## 2. 当前缺口

经代码核对：

- `internal/reviewv4/codec.go` 的 `parse` 对完整 review 文件调用 `DecodePresentation`，当前输入实际是 JSON。
- `internal/migrationv4/migrate.go` 用 `strictjson.Encode(presentation)` 生成 review 文件；history 只保留为有哈希的 UTF-8 字节。
- `internal/contextupdate/service.go` 普通扫描仍通过 `reviewv2.LoadV3Bytes` 读取人工文档。
- `internal/syncdoc/v2_units.go` 的稳定语义单元只支持 v2/v3。

四文件 journal 已存在，但它只能证明所给字节被安全发布，不能证明这些字节是可编辑的 Markdown。不得以 JSON 文件改后缀、正文预览覆盖编辑、或 v4→v3→v4 的有损转换替代本补项。

## 3. 四文件的权威分配

| 文件 | 内容与权威 | 人工编辑 |
|---|---|---|
| `项目回顾.md` | 当前目标、阶段、状态、下一步；决策、风险、未决问题、正式问题节点的人工正文 | 仅允许字段注册表中的人工字段及自定义内容 |
| `项目历史.md` | 全部已接受里程碑的标题、摘要和闭环展示 | 允许人工标题、摘要、结论与后续说明；机器证据区只读 |
| `.session-reviewer/ledger.json` | 机器事实、价格审计、已接受结构关系、修订、上次接受的人工内容基线和四文件绑定 | 不可直接修改 |
| `.session-reviewer/session-index.json` | 累积 Session 集合、覆盖计数、来源与世代绑定 | 不可直接修改 |

“机器账本持有结构”不表示扫描可以改结构：正式问题父子关系、决策替代关系等仍只由经过确认的受信结构命令变更，属于 HumanPresentation。

### 3.1 单一正文位置

- `current_state`、`decisions`、`risks`、`open_loops`、`problem_nodes` 的人工字段位于回顾。
- `timeline` 的人工字段和完整闭环位于历史；回顾中的近期里程碑只有链接，不复制可编辑正文。
- 回顾内决策置顶区域只列链接；完整决策正文只出现一次。
- 问题概览树按已接受的结构生成，只列节点链接；问题正文块各出现一次。改变缩进不改变父子关系。
- 正文锚点由稳定实体 ID 生成，不由标题生成；改标题不产生新实体或断开引用。
- 两份文档内的自定义段落是各自独立的人工内容，不自动升级成正式问题、决策或里程碑。

历史包含全部已接受里程碑；完整节点正文不得固定截断。回顾仅作链接概览时显示总数、展示数与完整入口。数组/文件超过合同上限时拒绝新发布，不截断后声称完整。

## 4. 合并基线不是第二份当前正文

在 `machine-ledger-v4` 增加可选的 `document_projection`，采用封闭的内嵌合同：

```text
document_projection {
  schema_version = 1
  format = "review-markdown-v1"
  presentation_base: review-presentation-v4
}
```

`presentation_base` 保存最近一次**成功接受**的完整类型化 HumanPresentation，包括结构、人工字段和现有 patch/baseline 信息。它不是原始对话快照；只能包含上位合同已经允许的文本和限长摘录。该对象不反向包含 ledger，禁止递归包装。

- 已接受投影：两个 Markdown 回读出的人工字段必须等于 `presentation_base`，机器字段和结构由该基线及账本认证。文档哈希、项目、generation、ProjectView digest、revision 必须相符。
- 待同步编辑：Markdown 字段相对基线的差异是人工编辑草稿，不能因为旧哈希不匹配就覆盖，也不能绕过校验当成已接受数据。
- 基线更新：只有新的发布事务成功提交后，基线才变成合并结果；失败或取消不更新。
- 没有可验证基线：保持文档原样并报告需要恢复，不以当前正文重建机器哈希或问题关系。

内嵌模型的 `human_patches`、`orphan_patches`、`generated_baselines` 与账本对应字段必须深度一致；不一致拒绝接受。`presentation_base` 的项目、generation、ProjectView digest、revision 必须分别匹配外层身份和 `accepted_revision`。整个扩展参与既有 ledger 自摘要，不引入第二种自摘要算法。

## 5. Markdown 格式与回读

### 5.1 文档壳

保留普通标题、段落、列表、链接和代码块。frontmatter 只保存文档身份与版本绑定：`id`、`entity_type`、`project_id`、`schema_version: 4`、`document_format: review-markdown-v1`、`revision`、`generation_id`、`minimum_reader_version`、`minimum_writer_version`。

允许原有非保留 frontmatter 字段，不允许重复 YAML key、别名/合并键、多文档或未知保留字段。保留字段为上述固定列表，以及用于未来扩展的 `session_reviewer_` 前缀；该前缀下未注册的键报错，其他用户键原样保留。`document_format` 是额外判别符；不能仅凭后缀或首个 `{` 判断整个项目的格式。

### 5.2 稳定字段块

沿用 HTML 注释对普通阅读隐藏、对源码可见的方式，但使用独立 v4 命名空间。示意对应目标字段：

```markdown
## 项目目标
<!-- session-reviewer:v4-field entity="project-overview" name="goal" -->
让项目中的全部 Sessions 可以完整回顾。
<!-- /session-reviewer:v4-field entity="project-overview" name="goal" -->
```

实体键固定为 `project-overview` 或 `decision:<id>`、`risk:<id>`、`open-loop:<id>`、`problem:<id>`、`milestone:<id>`，使用原合同 safe ID，不接受任意 JSON 路径或文件路径。`name` 只能来自本文件第 6 节的字段注册表。一个键只能出现一次；其物理文档由注册表决定。

字段值是两个标记之间的原始 Markdown 字符串，不把标题、列表符号或代码块翻译成业务关系。渲染器在开标记之后和闭标记之前各提供一个换行分隔符；解析只去掉这两个分隔符，不 `TrimSpace` 内容。空值仍保留空字段块；字段缺失与字段空值不同。

只识别独占一行、处于顶层且位于 fenced/indented code block 外的保留标记。代码示例中的标记是普通文字。开闭属性必须一致；重复、跨块嵌套、未知字段、外来 ID、错误文件归属和不完整标记均拒绝机器写入，不删除原文。需要展示保留标记文字时使用代码块或转义，不能猜测其含义。

### 5.3 格式与自定义内容保留

文档解析结果同时保留类型化值与原始字节跨度。未改变字段、未改变的文档壳、自定义段落、链接和代码块保持原字节；不全文件重新 pretty-print。比较语义时仅规范化 CRLF/LF，保留空格、段落和内容尾换行。新插入区域沿用文档现有换行风格；混合换行文档的未改区域不重写。

未标记文本默认是自定义内容，不通过自然语言或标题猜测填入正式字段。未知用户 frontmatter、段落和块在迁移与扫描中保留。删除整个正式字段块或复制正式实体块不会被当成删除/创建实体的命令，而会给出可定位的格式错误，保留草稿等待修复。

机器生成区域使用独立 `session-reviewer:v4-generated` 标记，与人工字段互斥。修改该区域报告只读区域被改动，不静默覆盖，不把其文本作为新的机器事实。普通自定义段落可用于补充机器证据的解释，但不改变证据状态。

生成区域的开闭格式分别为 `<!-- session-reviewer:v4-generated entity="<entity>" name="<region>" -->` 和 `<!-- /session-reviewer:v4-generated entity="<entity>" name="<region>" -->`，遵循相同的顶层、唯一和不嵌套规则。region 注册为 `project-overview` 的 `problem-tree`、`pinned-decisions`、`recent-milestones`，以及 `milestone:<id>` 的 `evidence`。区域内容以当前格式版本的确定性渲染器、已认证的 ledger 与 presentation_base 重新生成的结果为校验依据，不相信正文自报的哈希。检查草稿时先与旧接受态比较，再生成新机器区域；不能把正常的新扫描差异误判为人工篡改。改变区域格式需要显式格式升级，不随模板更新静默重写。

## 6. 人工编辑白名单

| 实体 | 可直接编辑的字段 | 不能由正文修改的字段 |
|---|---|---|
| current_state | goal、stage、status、next_action、last_verification | 项目与世代身份、机器验证证据 |
| decision | title、rationale、impact、reevaluate_when | ID、kind、status、supersedes、引用、provenance、pinned、revision、原迁移状态 |
| risk | title、detail、status | ID |
| open_loop | title、question、next_experiment、completion_criterion、status | ID |
| problem | question、completion_criterion、current_conclusion | ID、父子/相关关系、workflow_state、answer_state、排序、引用与修订 |
| milestone | title、summary、conclusion、impact_and_follow_up | ID、kind、occurred_at、generation、决策引用、source refs、coverage、触发/执行/验证事实 |

`risk.status`、`open_loop.status` 和项目状态沿用原有文本合同，不据此推断 `problem.workflow_state=resolved`。`last_verification` 是人工描述，显示为人工记录，不能改变命令退出码或机器验收结果。

里程碑 `conclusion` 对应 `closed_loop.conclusion.text`：

- 字段未变时不改 provenance；只因重排版不能把机器摘录变成人工确认。
- 人工改为非空文本时，用受信同步操作设为 `human_confirmed`，清除 missing reason，保留原 source refs，记录人工 patch；不改验证状态或任何其他闭环段。
- 非 missing 结论被清空时拒绝接受并保留草稿，提供恢复原值的定位信息；不得把清空解释为“未捕获 Agent 回答”。原本 missing 的空结论保持 missing。

`impact_and_follow_up` 对应其 segment.text。人工首次补入非空文本可将 missing 改为 present、清除 missing reason，并记录人工来源 patch；该状态仅说明有说明文字，不是执行通过。对原本 partial 的段只改文字仍保留 partial 和原引用。清空原非空段同样拒绝接受并保留草稿。

任何直接编辑均不修改来源正文或私有 Conversation Chain。结构增删、移动、合并、排序、决策替代/归档、问题解决/重开仍走受信 CLI 或插件确认流程，校验实体修订和图约束。

上述流程是授权边界，不代表当前所有操作已有入口。缺少已定义命令合同的操作保持不可用，不以正文编辑代替；结论或后续说明的显式清除不在本补项范围。后续若增加这些操作，必须先定义状态转移、引用保留、审计和修订校验合同。

## 7. 读取、编辑与扫描合并

### 7.1 两种读取入口

1. **读取已接受投影**：验证 ledger 自摘要、文档精确哈希、Markdown 格式、基线一致性、完整 index 与私有 manifest 的绑定，全部通过才提供正式状态。
2. **读取编辑草稿**：以认证的已接受基线和 sync 合并基线解析本地变更，只允许人工白名单/自定义内容变化。返回编辑项与诊断，不自行写账本。

插件可在编辑草稿上提示“有未同步修改”，但不得展示为已成功发布。无 CLI 或无 ledger 时普通 Markdown 仍可阅读；结构操作和正式接受禁用，并提供明确恢复入口。

### 7.2 三方合并

对每个稳定实体字段比较上次成功同步时保存的共同 Base、Project、Vault。第 4 节的 presentation_base 用于认证各端接受态及识别本地草稿；它不一定等于共同同步 Base，不能用较新一端的接受态覆盖共同祖先。尚无共同 Base 时，须走显式初始化预览：仅两端相同字段可自动合并，双方不同则要求选择，不猜测谁更新。

- 两端均等于 Base：不变。
- 仅一端变化：采用该端人工值。
- 两端变化为相同值：合并为一次变更。
- 两端改成不同值：返回字段冲突，显示双方文本和基线，不覆盖任一端。

自定义内容复用现有文档语义单元与物理壳保留机制；同一区域双方冲突同样阻止发布，不能以新的整份模板替换。检测机器保留字段或结构被手改时，不能通过上述三方规则接受。

扫描产生的确定性新事实与人工修改分开：更新机器索引、账本与只读证据；对白名单字段沿用 `HumanPresentation > deterministic ProjectView`。已存在人工覆盖不因生成器变化消失。实体不再有机器支持时保留其人工内容与 orphan patch，不静默删除。

普通扫描在发布前重新读取并校验精确 Project/Vault 预像。锁外算出的合并结果不能覆盖锁内发现的新编辑；变化返回冲突并保持上一接受状态。

### 7.3 修订与幂等

只有实际接受了文档字节或结构/表现变化才推进 presentation revision；单纯读取、dry-run 和同输入重扫不推进。人工字段修改可推进相应实体修订，但不改扫描 generation。纯自定义排版变化可推进文档接受修订，不提升结论来源或问题状态。

失败事务不更新 accepted baseline、sync base 或发布指针。成功后同输入连续 render、sync、重启读取必须保持相同字节、哈希与修订。

## 8. 发布与恢复

保留既有两个事务范围：

- 扫描：两个 Markdown + ledger + Session index，四文件原子发布。
- 人工修改/确认：两个 Markdown + ledger，事务开始和提交前均验证未重写的 Session index 仍与预期 generation、digest 一致。

Project 的机器文件仍单向权威；人工文档双向合并。journal 记录实际文件集合、预像和目标哈希；所有 Project/Vault 目标验证通过后才推进发布状态。物理写入中的暂态文件不算已接受快照；读者需校验 ledger 绑定与 journal 状态，发现混合世代应拒绝或读取旧接受快照，不能声称四次文件 rename 是操作系统级原子快照。

新文档解析器只替换格式与语义单元适配，不新建一套锁、journal 或恢复引擎。迁移、人工变更和扫描发布都必须经过相同的受信路径与冲突检查。

## 9. 版本与显式迁移

### 9.1 能力区分

新格式判别为 `review-markdown-v1`，内嵌 `document_projection.schema_version=1`。拟定新能力的最低 reader/writer 为 `0.4.1`，这是设计中的能力版本，不表示该版本已发布。

- 旧 v4 JSON 组合：外层最小版本仍为 `0.4.0`，不带扩展；旧有效夹具和 canonical bytes 不变。
- 新 Markdown 组合：外层 ledger 和两文档最低 reader/writer 为 `0.4.1`，扩展必填；内层 `presentation_base` 继续使用现有 `review-presentation-v4` 的 `0.4.0` 语义合同，不递归承诺物理文档能力。
- 新实现明确接受这两种组合并拒绝交叉拼接。不得把全局“未知字段一律接受”作为兼容方案。旧实现必须因不支持版本或严格字段解析失败而拒绝写入。
- 删除扩展、篡改 format、降低 minimum writer 或混用旧 ledger/new Markdown 均失败关闭。新增 `document_projection` 全部字节进入 ledger 自摘要。

2026-09-09 历史来源能力补充：上位设计 §17.3 的限定 source-turn 引用或同一 Session 多快照 dependency 使用语义能力 `0.4.3`；包含此扩展时，内层 presentation、外层 ledger 与两份 Markdown 的最低 reader/writer 均为 `0.4.3`。未使用扩展的旧 JSON/Markdown 组合继续遵循上述 `0.4.0`/`0.4.1` 规则，省略字段和旧规范字节不变。该补充不授权单纯读取或初始化时升级能力、重写旧发布或接受人工草稿；新发布必须校验完整版本组合，不能只放宽全局版本解析器。

### 9.2 迁移路径

v2/v3 继续使用显式 `sync --dry-run` 与带 preview digest 的确认迁移流程。不能先做一次可能丢字段的中间发布，再升级到新格式。v2 在内存中经现有合法转换衔接，最终目标四文件一次接受。

已经存在的 Gate 0 v4 JSON 项目也走同样的“格式升级预览→明确确认”流程，不把 JSON 当损坏 Markdown，也不在普通 scan 中自动替换。

预览必须覆盖：原始四文件是否存在及精确哈希、源版本、人工字段归属、需要保留的自定义内容、目标四文件哈希、当前已接受 generation、SessionView 依赖、index 绑定和目标预像。确认在锁内重新生成并比较 preview digest，任意变化返回 stale，不套用旧预览。

旧 review/history 都含同一语义字段时：利用已验证的旧 parser、机器基线与人工 patch 判断来源。明确相同可归一；有已证明的单侧人工编辑采用该人工值；双方不同且无可靠依据则列为迁移冲突，不能静默选择 JSON 或历史文本。旧人工原文、自定义块和稳定 ID 必须保留；不可识别但安全的正文以明确标注的历史保留段保留，不据其内容推断新实体。

上位设计要求的占位演进节点重分类、chain 引用重建仍由相应能力提供。本补项不能用空数组冒充迁移成功：缺少完成迁移所需的链或分类能力时，预览明确阻止确认；已有能力就调用其确定性结果，不在 Markdown codec 内重新做语义推断。

旧迁移 index 尚无私有 manifest 绑定时，补充合同门禁必须覆盖“确认迁移时建立认证继承绑定”的衔接；dry-run 不创建私有对象、不推进 prepared/published。不得将松散公共 index digest 当作已认证的历史依据。

## 10. 有界解析与诊断

两个 Markdown 和 ledger 各自上限 64 MiB；index 继续使用 65,536 项/64 MiB。嵌入基线不豁免 ledger 上限。超限保留原文件和上一接受世代，不裁切。

解析 UTF-8 与字段字节限额沿用 v4 类型合同；文档扫描须线性遍历、有限内存，不使用按每个实体反复扫描全文的实现。注册表仅允许固定字段名，不把 marker 属性当表达式执行。渲染不执行文档中的命令、链接、代码或提示词。

沿用既有敏感内容扫描边界；本机绝对路径、秘密或禁止的源内容不能因作为“自定义内容”绕过发布检查。遇到既有敏感人工段落，保留原文并明确报错，不自动删改或泄漏到诊断。

新增封闭错误族：`markdown_format_invalid`、`markdown_field_duplicate`、`markdown_field_missing`、`markdown_structure_edit_requires_command`、`markdown_generated_region_modified`、`markdown_baseline_missing`、`markdown_field_conflict`、`markdown_migration_conflict`。继续使用既有输入溢出、generation mismatch、migration preview stale 等错误。诊断只返回类型、相对文档、实体/字段和有界文本；敏感内容不回显。

## 11. 模块边界

| 模块 | 单一职责 |
|---|---|
| reviewv4 文档 codec | 文档格式、字段注册表、字节跨度、草稿与接受态解析；保留独立 JSON DTO codec |
| reviewv4/schema/TS 合同层 | ledger 扩展、版本组合、基线一致性与共同有效/无效夹具 |
| syncdoc v4 适配 | 稳定字段、自定义内容、物理壳与机器区域检查 |
| presentation/contextupdate | 合并人工值与确定性新事实，生成四文件计划 |
| syncproject/migrationv4 | 格式识别、预览与确认、legacy 内容无损衔接 |
| publication | 复用锁、预像、journal、精确验证与恢复 |

Go 与 TypeScript 使用同一份字段归属与可写性合同和成对 fixture，不能各自实现不同的“差不多能读”的 Markdown 规则。插件只读解析不能自行重写隐藏账本。

## 12. 必须通过的验收

1. 无插件打开两文档，能够直接阅读目标、决策与完整里程碑；源码不是整份 JSON。
2. 每类白名单字段都做 render→parse，中文、多行、空格、代码块、括号、空值和尾换行往返不丢失。
3. 人工修改目标、理由、结论和自定义段落后 scan→sync→重启，修改保留；仅结论编辑不能改变验证状态。
4. 同字段双侧不同编辑产生冲突，不同字段编辑可合并；机器区域、关系、revision 与 ID 手改拒绝接受。
5. 删除字段块、重复标记、标记出现在代码块、错配开闭、未知字段和外来实体均有确定结果；损坏草稿不被覆盖。
6. 回顾链接与历史正文引用同一 ID，标题变更不创建第二份实体；所有正式正文只有一个位置。
7. v2、v3、旧 v4 JSON、新 Markdown 四类夹具覆盖读取、升级预览、明确确认和版本错误；dry-run 对 Project/Vault/私有存储均无写入。
8. 迁移保持正式问题结构、决策状态原文、人工编辑、chain 引用和历史价格；无法可靠选择旧重复字段时阻止确认。
9. 变更前后 Session index 对应的私有绑定可验证；人工修改不改其规范字节，不推进扫描 generation。
10. 每个写入/同步/验证故障点恢复后，所有接受态绑定一致；失败不更新合并基线。
11. 相同输入连续两次 render/sync/reopen 无字节、哈希、实体 revision 或文档 revision 漂移。
12. Go/TS 对有效和无效文档完全一致；本地单元/集成/零 Token 门禁与原生 Windows CI 分别记录，不互相替代。
13. 在真实 Obsidian Vault 中完成正文编辑、冲突提示、同步、重启与无 CLI 降级验收后，才声称该补项可交付。

## 13. 自审与后续门槛

本稿已区分：当前人工草稿与已接受基线、结构权威与机器写入权限、可写结论与只读验证证据、物理文件暂态与已接受快照。没有把 JSON DTO、Markdown 物理格式、私有索引认证或真实 UI 验收混为同一门禁。

当前只完成书面设计；schema、parser、迁移及 UI 均未因本文发生实现变化。用户审阅本稿后，再用 writing-plans 拆分补充合同与 codec/sync/迁移实施任务，并修订原 Task 3 的依赖。不得凭本稿存在就恢复普通 v4 发布或宣布 Gate 0 补充门禁通过。
