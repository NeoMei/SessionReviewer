# SessionReviewer Obsidian 项目脉络、决策与价格查询设计

- 状态：扩展设计已确认；因新增会话因果链、问题脉络与演进闭环合同，Gate 0 重新打开
- 日期：2026-09-04
- 适用范围：SessionReviewer 零 Token 扫描、macOS/Windows Obsidian Desktop 项目脉络浏览器、Project/Vault 投影
- 扩展：`2026-08-25-session-reviewer-project-evolution-browser-design.md`

## 1. 背景与问题

SessionReviewer 0.3.0 已将可验证的零 Token 扫描与人类语义总结分层。这个方向解决了大量 Session 扫描被 Agent 生成失败中断的问题，但现有 Obsidian 表现仍然沿用“少量演进节点 + 关键决策卡片”模型，产生了以下语义断层：

1. 超长 Session 或一个项目的多组 Sessions 在 Obsidian 中只显示最近少量条目，用户无法确认是否已扫全，也无法稳定地下钻到早期事件。
2. 新的零 Token 扫描可以记录发生过的事实，但不会臆造“为什么这样决定”。因此新项目的“关键决策”为空，现有界面却没有说明这是能力边界而非扫描失败。
3. 当前投影可把大量原子事件 ID 直接写进人类可读页面。这保留了索引，但破坏了“打开项目后快速恢复上下文”的产品目标。
4. 模型价格变化频繁，本地固定价格表容易过期；未匹配价格又不应被当作零成本。
5. 当前“项目演进”把单条用户请求直接投影成节点，右侧只复述问题并填入统一占位文案，看不到 Agent 回答、执行动作或实际验证；这既不是完整会话回顾，也不构成项目里程碑。
6. 项目讨论通常从一个大问题逐步拆成多个子问题，现有界面没有保存这种细化脉络，也无法建议新问题应归入哪个已确认节点。

## 2. 产品原则

本设计固定以下边界：

- **扫描负责完整**：发现、解码和索引所有可归属 Sessions，异常 Session 也必须可见。
- **投影负责可读**：项目首页不堆叠原子事件，只呈现恢复工作所需的当前状态和里程碑。
- **人负责确认语义**：正式的决策与约定必须由人创建、编辑或确认。
- **AI 只负责候选**：AI 可按用户要求从新 Sessions 提炼决策候选，但不能自动升级为正式项目事实。
- **截断必须显式**：任何列表、分页、摘要或保留策略都必须显示总量、当前范围和未展示数量，禁止静默丢弃。
- **价格可追溯且不阻断扫描**：用量事实与价格查询分离；价格不可用时仍保留 Token 统计，但费用不得伪装为 `$0`。
- **结构整理零 Token 优先**：问答分段、同 Session 因果绑定、显式引用匹配、执行与验证归属均使用确定性规则；只有用户主动要求处理规则无法判定的语义歧义时才调用 Agent。
- **只处理可见内容**：人类消息、Agent 可见回答和工具事实可进入因果链；隐藏推理、系统或开发者指令、加密或不透明压缩内容永远不能成为问题、结论或关联依据。

## 3. Obsidian 信息架构

项目页顶部继续作为唯一恢复入口，展示：

- 项目目标；
- 当前阶段；
- 当前状态；
- 下一步；
- 风险与待办；
- 扫描覆盖摘要。

主视图固定为以下顺序：

1. **项目演进**
2. **问题脉络**
3. **决策与约定**
4. **全部 Sessions**
5. **用量**

这个顺序先提供恢复判断所需的高层信息，再提供完整证据下钻，最后展示资源使用。

## 4. 项目演进

“项目演进”只展示语义化里程碑，不直接展示每个消息或工具事件。合格节点包括：

- 方案或约束被确认；
- 一个可识别的实施阶段完成；
- 关键验证或真实环境验收通过；
- 发布、回滚或重要版本变化；
- 重大失败、方向调整或阻断被解除。

默认可以只展示最近的里程碑，但必须同时显示总数和“查看全部”入口。完整模式使用搜索、分页或虚拟列表，不再用固定的 `slice` 伪装成完整项目史。

确定性 ProjectView 只能用中性文案生成有明确机器证据的里程碑，例如已创建提交、已通过验证或已发布版本。原因、意义和方向调整等语义只能来自 HumanPresentation 或经人确认的 AI 候选。界面对“机器验证”与“人工确认”里程碑显示不同来源标记。

项目首页中的原子事件 ID 列表被取消。事实索引通过“全部 Sessions”和私有 Observation Store 保留。

### 4.1 演进详情的闭环摘要

演进节点右侧不再使用“节点意义、摘要、为什么会走到这里、发生了什么、结果与验证、下一步”的空泛模板。固定按以下顺序展示：

1. 触发问题；
2. Agent 结论；
3. 执行与变更；
4. 结果与验证；
5. 对项目的影响与后续。

默认“Agent 结论”来自可见 Agent 最终回答的限长原文摘录，不调用模型重新总结。人工确认或编辑的结论优先于原文摘录；只有用户主动要求整理时才允许 Agent 生成候选摘要，并标记“AI 整理，待确认”，确认后才能成为正式 HumanPresentation。界面提供“查看回答正文”“打开原 Session”“查看关联问题”下钻；回答正文按需从经过哈希认证的原 Session 读取、过滤和脱敏，不持久化到 Vault 或私有派生链。单条源记录最多读取 64 KiB，超限时明确标记截断并以“打开原 Session”作为唯一完整来源。原始工具输出不在此入口返回。

确定性结论摘录选择该问答单元最后一条非空可见 Agent 消息；展开时按原顺序显示该单元的全部可见 Agent 消息。如果 Session 在该单元结束前中断、最后消息未完成或 adapter 只能证明稳定前缀，则结论标记“回答可能不完整”，不能展示成已完成答复。

一个里程碑可以引用多个 provider-neutral 问答单元，因此最初提问、后续实施和再次验证可以跨 Session 组成闭环。每一段都必须显示真实 `(provider, session_id, turn_unit_id)`、时间和证据类型，不能把后续 Session 的回答伪装成原 Session 回答。回答、执行、验证或来源缺失时保留已有段，并明确显示“未捕获 Agent 回答”“未发现执行证据”“待验证”或“来源不可用”，不得用统一占位文案补齐。

### 4.2 旧演进节点迁移

v3 自动生成且未被人编辑的 `user_request` 占位节点在显式 v4 迁移时重新分类：有实施、验证、发布或人工确认依据的升级为里程碑并生成闭环摘要；只有提问、没有形成关键项目变化的转入问题脉络。所有源问答和 Session 引用继续保留。

人工编辑过的旧演进节点原样保留，并标记 `provenance=migrated`；迁移不得用新规则覆盖其标题或正文。dry-run 必须分别报告“升级为里程碑、转入问题脉络、保留人工节点、无法归类”的数量和稳定 ID，确认迁移后才能发布，且重复执行结果必须一致。

## 5. 问题脉络

“问题脉络”使用顶部独立 Tab。左侧只显示真实问题句及其父子层级，不重复“项目演进、决策与约定、模型价格”等顶部栏目。主体区域显示当前问题的父路径、直接子问题和有限相关问题；右侧显示所选问题关联的问答与执行因果链；底部抽屉处理待归类问题。

项目只有一张跨 Sessions、跨 Codex/Claude Code/OpenCode 的问题图。界面可按 Session、provider 和状态筛选，但筛选不改变图的权威结构。正式图属于 `review-presentation-v4` 的 HumanPresentation；AI 或规则推荐只能进入私有 `problem-map-candidate-v1`，未经用户确认不得改变父子关系。

