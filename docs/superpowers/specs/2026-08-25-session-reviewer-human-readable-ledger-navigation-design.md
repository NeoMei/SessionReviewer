# SessionReviewer 人类可读项目首页与演进导航设计

## 1. 背景与问题

SessionReviewer 已能把 accepted ledger 写入项目目录并同步到 Obsidian，也已经生成 `diagrams/project-evolution.md`。但当前产品存在两个直接影响回顾效率的问题：

1. `project-overview.md` 只显示项目名和用量统计，没有指向演进图、当前状态、决策、待办或 Session 的明显入口；用户因此很难发现已经存在的图。
2. `decisions/`、`open-loops/` 和 `sessions/` 直接暴露按稳定 ID 命名的实体文件，缺少目录解释、中文索引和快速摘要。用户必须理解内部数据模型并逐个打开文件，才能知道项目为什么演进到当前状态。

本设计把 `project-overview.md` 改造成唯一项目首页，使用有界的 Mermaid 恢复主线，并为三个细分目录生成中文索引和面向人的快速摘要。目标是让用户隔几天重新打开项目后，在十秒内回答：项目目标是什么、为什么这样设计、已经验证了什么、现在处于什么状态、下一步做什么。

## 2. 设计原则

- **一个明显入口**：普通回顾从 `project-overview.md` 开始，不要求用户先理解目录结构。
- **先结论后证据**：首页、索引和详情页先呈现白话结论，完整证据与技术字段继续保留。
- **有界可读**：首页图和最近变化不会随项目历史无限增长。
- **确定性派生**：CLI 只从 accepted ledger 生成页面，不调用模型，不创造新决策或结论。
- **双端可用**：项目目录和 Obsidian 中看到相同首页、图和索引。
- **可移植**：使用标准 Markdown 相对链接和 Mermaid；不依赖 Obsidian 专属 Wiki 链接。
- **明确编辑边界**：语义实体仍可双向编辑；首页仪表盘、图、索引和快速摘要属于生成器拥有的派生区域。

## 3. 用户体验与页面结构

### 3.1 唯一项目首页

`docs/session-review/project-overview.md` 保持现有稳定实体身份，新增生成器拥有的仪表盘区域。页面按以下顺序显示：

1. 当前项目目标。
2. 最后验证状态与下一步。
3. A1 Mermaid 项目演进主线。
4. 当前开放风险、阻塞和开放待办。
5. 最近三项关键变化。
6. 项目总耗时、Token、成本和模型占比。
7. 当前状态、完整时间线、决策索引、待办索引和 Session 索引的标准 Markdown 链接。

首页不删除用户自定义的未知章节。生成器只替换明确拥有的章节，并继续遵守现有 frontmatter 与 revision 合同。

生成器拥有的章节使用固定、可验证的 ownership marker。它们不进入可编辑语义单元集合：同步引擎发现这些区域被手工修改时，使用 accepted ledger 重新生成，而不是把修改当作人类语义编辑、提升 revision 或写入 Base。用户自定义的其他未知章节仍按现有合同保留和合并。

### 3.2 A1 Mermaid 恢复主线

首页直接渲染固定五层主线：

```text
项目目标 → 关键决策汇总 → 最近已验证里程碑 → 当前状态 → 下一步
```

图使用 `flowchart LR`。每一层是一个高层节点，因此主图固定为五个节点，不会因决策或 Session 增长而失控：

- **项目目标**：来自 `CurrentState.Goal`。
- **关键决策汇总**：在一个节点中列出 accepted decisions 的中文标题；最近 timeline events 引用的决策优先，其余按标题和稳定 ID 排序。达到显示预算后显示前若干项和“另有 N 项”，完整内容通过决策索引查看。
- **最近已验证里程碑**：列出最近最多三条 verified timeline events。
- **当前状态**：来自 `CurrentState.LastVerified`。
- **下一步**：优先使用 `CurrentState.NextAction`，并显示开放待办数量。

节点文本使用确定性长度预算和 Mermaid 转义。任何单个字段过长都只影响显示摘要，不改变 accepted ledger 原值。

