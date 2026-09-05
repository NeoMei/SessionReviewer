---
id: review-project-p
entity_type: project-review
project_id: project-p
schema_version: 4
document_format: review-markdown-v1
revision: 1
generation_id: generation-1
minimum_reader_version: 0.4.1
minimum_writer_version: 0.4.1
custom_owner: 保留
---
# 项目回顾

这段自定义文本和 [链接](https://example.test/custom) 必须原样保留。

- <!-- session-reviewer:v4-field entity="project-overview" name="goal" -->
> <!-- /session-reviewer:v4-field entity="project-overview" name="goal" -->

<!-- session-reviewer:v4-field entity="project-overview" name="goal" -->
项目目标夹具
<!-- /session-reviewer:v4-field entity="project-overview" name="goal" -->
<!-- session-reviewer:v4-field entity="project-overview" name="stage" -->
  实施阶段
<!-- /session-reviewer:v4-field entity="project-overview" name="stage" -->
<!-- session-reviewer:v4-field entity="project-overview" name="status" -->
active
<!-- /session-reviewer:v4-field entity="project-overview" name="status" -->
<!-- session-reviewer:v4-field entity="project-overview" name="next_action" -->
运行聚焦测试。

```sh
go test ./internal/reviewv4
```
<!-- /session-reviewer:v4-field entity="project-overview" name="next_action" -->
<!-- session-reviewer:v4-field entity="project-overview" name="last_verification" -->

<!-- /session-reviewer:v4-field entity="project-overview" name="last_verification" -->

<!-- session-reviewer:v4-generated entity="project-overview" name="problem-tree" -->
- [正式问题](#problem-problemalpha)
<!-- /session-reviewer:v4-generated entity="project-overview" name="problem-tree" -->
<!-- session-reviewer:v4-generated entity="project-overview" name="pinned-decisions" -->
- [决策](#decision-decisionalpha)
<!-- /session-reviewer:v4-generated entity="project-overview" name="pinned-decisions" -->
<!-- session-reviewer:v4-generated entity="project-overview" name="recent-milestones" -->
共 1 条，显示 1 条；[查看完整历史](项目历史.md)。
- [里程碑](项目历史.md#milestone-milestonealpha)
<!-- /session-reviewer:v4-generated entity="project-overview" name="recent-milestones" -->

## 决策
<a id="decision-decisionalpha"></a>

<!-- session-reviewer:v4-field entity="decision:decision:alpha" name="title" -->
选择 [Markdown] 作为人工编辑面。
<!-- /session-reviewer:v4-field entity="decision:decision:alpha" name="title" -->
<!-- session-reviewer:v4-field entity="decision:decision:alpha" name="rationale" -->
正文应可直接阅读，
同时保持机器账本封闭。
<!-- /session-reviewer:v4-field entity="decision:decision:alpha" name="rationale" -->
<!-- session-reviewer:v4-field entity="decision:decision:alpha" name="impact" -->
编辑不再依赖 JSON。
<!-- /session-reviewer:v4-field entity="decision:decision:alpha" name="impact" -->
<!-- session-reviewer:v4-field entity="decision:decision:alpha" name="reevaluate_when" -->
当无损往返无法保证时。
<!-- /session-reviewer:v4-field entity="decision:decision:alpha" name="reevaluate_when" -->

## 风险
<a id="risk-riskalpha"></a>

<!-- session-reviewer:v4-field entity="risk:risk:alpha" name="title" -->
无损性回归
<!-- /session-reviewer:v4-field entity="risk:risk:alpha" name="title" -->
<!-- session-reviewer:v4-field entity="risk:risk:alpha" name="detail" -->
未修改区域若被 pretty-print 会产生噪声。
<!-- /session-reviewer:v4-field entity="risk:risk:alpha" name="detail" -->
<!-- session-reviewer:v4-field entity="risk:risk:alpha" name="status" -->
open
<!-- /session-reviewer:v4-field entity="risk:risk:alpha" name="status" -->

## 未决
<a id="open-loop-loopalpha"></a>

<!-- session-reviewer:v4-field entity="open-loop:loop:alpha" name="title" -->
CRLF 保留
<!-- /session-reviewer:v4-field entity="open-loop:loop:alpha" name="title" -->
<!-- session-reviewer:v4-field entity="open-loop:loop:alpha" name="question" -->
混合换行文档如何保持未改字节？
<!-- /session-reviewer:v4-field entity="open-loop:loop:alpha" name="question" -->
<!-- session-reviewer:v4-field entity="open-loop:loop:alpha" name="next_experiment" -->
构造 CRLF/LF 交错夹具。
<!-- /session-reviewer:v4-field entity="open-loop:loop:alpha" name="next_experiment" -->
<!-- session-reviewer:v4-field entity="open-loop:loop:alpha" name="completion_criterion" -->
未改区域哈希一致。
<!-- /session-reviewer:v4-field entity="open-loop:loop:alpha" name="completion_criterion" -->
<!-- session-reviewer:v4-field entity="open-loop:loop:alpha" name="status" -->
open
<!-- /session-reviewer:v4-field entity="open-loop:loop:alpha" name="status" -->

## 正式问题
<a id="problem-problemalpha"></a>

<!-- session-reviewer:v4-field entity="problem:problem:alpha" name="question" -->
如何确保同一字段只有一个权威位置？
<!-- /session-reviewer:v4-field entity="problem:problem:alpha" name="question" -->
<!-- session-reviewer:v4-field entity="problem:problem:alpha" name="completion_criterion" -->
24 个字段归属均唯一。
<!-- /session-reviewer:v4-field entity="problem:problem:alpha" name="completion_criterion" -->
<!-- session-reviewer:v4-field entity="problem:problem:alpha" name="current_conclusion" -->
使用封闭字段目录。
<!-- /session-reviewer:v4-field entity="problem:problem:alpha" name="current_conclusion" -->

```markdown
<!-- session-reviewer:v4-field entity="decision:example" name="title" -->
```