### 5.1 问答单元与因果链

最小问答单元从一条可见用户消息开始，到同 Session 的下一条可见用户消息之前结束，包含期间所有可见 Agent 回复、工具调用、工具结果和文件变化引用。系统消息、开发者消息、隐藏推理和不透明压缩内容不参与分段。连续的多条 Agent 消息属于同一单元；没有 Agent 回复的单元合法存在，回答状态为 `no_answer`。

同一 Session 内的顺序绑定是确定性的。跨 Session 只在出现稳定问题 ID、显式引用、相同文件/符号/提交/错误签名等可复核信号时自动关联；仅靠语义相似只能生成待确认候选。每个问答单元使用 `(provider, session_id, turn_unit_id)` 身份，不能仅以原生 Session ID 或消息文本去重。

### 5.2 正式问题节点

每个正式问题节点包含：

~~~text
id
question
primary_parent_id | null
related_node_ids[]
workflow_state = not_started | in_progress | paused | resolved
answer_state = no_answer | answered_unverified | execution_verified
completion_criterion
current_conclusion
source_turn_refs[] { provider, session_id, turn_unit_id }
provenance = human_created | migrated | candidate_confirmed
first_proposed_at
sibling_order
confirmed_at | null
revision
~~~

节点只能有一个主父节点，但最多可以有两个相关节点交叉引用。主父关系必须无环；坐标和临时折叠状态不持久化。兄弟节点默认按首次提出时间和稳定 ID 排序，用户可以显式调整顺序。

`execution_verified` 只表示存在受支持的测试、构建、命令退出码、产物或真实环境证据；它不等于项目问题已经解决。`workflow_state=resolved` 必须来自用户确认或已接受的项目验收事实。仅有 Agent 回答且没有验证时显示“已有回答，待验证”。

### 5.3 新问题归位建议

零 Token 规则先使用编号、标题层级、显式引用、共享文件/符号/提交/错误签名和连续追问关系产生归位建议。建议必须包含一个推荐主父节点、最多两个备选节点、最多两个相关节点、命中的可读依据和 `high|medium|low` 置信等级；不得展示无法复核的伪精确概率。

用户可以选择“作为子问题”“作为同级问题”“合并到已有问题”或“继续待归类”。合并必须保留所有源问答引用；移动有子树的节点时，确认对话框显示旧路径、新路径和受影响子树。规则无法可靠归位时默认进入“待归类问题”，不自动调用 Agent。用户主动要求 Agent 协助时，调用结果仍只是候选，并以排序后的 dependency digests 缓存，依赖未变化时不得重复消耗 Token。

## 6. 决策与约定

“关键决策”更名为“决策与约定”，并分为两个区域。

### 6.1 已确认

正式条目只能来自：

- 用户手动新增或编辑；
- 已有 v2/v3 决策迁移；
- AI 候选经用户确认。

每条记录包含：

- 决策或约定内容；
- 理由；
- 影响范围；
- 状态：生效中、已替代、已归档；
- 重新评估条件；
- 关联的项目里程碑与 Sessions；
- 来源类型：人工创建、旧版迁移或 AI 候选确认。

默认只展示“生效中”条目。旧决策不物理删除，而是用“已被某决策替代”保留演进关系。

### 6.2 待确认候选

零 Token 扫描不自动生成决策候选。只有用户主动点击“从新 Sessions 提炼候选”时，系统才可调用受限 Agent：

- 候选必须引用具体 Session 和已脱敏证据节点；
- 用户可编辑后确认、直接忽略或标记“不是决策”；
- 未确认候选不得进入项目演进，不得修改当前阶段、下一步或风险；
- 界面不展示不可校验的数字置信度，而是展示支持该候选的事实。

Agent 候选保存在私有、依赖绑定的语义注释存储中。Obsidian 插件通过受限只读查询加载候选；确认操作由受信 CLI 生成人类表现 patch 并进入既有同步流程。

无决策时不显示空白面板，而是显示：

> 尚无已确认的决策与约定。扫描已经保存项目事实，但不会替你判断项目意图。

并提供“新增决策或约定”与“从新 Sessions 提炼候选”两个入口。项目首页最多展示三条当前生效的决策：人工置顶条目优先，其余按发生时间倒序和稳定 ID 排序。置顶是人类可编辑展示字段，不改变决策身份或证据。

## 7. 全部 Sessions

### 7.1 列表完整性

每个被发现且可归属项目的 Session 必须占据一个索引项。损坏、未完成、部分可读、来源不可用或存在警告的 Session 不得从列表中消失。

列表顶部始终显示：

- 已收录总数；
- 已完整处理数；
- 部分处理数；
- 异常数；
- 尚未处理数；
- 来源不可用数；
- 扫描时间范围与来源分布。

`complete`、`partial`、`error`、`unprocessed` 是互斥处理状态，四项之和必须等于已收录总数。`source_availability` 是与处理状态正交的维度；一个此前已完整处理、后来源文件消失的 Session 保留 `complete`，同时标记为 `source_unavailable`，不得因来源消失改写已经接受的处理结果。

处理状态固定定义为：

- `complete`：已完成发现、冻结和解码，所有已知源记录均被访问，且没有无法解码、截断或未归类记录；
- `partial`：已产生可信 SessionView，但存在无法解码、未支持、折叠、截断或其他明确 coverage 缺口；
- `error`：本轮已尝试处理，但无法产生可信 SessionView；
- `unprocessed`：已经发现并归属项目，但在该扫描世代内尚未开始处理；只能出现在扫描中的暂态索引或 `completed_with_issues` 的终态记录中，必须带原因码。

现有机器终态到界面状态的默认映射为：`indexed` 且无 coverage 缺口映射到 `complete`；`indexed` 有缺口或 `unsupported` 但有可信部分投影映射到 `partial`；`unsupported`、`missing`、`unreadable`、`ambiguous` 且无可信投影映射到 `error`。实现不得仅凭警告数量猜测状态。

Session 索引是“项目已接受 Session 集合”的累积视图。新世代以旧索引和本轮发现集合的并集为输入：本轮未再次发现但过去已接受的 Session 保留索引、摘要和最后成功世代，并将来源标为 `source_unavailable`。扫描不会自动遗忘 Session；本设计不提供删除入口，未来若增加遗忘能力，必须是独立、明确确认并带审计记录的管理操作。

默认按时间倒序分组，稳定排序键为 `started_at desc nulls last, provider asc, session_id asc`。日期、来源、处理状态和来源可用性可直接由紧凑索引筛选；分支、文件和错误特征通过受限只读 CLI 查询，避免为搜索而把大量路径或错误文本复制到 Vault。

### 7.2 Session 索引项

每个索引项最少包含：

- provider 与命名空间化 Session ID；
- 开始、结束时间与耗时；
- 处理状态、来源可用性与警告数；
- 总事件数和已索引事件数；
- 文件变更、命令、验证、错误和产物等类型统计；
- 关联用量索引；
- 覆盖数据，包括是否存在被折叠、未投影或无法解码的记录。

未知数据使用显式 `null` 和对应的 `*_known: false` 表示，不得用零代替未知。索引只保存项目相对路径的有限摘要、类型化错误码和不可逆错误签名，不保存绝对路径、错误原文、高熵字符串或完整 excerpt。

不在 Vault 中默认复制完整原始对话或工具输出。

### 7.3 下钻与分页