`diagrams/project-evolution.md` 继续存在，包含同一 A1 主线以及完整时间线/关系附录，供深度复盘使用。首页必须直接包含 Mermaid 代码块，而不是只提供隐藏在子目录中的链接。

### 3.3 细分目录索引

以下派生文件固定置顶：

- `decisions/00-目录说明.md`
- `open-loops/00-目录说明.md`
- `sessions/00-目录说明.md`

每个索引首先用中文解释“这个目录记录什么、什么时候需要看、哪些内容可以编辑”，然后生成标准相对 Markdown 链接清单。

决策索引每项显示：

- 中文标题；
- 状态的中文显示值；
- 标签；
- 从现有 rationale/context 确定性提取的一句话理由。

待办索引每项显示：

- 中文标题；
- 状态的中文显示值；
- 阻塞原因；
- 下一实验或下一动作。

Session 索引每项显示：

- Session 日期或结束时间；
- 初始目标；
- 最后一个交互阶段的标题与摘要；
- 耗时、Token 和成本；
- 指向完整 Session 报告的链接。

索引中的链接文字使用人类可读标题。底层文件可以继续使用稳定 ID 文件名，用户无需从文件名理解其含义。

### 3.4 详情页快速理解区

生成器在现有详情页前部维护一个有界的“快速理解”区域，不增加新的语义 schema 字段：

- 决策：结论、中文状态、为什么重要。
- 待办：问题、中文状态、当前阻塞、下一实验。
- Session：初始目标、最近阶段、验证摘要、耗时、Token、成本。

快速理解内容只由实体已有字段确定性派生。完整 narrative、evidence、frontmatter 和用户自定义章节继续保留。快速理解区使用明确的生成器标记，手工修改会在下一次 accepted render 中被覆盖。

## 4. 生成、同步与编辑边界

### 4.1 语义实体

`project-overview.md`、`current-state.md`、timeline、decisions、open loops 和 session reports 继续遵守现有 accepted ledger、revision、evidence 和 Base/Project/Vault 三方合并合同。用户可以在项目端或 Obsidian 端编辑允许编辑的语义单元。

### 4.2 派生内容

以下内容由生成器拥有，重新生成它们不会提升语义实体 revision：

- 首页仪表盘章节和 A1 Mermaid 代码块；
- `diagrams/project-evolution.md`；
- 三个 `00-目录说明.md`；
- 各详情页的“快速理解”区域。

独立派生文件带有醒目的中文提示，说明手工编辑会被覆盖。派生文件和派生区域不作为新语义事实来源。

详情页快速理解区和首页仪表盘区使用同一 ownership-marker 合同。文档解析器在计算可编辑单元、验证 human changes 和执行三方合并时，将这些区域视为可重建的保留区域；修改、删除或伪造 marker 都不能绕过 reserved-field 检查或变成 accepted 语义内容。

### 4.3 发布到 Obsidian

独立派生文件不进入普通实体扫描与三方合并，避免被当作缺少实体 frontmatter 的 malformed 文档。同步引擎增加独立的派生文件发布阶段：

1. 只有本轮普通实体 reconcile 没有 conflict、issue 或 entity error 时，才从最终项目 ledger 重新加载完整状态并准备派生计划；部分失败时保留上一套派生内容并明确报告其未刷新。
2. 从已验证的最终项目 ledger 生成预期派生字节；所有文件在任何写入前完成渲染、预算和路径验证。
3. 使用现有根目录固定、路径验证、原子写和内容校验机制写入 Project 与 Vault 的对应位置。
4. 验证两端哈希一致后才报告派生发布成功。
5. 发布失败时返回稳定非零诊断，不把部分成功误报为完整同步成功。

普通实体同步不从 Obsidian 的派生文件反向导入语义。用户需要改变派生结果时，应编辑对应的 decision、open loop、session 或 current state，再运行正常 apply/sync。

## 5. 确定性摘要与显示预算

生成器不得调用模型。白话摘要使用以下固定规则：