点击 Session 后，右侧详情展示该 Session 的阶段、关键操作、验证结果、错误和留下的问题。这些摘要必须是已脱敏且有来源绑定的确定性投影。

用户继续查看事件时，插件通过受限只读 CLI 命令从私有 Observation Store 分页获取数据。公开命令合同必须包含 project ID、provider、Session ID、expected generation ID、不透明 cursor 和受限 page size，且不得接受任意文件路径。

每页最多 100 条。界面显示“当前 1–100 / 共 2,438 条”等完整性信息。顺序浏览使用不透明 `previous_cursor` 和 `next_cursor`；跳转首页、末页或指定页时，插件把一基 ordinal 交给 CLI 换取当前世代的页锚点。cursor 必须绑定 project ID、provider、Session ID、generation ID、排序版本、筛选摘要和 page size。任一绑定不一致或世代过期时返回类型化 `stale_cursor`，插件刷新索引后回到最接近的可用位置，不静默读取另一个 Session 或世代。

### 7.4 投影文件

新增隐藏的：

~~~text
docs/session-review/.session-reviewer/session-index.json
~~~

对应 Vault 投影仍位于 `.session-reviewer/` 下，不增加用户可见 Markdown 文档。

`session-index.json` 使用独立的 `session-index-v1` 合同，绑定 project ID、generation ID、ProjectView digest 和精确覆盖计数。它参与既有发布 journal、预像比较、Project/Vault 同步与发布后哈希验证。该文件是紧凑清单而不是 Session 详情库；最大 65,536 项、最大 64 MiB。超过任一上限时扫描不得截断后宣称完整，而应拒绝发布新世代并保留上一有效世代，报告 `session_index_capacity_exceeded`。

旧插件可忽略该隐藏文件；新插件在索引缺失时显示“需要重新扫描以建立完整 Session 索引”，不将缺失解释为零个 Sessions。

## 8. 用量与价格

### 8.1 展示

保留每个模型一张横向占满的卡片。卡片展示：

- 模型与实际计费服务商；
- 输入、缓存输入、缓存写入、输出与总 Token；
- 每百万 Token 单价；
- 按类型估算成本与总成本；
- 价格有效日期与查询时间；
- 价格状态：当前、促销、已过期缓存、手动补充、存在歧义或待定；
- ModelPriceWatch 数据页与服务商官方价格页。

界面明确标注“公开 API 标价估算”，订阅包含量、实际账单折扣、税费与企业协议价不参与计算。

### 8.2 ModelPriceWatch 查询