- 删除多余空白并取第一个非空段落。
- 对显示文本应用 Unicode 安全的 rune 长度限制，不截断 UTF-8 字节。
- 超长内容使用统一省略号，并在索引中保留详情链接。
- 状态使用固定中文映射；未知状态在进入生成阶段前已由 ledger validation 拒绝。
- timeline 和最近变化按 `occurred_at`、ID 排序；决策优先级按最近 timeline 引用、标题、ID 排序；其他列表按标题、ID 排序。相同输入必须产生相同字节。
- 首页最近变化最多三条；里程碑最多三条；决策汇总受节点字符与项目总字符双重预算约束。

所有 Markdown 链接文本、URL 路径和 Mermaid 节点文本分别使用对应的安全转义函数，不复用一种转义规则代替另一种。

## 6. 错误处理与恢复

- accepted ledger 无法加载时，不生成或发布任何新派生内容。
- 任一目标路径重定向、越界、碰撞、非普通文件或父目录身份变化时失败关闭。
- 派生计划必须在写入前完成所有渲染、路径和预算验证。
- apply 继续使用现有全计划/事务恢复边界，不能留下与 accepted ledger 不一致的半套首页或索引。
- sync 的派生发布失败时保留已有可验证文件并返回安全诊断；诊断不得包含 ledger 内容或敏感绝对路径。
- 重复 apply 或 sync 对相同 accepted state 不改写字节、哈希或修改时间。

## 7. 测试与验收

### 7.1 单元与属性测试

- 首页章节顺序、五节点 A1 Mermaid 结构和链接清单的 golden tests。
- Markdown、Mermaid、Unicode、括号、引号、换行和链接特殊字符转义。
- 超长目标、数千决策、数千待办和数千 Session 下的输出预算与确定性。
- 中文状态映射、首段摘要和 UTF-8 rune 截断。
- 未知自定义 frontmatter/章节保留，生成器拥有区域可重复替换。
- 同一状态重复渲染的字节、哈希和修改时间幂等。

### 7.2 同步与恢复测试

- 派生文件不会进入普通实体 inventory，也不会产生 malformed。
- 首次同步、增量同步和中断恢复后，Project/Vault 派生文件哈希一致。
- 派生写入失败、错误哈希、路径重定向、符号链接、Windows reparse point 和并发替换均失败关闭。
- 语义实体在任一侧编辑后，apply/sync 更新相应首页、索引和快速理解区。
- 对首页或详情页派生区域的手工编辑不会提升 revision，也不会进入 Base；下一次完整 reconcile 恢复规范字节。
- Windows x64 与 macOS Intel/Apple Silicon 构建和相关路径测试继续通过。

### 7.3 真实用户验收

在真实 SessionReviewer 项目及已连接的 Obsidian Vault 中验证：

1. 打开 `project-overview.md` 即可看到 Mermaid 主线，无需进入 `diagrams/`。
2. 五层主线能回答目标、关键决策、已验证结果、当前状态和下一步。
3. 三个索引均可从首页到达，链接可打开对应详情。
4. 普通用户只看中文标题和摘要即可理解记录用途，不需要阅读内部 ID。
5. 项目端和 Obsidian 端内容一致，`sync status` 的 conflicted、malformed、blocked 和 pending 均为零。
6. GitHub Markdown 页面能渲染首页 Mermaid，并能使用标准相对链接导航。

## 8. 范围外事项

- 不将 Canvas 或 Excalidraw 设为默认首页。
- 不批量重命名现有稳定实体文件。
- 不让 CLI 调用模型生成摘要。
- 不把派生索引作为可双向编辑的语义来源。
- 不在本功能中实现后台 watcher、通知或新的 Git 自动化。

## 9. 完成标准

当首页、A1 Mermaid、三个目录索引和详情快速理解区均由 accepted ledger 确定性生成，并在真实 Project/Obsidian 两端通过导航、幂等、安全失败和跨平台测试后，本功能完成。用户应能从首页开始完成日常恢复，不再需要先理解 `diagrams/`、`decisions/`、`open-loops/`、`sessions/` 或内部实体 ID。