默认使用 [ModelPriceWatch API](https://modelpricewatch.com/api/) 作为价格主查询目录。受信 CLI 对 `https://modelpricewatch.com/api/v1/models.json` 和 `https://modelpricewatch.com/api/v1/price-history.json` 分别做每 24 小时最多一次的全局缓存刷新，可使用 ETag 或等价条件请求。不按项目或模型频繁请求。

请求只下载公共价格目录，不上传项目名、Session ID、Token 计数、工作目录或其他本机数据。

匹配必须同时满足：

1. 实际计费 provider/host 精确匹配到一个 ModelPriceWatch listing ID；
2. 规范模型 ID 或经审查的别名精确匹配；
3. 区域、调用模式、上下文档位、批处理状态和计费类型等适用条件均已知且相符；
4. 所有产生非零 Token 的计费维度都有对应明确价格。

不使用模糊名称相似度自动决定价格。同一模型在多个 host 出售时，必须使用 Session 实际路由的 host 价格。

ModelPriceWatch 记录作为查询索引，价格快照同时保存其 `detail_url`、服务商 `pricing_url`、数据 `last_updated`、本地 `retrieved_at`、促销标记和促销截止日期。界面按网站要求显示 ModelPriceWatch 归属链接。

ModelPriceWatch 的 `provider` 和模型名称不能脱离 listing ID 直接当作本机计费路由。受审查别名表使用 `(billing_host, billed_model_id, billing_mode, region) → modelpricewatch_listing_id` 作为键；同一键只能指向一个有效 listing，冲突时进入待定。

公开 API 未提供的计费维度，例如独立 cache-write 价格，必须由官方价格来源或有来源 URL 和生效日期的本地审核补充表提供。不自行推算缺失价格。`price_note` 等非结构化说明只作为审核提示，不由程序解析成计费规则；只要条目依赖尚未结构化支持的上下文档位、区域、批处理、促销或其他条件，就不得自动定价。

### 8.3 价格快照与降级

价格绑定到每个 Session 的用量记录，并作为不可变历史快照。价格目录更新只影响之后新接受的用量，不追溯重算旧 Session 成本。

历史 Session 在接受时没有价格的，允许日后补全，但只能使用在该 Session 计费时间已生效的可追溯历史价格，不得用查询时的当前价格倒填。历史查询选择 `price-history.json` 中不晚于计费时间的最近有效快照；若没有早于或等于计费时间的可验证基线，则保持待定。补全或纠错通过新的版本化快照和审计原因表示，不覆写原快照。

计费时间默认使用 Session `ended_at`。如果一个 Session 跨越价格变更边界，且用量不能按边界前后可靠拆分，则该 Session 进入 `ambiguous_billing_period`，不得用单一价格自动计算。

价格匹配优先级为：

~~~text
ModelPriceWatch 的 provider + model 精确匹配
    → 官方来源可追溯快照
    → 有来源的本地已确认补充
    → 待定
~~~

网络失败、限流或目录无匹配不得阻断 Session 扫描或投影。缓存年龄只按本地 `retrieved_at` 计算，ModelPriceWatch 的 `last_updated` 仅作为来源证据日期：不超过 24 小时的缓存是当前目录；超过 24 小时但不超过 7 天时可用作新 Session 的过期估算，但必须显示缓存年龄；超过 7 天的目录只能作为参考，不能创建新的已定价快照。

无可用价格或只有部分计费维度可定价时，逐维度显示缺失原因。数据合同保存 `known_subtotal_usd`、可空的 `total_cost_usd`、`pricing_complete` 和 `missing_billing_dimensions`；只有 `pricing_complete=true` 时才允许写入总成本。未知价格和未知成本均为 `null`，不得保存为数值零。

## 9. 端到端数据流

~~~text
Agent Session 来源
    → SourceAdapter 发现与解码
    → Observation Store（机器观察事实）
    → SessionView（单 Session 确定性物化视图）
    → conversation-chain-v1（私有可见问答/执行因果链）
    → ProjectView（项目级归并）
       ├─→ 项目演进投影
       ├─→ 问题归位规则 → problem-map-candidate-v1
       ├─→ session-index-v1
       ├─→ 用量记录 → 价格快照
       └─→ 受限只读事件查询

用户人工编辑 ──→ HumanPresentation ──┬─→ 项目演进闭环
                                      ├─→ 正式问题图
                                      └─→ 决策与约定
受限 AI 提炼   ──→ AgentAnnotation ──→ 待确认候选
                                              └─→ 用户确认 → HumanPresentation
~~~

正式项目语义的展示优先级为：

~~~text
HumanPresentation > 确定性 ProjectView
~~~

AgentAnnotation 在被确认前只出现在“待确认候选”区域，不参与正式项目语义的优先级计算。用户确认后会创建 HumanPresentation 条目，不再以 AgentAnnotation 身份覆盖项目。该优先级只适用于人类语义与展示字段，不能改写 Session 身份、时间戳、Token 计数、命令退出码或来源哈希等机器事实。

## 10. 失败与恢复

各子系统独立失败，不相互放大：

- Session 损坏：该 Session 记录为异常，其他 Sessions 继续处理。
- 来源消失：继续展示已保留索引与摘要，深层事件标记为不可访问。
- 列表或聚合超限：保留精确 coverage 计数，显示已展示、未展示和丢弃原因，不声称完整。
- AI 候选失败：不改变扫描世代、正式决策或同步状态。
- 价格查询失败：保留用量，使用已标注时效的缓存或进入待定状态。
- 价格模型歧义：禁止模糊自动匹配，需要审核别名或人工补充。
- Project/Vault 并发编辑：发布预像不一致时进入既有冲突处理，不覆盖人工内容。
- 插件或 CLI 版本过旧：发布绑定 minimum writer/reader 能力，不将新投影静默降级成旧格式。
- 问答链部分缺失：保留已捕获段和 coverage，明确显示断点，不把相邻 Session 文本强行拼成回答。
- 问题归位歧义：候选留在待归类区，不改变正式问题图，也不自动启动 Agent。
- 问题图冲突或成环：整个结构变更 CAS 失败，保留当前正式图和候选，返回旧路径、新路径及冲突修订。

## 11. 迁移与兼容

- v2/v3 已有人工目标、状态、风险、决策和演进节点原样保留，不重新推断其语义。
- v3 未经人工编辑的 `user_request` 占位演进节点按 4.2 的显式迁移规则重新分类；它们不因“已纳入索引”占位结果而被视为人工语义。
- 现有 v3 `recent-progress` 原子事件列表在下一次成功发布时从人类页面移除，对应观察事实仍在私有存储中可查。
- `项目历史.md` 继续作为无插件时的语义里程碑降级入口，不扩展为全量原子事件库。
- 新的 `session-index-v1` 使用独立隐藏合同，避免只因增加 Session 浏览能力就改写现有人类 Markdown 语义。
- 不支持 Session 索引的旧插件在 v2/v3 数据保持未迁移时仍可解析项目回顾和历史；新插件对缺失索引的旧项目提供重新扫描入口。
- 价格历史不因迁移或目录刷新被追溯重算。
- 新问题图不保存画布坐标；迁移只建立确认过的层级和源问答引用，无法确定父节点的旧问题进入待归类区。
- 人类 Markdown 的决策字段扩展使用新的 presentation schema；迁移到 v4 后，旧插件属于不受支持的只读组合，不保证能解析新 schema，也不得写入。两个 Markdown 仍可由用户作为普通文档阅读；若需要在升级前获得明确的不兼容提示，应先发布能够识别 `minimum_reader_version` 的桥接版插件。

## 12. 验证策略

### 12.1 单元与合同测试

- Session 索引稳定排序、身份唯一性、世代绑定和 coverage 统计；
- 四种处理状态严格分区、未知值不伪装为零、来源消失后索引累积保留；
- Session 索引容量超限时保留上一有效世代并失败关闭；
- 异常、未完成、来源消失和部分可读 Session 不被过滤；
- cursor 分页的首页、中间页、末页、超限 page size 和身份混用失败关闭；
- 决策候选不能绕过用户确认进入 HumanPresentation；
- 相同 dependency 集合的提炼幂等、失败不推进 watermark、过期候选不能确认；
- 决策替代链无环且保留旧条目；
- provider + model 精确价格匹配、别名歧义、多 host 路由、促销、过期缓存和未匹配状态；
- 分档、区域、批处理、跨价格边界和缺少 cache-write 等条件不能被静默简化；
- 价格快照在后续目录变更后保持字节不变；
- 未定价成本不被纳入“完整总成本”。
- 问答单元边界、连续多 Agent 消息、无回答、工具调用/结果归属和超长可见回答分块；
- 隐藏推理、系统/开发者指令和原始工具输出不能进入 conversation chain；
- 问题图单主父、无环、兄弟稳定排序、相关节点上限、状态正交和 source turn identity 唯一性；
- 归位建议主推荐、两个备选、两个相关节点、可读依据和置信等级的封闭合同；
- 同 dependency digest 重复整理不得启动 Agent，依赖变化后旧候选必须 stale；
- 闭环摘要只有具备明确里程碑资格时才进入项目演进，缺段不得生成占位结论。

### 12.2 集成与性能测试

- 单个 Session 含数千事件，可访问第一条、末条和中间页；
- 单项目含至少 154 个 Sessions，顶部总数与列表数量一致；
- 数万索引项使用虚拟列表，不一次创建全部 DOM 节点；
- Codex、Claude Code 和 OpenCode 混合项目按 namespaced identity 稳定归并，单一 provider 失败不影响其他来源；
- 网络断开、HTTP 429、超时和无匹配模型不影响扫描世代提交；
- Project/Vault 在扫描期间并发编辑时，预像检查拒绝覆盖人工修改；
- v2、现有 v3 和全新项目的迁移、重扫和恢复路径。
- 同一问题跨三个 provider、多个 Session 的显式引用拼接，以及只有语义相似时保持候选；
- 旧自动 `user_request` 节点升级、转入问题脉络、保留人工节点和重复迁移幂等；
- 普通零 Token 扫描的 Agent 子进程调用次数严格为零。

### 12.3 真实 Obsidian 验收

每次界面或合同修改后，都必须安装当前构建包到真实 Vault 并验证：

1. 五个标签顺序正确；
2. 项目演进默认简洁且可打开全部里程碑；
3. Session 总数、异常数、列表和最后一个 Session 一致；
4. 超长 Session 分页无静默缺口；
5. 决策空状态、人工新增、AI 候选、确认和忽略流程正确；
6. 价格日期、数据页、官方来源、促销或待定状态可读；
7. 插件重启、Vault 重开和同步后状态不丢失；
8. 无 CLI、无网络和来源消失的降级提示准确。
9. 第一个演进节点右侧可见 Agent 回答摘录、执行、验证和来源，或明确说明对应段未捕获；
10. 问题树左侧只显示真实问题及层级，待归类问题不会未经确认进入主树；
11. 键盘可以选择问题、展开闭环证据、确认归位和返回原 Session。

无 CLI 时，“全部 Sessions”仍必须显示 `session-index-v1` 中的完整清单和基础筛选；仅 Session 摘要、深层事件和分支/文件/错误搜索被禁用，并给出安装或配置 CLI 的单一恢复入口。

## 13. 验收条件

交付必须同时满足：

1. 用户可以在 Obsidian 中定位任何被扫描的 Session，包括最早、异常和来源不可用的 Session。
2. 任何长列表都显示总数和当前范围，不存在无提示的固定截断。
3. 项目首页和演进页不再显示大批原子消息 ID。
4. 零 Token 扫描不创建或改写正式决策。
5. AI 候选未经用户确认不得进入正式项目语义。
6. 无决策时界面清楚解释扫描与语义确认的边界。
7. 价格必须绑定实际 provider/host、模型、来源 URL 和日期；歧义或缺失不得自动猜测。
8. 历史费用使用接受时价格快照，后续价格刷新不修改旧记录。
9. 价格服务不可用时扫描仍成功，费用以“待定”降级而不是 `$0`。
10. v2/v3 已有人工内容、决策替代关系和历史价格不因升级丢失或被重算。
11. 已启用的 Codex、Claude Code 和 OpenCode Sessions 在同一五栏界面中具有相同的索引、下钻、状态和降级体验。
12. Session 来源消失后，其已接受索引和摘要仍可见；恢复来源后，新世代能重新关联而不产生重复 Session。
13. 项目演进只包含合格里程碑；右侧闭环摘要按“触发问题、Agent 结论、执行与变更、结果与验证、项目影响与后续”展示并可追溯。
14. 默认 Agent 结论来自可见回答的确定性摘录；普通扫描不调用 Agent，AI 整理未经确认不能进入正式表现。
15. 正式问题图跨 Sessions 和 provider 合并，但每个节点只有一个主父节点，模糊归位只进入待确认候选。
16. `execution_verified` 不自动把问题标为 `resolved`；问题解决需要用户确认或已接受验收事实。
17. v3 自动占位节点迁移可预览、可核对、幂等且不覆盖任何人工编辑。

## 14. 非目标

- 不把全部原始 Session 文本或完整工具输出复制到 Vault。
- 不让零 Token 规则自动推断意图、理由或正式决策。
- 不默认为每个 Session 调用 AI 生成摘要。
- 不将 ModelPriceWatch 价格视为用户真实账单或不可复核的唯一真相。
- 不在本设计中实现实际账单对账、订阅额度扣减或企业合同价。
- 不增加用户可见的项目文档数量。
- 不保存、分析或展示隐藏推理、系统提示词或开发者指令。
- 不把每个问答单元都提升为项目演进节点。
- 不允许自由拖拽直接改写问题层级；结构变更必须经过明确动作和 CAS 确认。

## 15. 建议实施边界

实施计划先完成合同与基线 Gate 0，再分成可独立验证的六组：

0. 固定 0.3.5 v3 实施基线，落地 schema、CLI、状态机、版本矩阵和迁移夹具；

1. `session-index-v1` 生成、发布、同步和只读分页查询；
2. Obsidian 五视图顺序、全部 Sessions 列表与超长 Session 下钻；
3. 决策与约定的空状态、人工新增、AI 候选与确认转换；
4. ModelPriceWatch 目录缓存、精确匹配、价格快照和用量卡片状态。
5. `conversation-chain-v1`、跨 Session 因果拼接和项目演进闭环摘要；
6. 正式问题图、`problem-map-candidate-v1`、归位建议和待确认交互。

六组共享 Gate 0 固定的合同与世代身份，不得在 UI 实施过程中临时改变核心 schema。每组都必须在进入下一组前通过单元测试、集成测试和针对性真实 Obsidian 验收。本文件是总设计；六组分别使用独立实施计划，不合并成一个难以评审和回滚的巨型计划。

依赖顺序固定为：扩展 Gate 0 完成后，Session 索引发布与会话因果链可以独立实施；问题脉络依赖会话因果链；“全部 Sessions”最终界面整合依赖 Session 索引和问题脉络；决策与约定依赖五 Tab shell；价格服务依赖 Session 索引，价格 UI 依赖五 Tab shell。未满足前置门禁时不得用占位数据宣称对应功能完成。

## 16. 实施基线与版本合同

### 16.1 基线

项目所有者在实施前确认使用远端最新发布标签 `0.3.5`（`ea5b1ba`）的零 Token v3 架构作为唯一实现基线，以保留 0.3.1–0.3.5 的扫描、Windows 和发布恢复修复。原工作区中的回退、删除或未完成跨版本修改保留原状；实现只在隔离分支 `codex/obsidian-context-v4` 中进行，不覆盖这些既有修改。

Gate 0 固定以下版本边界：

- `review-presentation-v4`：两个可读 Markdown、扩展后的决策字段和对应 HumanPresentation patch；
- `machine-ledger-v4`：现有 v3 机器账本的后继版本，保存 presentation 基线、内嵌价格快照、当前快照引用和所有同步哈希；
- `session-index-v1`：完整 Session 紧凑索引；
- `session-summary-v1`：单 Session 的确定性、已脱敏详情响应；
- `session-event-page-v1`：Observation Store 的分页读取响应；
- `agent-annotation-v1`：私有候选决策、约定或里程碑结论摘要及其提炼运行状态；
- `pricing-snapshot-v1`：不可变价格快照，作为 `machine-ledger-v4` 的受校验成员。
- `pricing-supplement-v1`：人工补价/纠错的受限标准输入合同；服务端计算费用，不持久化调用方提交的计算结果。
- `conversation-chain-v1`：私有、provider-neutral 的单 Session 问答单元与执行因果链，绑定 SessionView dependency digest；不写入 Vault。
- `problem-map-candidate-v1`：私有问题归位候选与可复核规则依据；未经确认不进入 HumanPresentation。
- `review-presentation-v4` 增加正式 `problem_nodes[]`、问题图修订和演进 `closed_loop`；继续使用两个 Markdown，不增加第三个用户可见文档。

每个持久化合同必须同时提供 JSON Schema、Go 运行时校验、TypeScript 解析器、有效/无效 fixture 和规范化字节测试。新增字段不得只依赖 TypeScript 类型或 UI 判空。由于这些合同不在已完成的八组 fixture 中，原 Gate 0 本地证据不再覆盖当前完整设计；必须扩展并重跑 Gate 0，Windows 原生 CI 仍需在推送后单独验证。

### 16.2 Provider 范围

上述合同必须是 provider-neutral：`provider` 使用受限 safe ID，不在通用 schema 中写死为 `codex`。已启用的 Codex、Claude Code 和 OpenCode SourceAdapter 使用相同的 Session 索引、状态、分页和 Obsidian 表现合同；某个 provider 尚未安装或不兼容时，以来源级诊断呈现，不能让其他 provider 的 Session 消失。

本设计不重新定义三种 SourceAdapter 的解码细节。若实施基线尚未包含 Claude Code 或 OpenCode Adapter，它们是对应端到端验收的前置工作，不能通过在 UI 中显示 provider 名称冒充同等支持。

## 17. 持久化合同

### 17.1 `session-index-v1`

顶层至少包含：

~~~text
schema_version = 1
minimum_reader_version
digest
project_id
generation_id
project_view_digest
generated_at
sort_version
coverage
sessions[]
~~~

`digest` 是对“省略 digest 字段后的规范 JSON 字节”计算的 SHA-256，避免自引用；数组顺序、空值和数字格式都进入规范化合同。

`coverage` 固定包含：

~~~text
total
complete
partial
error
unprocessed
source_available
source_unavailable
started_at_known
ended_at_known
usage_known
~~~

强制不变量：

~~~text
complete + partial + error + unprocessed = total
source_available + source_unavailable = total
len(sessions) = total
~~~

每个 `sessions[]` 索引项至少包含：

~~~text
provider
session_id
processing_state
state_reason_codes[]
source_availability
source_terminal_state | null
started_at | null
ended_at | null
duration_ms | null
warning_count
record_count | null
indexed_event_count
coverage { seen, indexed, collapsed, unprojected, undecodable, truncated }
fact_counts { file_change, command, verification, error, artifact }
session_view_digest | null
usage_record_digest | null
summary_digest | null
last_seen_generation_id | null
last_successful_generation_id | null
~~~

索引身份是 `(project_id, provider, session_id)`。`session_id` 只需在同一 provider 内唯一，界面和所有 CLI 命令始终同时携带 provider。相同身份在新世代中形成新索引修订，不改写旧世代的规范字节。

`started_at` 和 `ended_at` 只有在来源真实提供时才写入字符串；不可知时必须为 `null`，不得从另一时间、扫描时间或迁移时间补写。`started_at_known` 和 `ended_at_known` 分别只统计非 `null` 值；规范排序为 `started_at desc nulls last, provider asc, session_id asc`。

所有数组有明确最大项数，所有字符串有 UTF-8 字节上限。`state_reason_codes` 只能使用版本化枚举；用户可见说明由插件本地化，机器文件中不保存任意错误文本。

### 17.2 Session 摘要

`session-summary-v1` 不写入 Vault，由 CLI 从当前 SessionView 和其依赖生成。它包含：

- Session 身份、generation ID 和 SessionView digest；
- 阶段边界；
- 关键操作；
- 验证结果；
- 类型化错误；
- 未解决问题；
- 每个区块的 coverage 和来源 revision IDs。

每个区块最多 32 项，每项正文最多 512 UTF-8 字节，按 `occurred_at asc, sequence asc, revision_id asc` 稳定排序。超出部分保存总数和未展示数。摘要只能使用确定性规则和受限脱敏 excerpt；不得生成原因、意图或未被事实支持的“下一步”。规则 ID、规则版本和依赖摘要进入响应，以便重现。

### 17.3 会话因果链与问题图合同

`conversation-chain-v1` 是私有派生记录，按 `(project_id, provider, session_id, session_view_digest)` 唯一绑定，至少包含：

~~~text
schema_version
project_id
provider
session_id
session_view_digest
dependency_digest
segmentation_rule_version
coverage { source_messages, captured_messages, turn_units, unanswered_units, truncated_messages }
turn_units[] {
  turn_unit_id
  ordinal
  started_at
  ended_at | null
  user_message { revision_id, source_ref, occurred_at, visible_excerpt, truncated }
  assistant_messages[] { revision_id, source_ref, occurred_at, visible_excerpt, truncated }
  actions[] { revision_id, kind, tool_name | null, excerpt }
  results[] { revision_id, kind, verification_state, excerpt }
  answer_state
}
~~~

可见用户和 Agent 摘录最多 4,096 UTF-8 字节；超限必须设置 `truncated=true`、增加 `truncated_messages` 并在 UI 明示。需要正文时，受限查询通过 `source_ref` 调用 SourceAdapter 的认证读取能力，每条最多 64 KiB，读取后只解码对应可见 user/assistant 正文并再次脱敏，不保存查询结果。工具调用和结果只保存受限脱敏 excerpt 与 revision ID，不保存原始高熵输出。链的生成完全确定性；任一输入 revision、规则版本或脱敏版本变化都必须改变 dependency digest。

`review-presentation-v4.timeline[].closed_loop` 至少包含触发问题、结论表现类型、结论正文、执行摘要、验证摘要、项目影响、后续、`source_turn_refs[]` 和分段 coverage。`conclusion_kind` 固定为 `visible_answer_excerpt|human_confirmed|ai_candidate_confirmed|missing`；`missing` 时正文必须为空并提供类型化缺失原因。只有 `human_confirmed` 和 `ai_candidate_confirmed` 可表达不能从可见原文直接得出的语义。

`agent-annotation-v1` 增加 `annotation_kind=decision_candidate|agreement_candidate|milestone_conclusion_candidate` 和通用的 `confirmed_entity_id|null`。里程碑结论候选必须引用目标 milestone ID、source turn dependencies 和 prompt schema version；确认时只 patch 对应 `closed_loop.conclusion` 并将 `conclusion_kind` 设为 `ai_candidate_confirmed`，不得顺带修改验证、影响、下一步或问题状态。

`review-presentation-v4.problem_nodes[]` 使用 5.2 的正式节点字段。图校验必须证明：ID 唯一、父节点存在、无环、根节点集合与空父节点一致、每个相关节点存在且不自指、相关节点不超过两个、source turn refs 存在于当前或保留的 chain dependency 中、同级 `sibling_order` 唯一且稳定。

`problem-map-candidate-v1` 是私有 CAS 记录，至少包含：

~~~text
candidate_id
project_id
question
source_turn_refs[]
recommended_relation = child | sibling | merge | keep_pending
recommended_target_id | null
alternate_target_ids[]
related_node_ids[]
grounds[] { rule_id, rule_version, matched_fact_refs[], explanation }
confidence = high | medium | low
status = pending | applied | merged | kept_pending | stale | dismissed
dependency_digests[]
analysis_mode = deterministic | agent_requested
agent_run_id | null
revision
created_at
updated_at
~~~

备选节点和相关节点各不超过两个。`analysis_mode=deterministic` 时 `agent_run_id` 必须为 `null`；`agent_requested` 必须引用一个受限 Agent run。相同排序 dependency digests、规则版本和问题规范化文本产生同一候选身份，重复整理不得重复调用 Agent。

### 17.4 文件所有权与发布

| 产物 | 权威写入方 | Project | Vault | 人工可编辑 | 发布事务 |
|---|---|---:|---:|---:|---:|
| `项目回顾.md` | HumanPresentation/同步引擎 | 是 | 是 | 是 | 是 |
| `项目历史.md` | HumanPresentation/同步引擎 | 是 | 是 | 是 | 是 |
| `.session-reviewer/ledger.json` | 受信 CLI | 是 | 是 | 否 | 是 |
| `.session-reviewer/session-index.json` | 扫描投影器 | 是 | 是 | 否 | 是 |
| Observation Store | 扫描引擎 | 私有 | 否 | 否 | 扫描世代事务 |
| Conversation Chain Store | 确定性链生成器 | 私有 | 否 | 否 | 扫描世代事务 |
| AgentAnnotation Store | 受限语义候选服务 | 私有 | 否 | 否 | 独立 CAS |
| Problem Map Candidate Store | 归位建议服务 | 私有 | 否 | 否 | 独立 CAS |
| 全局价格目录缓存 | 价格服务 | 平台用户缓存 | 否 | 否 | 原子缓存刷新 |

一次扫描发布的原子集合是两个 Markdown、`ledger.json` 和 `session-index.json`。journal 必须保存四者的目标哈希、预像哈希、临时文件和恢复阶段；任一写入、同步或发布后校验失败时，不能暴露混合世代。

人工编辑或候选确认的发布集合是两个 Markdown 和 `ledger.json`；事务开始前必须验证 `session-index.json` 仍绑定预期 generation，但无需重写其规范字节。普通 Project/Vault sync 比较和验证全部四个文件，机器文件仍只允许 Project 权威副本单向发布。

Conversation Chain Store、AgentAnnotation Store、Problem Map Candidate Store 和全局价格目录缓存不属于 Project/Vault 同步集合。问题归位、决策候选或里程碑结论候选确认会创建 HumanPresentation patch，随后才通过正常发布事务进入 Markdown 和 ledger。价格目录只是输入缓存；一旦价格被接受，`pricing-snapshot-v1` 作为 `machine-ledger-v4.pricing_snapshots[]` 成员随机器账本发布，之后不依赖缓存继续存在。

## 18. 状态机与 CLI 合同

### 18.1 Session 状态机

~~~text
discovered/unprocessed
    ├─→ complete
    ├─→ partial
    └─→ error

source_available ⇄ source_unavailable
~~~

处理状态是某个世代的结果，不在同一世代原地回退；重扫产生新世代。来源可用性可以在保留旧处理结果的前提下变化。失败或取消且未成功发布的扫描不改变当前有效索引。

### 18.2 决策候选状态机

候选状态固定为：

~~~text
pending ─→ confirmed
   ├─────→ ignored ─→ pending
   ├─────→ not_decision
   └─────→ stale
ignored ────────────→ stale
~~~

- `confirmed`、`not_decision` 和 `stale` 是该候选修订的终态；
- 确认后创建或 patch 对应 HumanPresentation 实体，候选只保存通用 `confirmed_entity_id`，不再参与正式展示优先级；决策/约定候选创建新条目，里程碑结论候选只能修改目标闭环的结论字段；
- 对正式决策的后续修改创建 HumanPresentation 新修订，不回写候选正文；
- `ignored` 默认隐藏但允许用户恢复；
- `not_decision` 持久保留，阻止相同提炼运行再次提出同一候选；
- 引用的 SessionView dependency 不再是活动修订时，未确认候选转为 `stale`，必须基于新依赖重新提炼后才能确认。

“新 Sessions”按 SessionView dependency digest 判断，不按文件修改时间判断。一次提炼运行的稳定身份由 `project_id + extractor_version + prompt_schema_version + 排序后的新 SessionView digests` 计算；同一身份重复点击返回原运行，不重复调用 Agent。只有成功保存候选后才推进 `last_successful_extraction_dependencies`；失败、取消或格式校验失败均不推进 watermark。

正式决策在 `review-presentation-v4` 中至少包含：

~~~text
id
kind = decision | agreement
occurred_at
title
rationale
impact
status = active | superseded | archived | legacy_unmapped
legacy_status_text | null
reevaluate_when
supersedes[]
milestone_ids[]
session_refs[] { provider, session_id }
provenance = human_created | migrated | ai_candidate_confirmed
pinned
revision
~~~

替代关系必须无环；`status=superseded` 时至少存在一个后继条目直接引用该条目，后继自身可以在以后继续被替代。原生 v4 决策只使用 `active|superseded|archived`，且 `legacy_status_text=null`。v3 状态只有在原文精确为 `active` 或 `archived` 时映射到同名 v4 状态；其他任意原文（包括空串、`superseded` 和人类语言文本）都表示为 `status=legacy_unmapped`、`provenance=migrated`，并在 `legacy_status_text` 中逐字保存原值。`legacy_unmapped` 必须携带非 `null` 的 `legacy_status_text`，不允许用于原生决策。迁移无法恢复的其他新增字段使用空值、空数组或 `false`，不得推断理由或关系。

### 18.3 问题节点与归位候选状态机

正式问题节点的两个状态维度正交：

~~~text
workflow_state: not_started ⇄ in_progress ⇄ paused → resolved
answer_state:   no_answer → answered_unverified → execution_verified
~~~

重新打开已解决问题需要显式人工操作并创建新修订；新的执行证据可以提升 `answer_state`，但不得自动设置 `workflow_state=resolved`。归位候选状态固定为：

~~~text
pending ─→ applied
   ├─────→ merged
   ├─────→ kept_pending ─→ pending
   ├─────→ dismissed
   └─────→ stale
kept_pending ───────────→ stale
~~~

`applied` 表示已按 child 或 sibling 关系写入正式图；`merged` 必须把候选的全部 source turn refs 并入目标节点；`stale` 表示任一依赖、目标节点修订或规则版本变化。任何应用、移动、合并或排序操作都验证 presentation 预像、问题图修订和候选修订，失败时不产生部分写入。

### 18.4 只读 CLI

插件只允许以 `shell=false` 和固定参数数组调用以下只读合同：

~~~text
session-reviewer inspect session-summary
  --project-id <id> --provider <id> --session-id <id>
  --expected-generation-id <id> --json

session-reviewer inspect session-events
  --project-id <id> --provider <id> --session-id <id>
  --expected-generation-id <id>
  [--cursor <opaque> | --anchor <one-based-ordinal>]
  --limit <1..100> --json

session-reviewer inspect session-search
  --project-id <id> --expected-generation-id <id>
  --query-kind <branch|file|error> --query <bounded-text>
  [--cursor <opaque>] --limit <1..100> --json

session-reviewer decisions candidates list
  --project-id <id> [--status <enum>] --json

session-reviewer evolution summary-candidates list
  --project-id <id> --milestone-id <id> [--status <enum>] --json

session-reviewer inspect conversation-chain
  --project-id <id> --provider <id> --session-id <id>
  --expected-generation-id <id>
  [--turn-unit-id <id>] [--message-cursor <opaque>]
  --limit <1..64> --json

session-reviewer problems candidates list
  --project-id <id> [--status <enum>] --json
~~~

`session-search` 只返回匹配的 `(provider, session_id)`、命中类型、总数和分页 cursor；`query` 最大 256 UTF-8 字节，只参与规范化文本匹配，永远不作为文件系统路径解析。无 CLI 时插件禁用分支、文件和错误特征筛选，同时保留索引内的日期、来源和状态筛选。

`session-event-page-v1` 返回：

~~~text
schema_version
project_id
provider
session_id
generation_id
session_view_digest
total
range_start
range_end
items[]
previous_cursor | null
next_cursor | null
first_cursor
last_cursor
coverage
~~~

当 `total=0` 时四个 cursor 均为 `null`，范围为 `0–0`；`--anchor` 小于 1 或大于 total 时返回 `anchor_out_of_range`，不得自动夹取到另一页。

事件项只包含类型化字段、有限脱敏 excerpt、revision ID、sequence 和 occurred_at。CLI 不返回原始系统/开发者指令、隐藏推理、令牌、绝对路径或未脱敏工具输出。cursor 最大长度、响应最大字节数和执行超时必须进入合同测试。

`conversation-chain` 默认返回问答单元索引和有限摘录；指定 `--turn-unit-id` 后按需从认证 source refs 读取该单元的可见人类/Agent 正文、动作和结果。`--message-cursor` 只用于同一问答单元的后续可见消息，绑定 project、provider、session、generation、turn unit、脱敏版本和 limit；绑定不符返回 `stale_cursor`。每条源读取仍受 64 KiB 上限和总响应上限约束，超限必须返回 coverage。即使私有源包含其他角色，该命令也只能返回 user/assistant 可见正文和受限工具摘要。

### 18.5 写入与异步 CLI

写操作固定为：

~~~text
session-reviewer decisions create
  --project-id <id> --expected-review-sha256 <digest> --json

session-reviewer decisions extract
  --project-id <id> --expected-generation-id <id> --json

session-reviewer decisions extract status
  --job-id <id> --json

session-reviewer decisions extract cancel
  --job-id <id> --expected-revision <n> --json

session-reviewer decisions candidate transition
  --project-id <id> --candidate-id <id>
  --expected-revision <n>
  --action <confirm|ignore|not_decision|restore>
  --expected-review-sha256 <digest> --json

session-reviewer pricing supplement
  --project-id <id> --provider <id> --session-id <id>
  --usage-record-digest <digest>
  --expected-ledger-sha256 <digest> --json

session-reviewer evolution summarize
  --project-id <id> --milestone-id <id>
  --expected-generation-id <id> --json

session-reviewer evolution summary-candidate transition
  --project-id <id> --milestone-id <id> --candidate-id <id>
  --expected-candidate-revision <n> --expected-review-sha256 <digest>
  --action <confirm|ignore|restore> --json

session-reviewer problems candidate transition
  --project-id <id> --candidate-id <id>
  --expected-candidate-revision <n> --expected-problem-map-revision <n>
  --expected-review-sha256 <digest>
  --action <apply_child|apply_sibling|merge|keep_pending|dismiss|restore>
  [--target-problem-id <id>] --json

session-reviewer problems move
  --project-id <id> --problem-id <id> --new-parent-id <id|root>
  --expected-problem-map-revision <n> --expected-review-sha256 <digest> --json

session-reviewer problems reorder
  --project-id <id> --parent-id <id|root>
  --expected-problem-map-revision <n> --expected-review-sha256 <digest> --json
~~~

`create`、带编辑内容的 `confirm` 和 `pricing supplement` 从标准输入读取最大 64 KiB 的版本化 JSON，不接受用户指定文件路径。补价输入使用 `pricing-supplement-v1`，必须完整声明计费路由、适用时间、可空费率、来源 URL、审计理由，以及可选的 `supersedes_snapshot_id`；服务端重新计算 billable quantities、line costs、subtotal 和 total，拒绝插件直接提交计算结果。`extract` 使用 SessionReviewer 已配置并验证的 proposal-only Agent，不接受 Markdown、候选正文或插件传入的任意可执行文件；它返回 job ID，状态查询复用现有受限异步任务模式。

所有写命令在修改前重新验证 project、generation、candidate revision 和 review 预像。CAS 失败返回当前摘要和类型化错误，不覆盖较新的扫描或人工编辑。任何候选或价格失败都不得推进扫描 generation。

`problems reorder` 从标准输入读取该父节点下每个直接子节点 ID 恰好一次的完整有序数组；缺失、重复、外来节点或并非直接子节点均拒绝。`problems move` 在应用前返回旧路径、新路径和受影响子树摘要供插件确认；服务端重新验证无环和 related-node 上限。界面不得用自由拖拽绕过这些命令。

### 18.6 价格状态机与快照

价格解析状态固定为：

~~~text
pending
current
promotion
stale_estimate
manual_supplement
ambiguous
legacy_unverified
superseded
~~~

`pricing-snapshot-v1` 至少包含：

~~~text
snapshot_id
project_id
provider
session_id
usage_record_digest
billing_host
billed_model_id
billing_mode
region | null
priced_at
created_at
status
modelpricewatch_listing_id | null
source_kind = modelpricewatch | official | manual | unresolved
source_url | null
detail_url | null
source_last_updated | null
retrieved_at | null
promo
promo_until | null
rates { input, cached_input, cache_write_input, output, reasoning_output }
billable_quantities { input, cached_input, cache_write_input, output, reasoning_output }
line_costs_usd { input, cached_input, cache_write_input, output, reasoning_output }
missing_billing_dimensions[]
known_subtotal_usd
total_cost_usd | null
pricing_complete
supersedes_snapshot_id | null
audit_reason
~~~

每个 rate 和 line cost 都是可空值；公开免费价格使用数值 `0`，未知使用 `null`。Token 原始计数不必天然互斥，因此每个 provider 的 UsageAdapter 必须显式生成互斥的 `billable_quantities`，并记录计费维度映射规则版本。例如 reasoning output 是否按 output 计费，只能由受审查的 provider 规则声明，不能跨 provider 默认套用。

两个价格目录响应分别设置 128 MiB 下载与解析上限，要求成功 HTTP 状态、JSON content type、受支持 schema、无重复字段和完整响应体；使用平台私有权限目录、进程锁和原子替换保存。刷新失败时保留上一份已验证缓存，不用半文件覆盖，也不把失败时间写成新的 `retrieved_at`。

目录刷新不修改快照。补价或纠错创建新快照，并通过 `supersedes_snapshot_id` 指向旧快照；聚合只选择每条用量的最新有效快照，但审计视图可以查看完整链。`machine-ledger-v4` 必须对整本账本验证价格替代图：前驱必须存在，不得自指、成环或分叉；前驱与后继必须共享完全相同的 `(provider, session_id, usage_record_digest)`；有后继的快照必须为 `superseded`，`superseded` 也必须有后继。`current_pricing_snapshot_ids` 只能选择非 `superseded` 叶子，每个用量身份最多一个；一旦某身份被选中，该身份不得还存在另一个断开的有效叶子。未选中的历史快照仍是不可变审计证据；未知费率和成本继续用 `null`，不得因图校验升级为零。ModelPriceWatch、官方来源和人工补充的优先级不覆盖适用条件检查：任何条件不明都先进入 `pending` 或 `ambiguous`。

## 19. 兼容与迁移矩阵

| 项目数据 | CLI | 插件 | 行为 |
|---|---|---|---|
| v2 | 旧 | 旧 | 保持现有两文档体验，不出现新能力 |
| v2 | 新 | 新 | 先提供迁移 dry-run；成功原子迁移到 presentation/ledger v4 后启用完整能力 |
| v3 | 旧 | 新 | 可读现有回顾、历史和账本；新标签显示“CLI 版本过旧”，不显示零 Sessions 或零成本 |
| v3 | 新 | 旧 | 新 CLI 可继续只读和同步 v3；升级到 v4 必须显式执行迁移。迁移后旧插件不受支持，Markdown仍可人工阅读 |
| v3 | 新 | 新 | 先提供显式迁移 dry-run；确认后建立 session-index-v1 并原子升级到 v4，已有 HumanPresentation 原样保留 |
| v4 | 旧 | 任意 | 旧 CLI 检测 minimum writer/reader 后失败关闭，不写文件 |
| v4 | 新 | 旧 | 不受支持且必须保持只读；能识别 minimum reader 的桥接版显示不兼容提示，其他旧版只保证不会通过 CLI 写入 |
| v4 | 新 | 新 | 完整读写、扫描、候选确认、价格快照和同步能力 |

v3 → v4 使用以下显式迁移合同：

~~~text
session-reviewer sync --dry-run
  [--project-id <id>] [--data-dir <path>] --json

session-reviewer sync --confirm-migration
  --expected-preview-digest <digest>
  [--project-id <id>] [--data-dir <path>] --json
~~~

dry-run 返回版本化迁移预览、将保留或补默认值的语义单元、四文件目标哈希和 `preview_digest`，不写 Project、Vault 或发布指针。确认命令必须在锁内重新生成预览并核对 digest；任一源文件、SessionView dependency、当前 generation 或目标预像变化都返回 `migration_preview_stale`，不得套用旧预览。普通 `sync` 不得静默执行 v3 → v4 迁移。

迁移必须满足：

1. dry-run 列出将新增、升级和保留的合同，不写文件；
2. v2/v3 决策的标题、理由、影响、稳定 ID 和现有来源关系逐字节保留；状态原文若不能按上述封闭规则精确映射，用 `legacy_unmapped + legacy_status_text` 无损表示；
3. 新字段只填显式默认值，不由机器补写理由、重评条件或替代关系；
4. v3 `recent-progress` 仅在四文件新世代成功发布后从人类页面移除；
5. 旧价格保留为迁移快照，无法证明来源或日期时标为 `legacy_unverified`，不重算；
6. 迁移备份和 journal 遵循既有私有路径、原子替换和恢复规则；
7. 迁移后连续两次 render、sync 和重启不得产生字节、哈希或 revision 漂移。
8. dry-run 分别列出自动 `user_request` 节点中升级为里程碑、转入问题脉络、保留人工编辑和无法归类的稳定 ID；确认迁移后才移除旧占位文案。
9. 迁移建立的正式问题节点不推断父子关系；只有明确旧层级或人工确认关系可以进入主树，其余进入待归类候选。
10. `conversation-chain-v1` 从当前 SessionView dependencies 重建，不把旧 Markdown 的摘要反向当成 Agent 原文。

Gate 0 完成标准是：上述所有 schema、状态枚举、CLI allowlist、兼容 fixture 和失败码均已固定并通过合同测试。原八组 fixture 的本地通过记录仍是历史证据，但不能覆盖新增合同；扩展 Gate 0 和 Windows 原生 CI 通过后，才能进入六个功能实施计划。
