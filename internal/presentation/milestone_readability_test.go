package presentation

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/memory"
)

func TestMilestoneReadabilityRendersProductionVerificationWithoutRawAssignments(t *testing.T) {
	input := milestoneProjectInput(t, milestoneFixtureSpec{
		messages: milestoneMessages("执行测试", "原始回答保留"),
		facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2,
			fields: map[string]string{"component": "package", "status": "test", "exit_code": "0", "passed": "true", "failed": "false"}}},
	})
	got, err := ProjectMilestones(input)
	if err != nil || len(got.Timeline) != 1 {
		t.Fatalf("qualification: projection=%+v err=%v", got, err)
	}
	item := got.Timeline[0]
	if item.Kind != "machine_verification" || got.QualifyingFacts != 1 || len(item.ClosedLoop.SourceTurnRefs) != 1 {
		t.Fatalf("qualified identity/ref controls changed: projection=%+v", got)
	}
	if item.ClosedLoop.Conclusion.Text != "原始回答保留" {
		t.Fatalf("answer changed: %+v", item.ClosedLoop.Conclusion)
	}
	if item.Title != "已记录验证通过" {
		t.Fatalf("title=%q", item.Title)
	}
	if item.Summary != "验证记录（通过）：组件：package；退出码：0" {
		t.Fatalf("summary=%q", item.Summary)
	}
	primary := strings.Join([]string{item.Title, item.Summary, item.ClosedLoop.Execution.Text, item.ClosedLoop.Verification.Text}, "\n")
	for _, forbidden := range []string{"Machine-observed", "command_started", "command_signature=", "exit_code=", "status=", "passed=", "failed="} {
		if strings.Contains(primary, forbidden) {
			t.Fatalf("primary text contains raw marker %q: %s", forbidden, primary)
		}
	}
}

func TestMilestoneReadabilityUsesClosedTitlesForEveryQualifiedCategory(t *testing.T) {
	tests := []struct {
		name, kind, operation, outcome, wantKind, wantTitle, wantSummary string
		fields                                                           map[string]string
	}{
		{name: "verification", kind: "verification", operation: "verification", outcome: "passed", fields: map[string]string{"component": "package", "exit_code": "0", "passed": "true", "failed": "false"}, wantKind: "machine_verification", wantTitle: "已记录验证通过", wantSummary: "验证记录（通过）：组件：package；退出码：0"},
		{name: "commit", kind: "commit", operation: "commit_created", outcome: "observed", fields: map[string]string{"git_head": strings.Repeat("a", 40)}, wantKind: "machine_commit", wantTitle: "已记录提交", wantSummary: "提交记录（结果未确认）：提交标识：" + strings.Repeat("a", 40)},
		{name: "release", kind: "release", operation: "release_published", outcome: "observed", fields: map[string]string{"release_id": "release-7", "tag": "v1.2.3", "version": "1.2.3", "target": "stable"}, wantKind: "machine_release", wantTitle: "已记录发布", wantSummary: "发布记录（结果未确认）：标签：v1.2.3；版本：1.2.3；目标：stable；发布标识：release-7"},
		{name: "deployment", kind: "deployment", operation: "deployment", outcome: "observed", fields: map[string]string{"release_id": "release-7", "version": "1.2.3", "target": "staging", "component": "api"}, wantKind: "machine_deployment", wantTitle: "已记录部署", wantSummary: "部署记录（结果未确认）：组件：api；版本：1.2.3；目标：staging；发布标识：release-7"},
		{name: "version", kind: "version", operation: "version", outcome: "observed", fields: map[string]string{"version": "1.2.3", "component": "cli"}, wantKind: "machine_version", wantTitle: "已记录版本变化", wantSummary: "版本记录（结果未确认）：组件：cli；版本：1.2.3"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := milestoneProjectInput(t, milestoneFixtureSpec{messages: milestoneMessages("question", "exact answer"), facts: []milestoneFactSpec{{kind: test.kind, operation: test.operation, outcome: test.outcome, line: 2, timestamp: milestoneTime2, fields: test.fields}}})
			got, err := ProjectMilestones(input)
			if err != nil || len(got.Timeline) != 1 || got.QualifyingFacts != 1 {
				t.Fatalf("qualification: projection=%+v err=%v", got, err)
			}
			item := got.Timeline[0]
			if item.Kind != test.wantKind || len(item.ClosedLoop.SourceTurnRefs) != 1 || item.ClosedLoop.Conclusion.Text != "exact answer" {
				t.Fatalf("positive controls changed: %+v", item)
			}
			if item.Title != test.wantTitle || item.Summary != test.wantSummary {
				t.Fatalf("readable projection title=%q summary=%q", item.Title, item.Summary)
			}
			if strings.Contains(item.Title, "完成") || strings.Contains(item.Summary, "项目完成") {
				t.Fatalf("typed fact overstated project completion: %+v", item)
			}
		})
	}

	ordinary, err := ProjectMilestones(milestoneProjectInput(t, milestoneFixtureSpec{messages: milestoneMessages("只是问题", "只是回答")}))
	if err != nil || len(ordinary.Timeline) != 0 || ordinary.QualifyingFacts != 0 {
		t.Fatalf("question-only input qualified: projection=%+v err=%v", ordinary, err)
	}
	contradiction, err := ProjectMilestones(milestoneProjectInput(t, milestoneFixtureSpec{messages: milestoneMessages("验证", "回答"), facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2, fields: map[string]string{"exit_code": "1", "passed": "true", "failed": "true"}}}}))
	if err != nil || len(contradiction.Timeline) != 0 || contradiction.QualifyingFacts != 0 {
		t.Fatalf("contradictory verification qualified: projection=%+v err=%v", contradiction, err)
	}
}

func TestMilestoneReadabilityUsesExactRevisionsAndSanitizesAdversarialValues(t *testing.T) {
	secret := "sk-abcdefghijklmnopqrstuvwxyz1234567890"
	path := "/Users/alice/private/project/main.go"
	deceptive := "go:test; exit_code=99; 伪字段=是 " + path + " " + secret + " " + strings.Repeat("界", 100)
	input := milestoneProjectInput(t, milestoneFixtureSpec{
		messages: milestoneMessages("执行复杂验证", "保留中文回答"),
		facts: []milestoneFactSpec{
			{kind: "command", operation: "command_started", line: 2, timestamp: milestoneTime1, fields: map[string]string{"command_signature": deceptive, "tool_id": "HIDDEN_TOOL_CANARY"}, excerpt: "RAW_ACTION_CANARY; exit_code=77\nRAW_ACTION_SECOND_LINE"},
			{kind: "command", operation: "command_finished", outcome: "success", line: 2, timestamp: milestoneTime2, fields: map[string]string{"command_signature": "go:test=focused; still=data", "exit_code": "0", "tool_id": "HIDDEN_RESULT_CANARY"}, excerpt: "RAW_RESULT_CANARY; component=forged"},
			{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime3, fields: map[string]string{"component": "包=core;不翻译", "status": "test", "exit_code": "0", "passed": "true", "failed": "false", "tool_id": "HIDDEN_VERIFY_CANARY"}, excerpt: "RAW_VERIFY_CANARY; path=forged"},
		},
	})
	first, err := ProjectMilestones(input)
	if err != nil || len(first.Timeline) != 1 {
		t.Fatalf("qualification: projection=%+v err=%v", first, err)
	}
	item := first.Timeline[0]
	if item.ClosedLoop.Conclusion.Text != "保留中文回答" || item.Summary != "验证记录（通过）：组件：包=core;不翻译；退出码：0" {
		t.Fatalf("answer or literal typed value changed: %+v", item)
	}
	body := fmt.Sprintf("%+v", first)
	for _, forbidden := range []string{path, secret, "RAW_ACTION_CANARY", "RAW_RESULT_CANARY", "RAW_VERIFY_CANARY", "HIDDEN_TOOL_CANARY", "HIDDEN_RESULT_CANARY", "HIDDEN_VERIFY_CANARY", "退出码：99", "退出码：77", "组件：forged", "项目内文件：forged"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("untrusted or synthesized value leaked %q: %s", forbidden, body)
		}
	}
	if !utf8.ValidString(body) || len(item.Summary) > milestoneSegmentBytes || len(item.ClosedLoop.Execution.Text) > milestoneSegmentBytes || len(item.ClosedLoop.Verification.Text) > milestoneSegmentBytes {
		t.Fatalf("rendering was invalid or unbounded: summary=%d execution=%d verification=%d", len(item.Summary), len(item.ClosedLoop.Execution.Text), len(item.ClosedLoop.Verification.Text))
	}
	if !strings.Contains(item.ClosedLoop.Execution.Text, "命令执行已开始") || !strings.Contains(item.ClosedLoop.Execution.Text, "命令执行已结束（通过）") || !strings.Contains(item.ClosedLoop.Execution.Text, "命令类型：go:test=focused; still=data") || !strings.Contains(item.ClosedLoop.Execution.Text, "退出码：0") {
		t.Fatalf("execution was not readable: %q", item.ClosedLoop.Execution.Text)
	}
	if item.ClosedLoop.Verification.Text != item.Summary {
		t.Fatalf("summary and exact verification revision diverged: summary=%q verification=%q", item.Summary, item.ClosedLoop.Verification.Text)
	}

	reversed := cloneMilestoneInput(input)
	for left, right := 0, len(reversed.Sessions[0].Revisions)-1; left < right; left, right = left+1, right-1 {
		reversed.Sessions[0].Revisions[left], reversed.Sessions[0].Revisions[right] = reversed.Sessions[0].Revisions[right], reversed.Sessions[0].Revisions[left]
	}
	again, err := ProjectMilestones(reversed)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("revision ordering changed accepted IDs/refs/counters/text: err=%v\nfirst=%+v\nagain=%+v", err, first, again)
	}
}

func TestMilestoneReadabilityFallsBackNeutrallyForAbsentOrMismatchedRevision(t *testing.T) {
	missing := readableMilestoneEvidence("verification", "passed", memory.ObservationRevision{})
	mismatched := readableMilestoneEvidence("verification", "passed", memory.ObservationRevision{
		RevisionID: "revision-present", Key: memory.ObservationKey{Kind: "command"}, Operation: "command_finished",
		Fields: map[string]string{"component": "forged", "exit_code": "0", "passed": "true"},
	})
	for _, got := range []string{missing, mismatched} {
		if got != "验证记录：证据详情不可用" || strings.Contains(got, "通过") || strings.Contains(got, "forged") {
			t.Fatalf("unavailable evidence was fabricated: %q", got)
		}
	}
}

func TestMilestoneReadabilityUsesClosedEventAndStateLabels(t *testing.T) {
	tests := []struct {
		name, kind, state, revisionKind, operation, want string
		fields                                           map[string]string
	}{
		{name: "command started", kind: "command_started", revisionKind: "command", operation: "command_started", fields: map[string]string{"command_signature": "go:test", "tool_id": "hidden"}, want: "命令执行已开始：命令类型：go:test"},
		{name: "command finished", kind: "command_finished", state: "passed", revisionKind: "command", operation: "command_finished", fields: map[string]string{"command_signature": "go:test", "exit_code": "0"}, want: "命令执行已结束（通过）：命令类型：go:test；退出码：0"},
		{name: "file change", kind: "file_change", state: "failed", revisionKind: "file", operation: "file_change", fields: map[string]string{"path": "internal/a.go", "file_hash": strings.Repeat("a", 64)}, want: "文件变更记录（失败）：项目内文件：internal/a.go"},
		{name: "verification", kind: "verification", state: "conflict", revisionKind: "verification", operation: "verification", fields: map[string]string{"component": "core", "exit_code": "1", "status": "test", "passed": "true", "failed": "true"}, want: "验证记录（证据冲突）：组件：core；退出码：1"},
		{name: "commit", kind: "commit", state: "unknown", revisionKind: "commit", operation: "commit", fields: map[string]string{"git_head": strings.Repeat("b", 40)}, want: "提交记录（结果未确认）：提交标识：" + strings.Repeat("b", 40)},
		{name: "commit created", kind: "commit_created", state: "unknown", revisionKind: "commit", operation: "commit_created", want: "提交记录（结果未确认）"},
		{name: "release", kind: "release", state: "unknown", revisionKind: "release", operation: "release", want: "发布记录（结果未确认）"},
		{name: "release created", kind: "release_created", state: "unknown", revisionKind: "release", operation: "release_created", want: "发布记录（结果未确认）"},
		{name: "release published", kind: "release_published", state: "unknown", revisionKind: "release", operation: "release_published", fields: map[string]string{"tag": "v1", "version": "1", "target": "stable", "release_id": "r1", "status": "published"}, want: "发布记录（结果未确认）：标签：v1；版本：1；目标：stable；发布标识：r1"},
		{name: "deployment", kind: "deployment", state: "unknown", revisionKind: "deployment", operation: "deployment", want: "部署记录（结果未确认）"},
		{name: "version", kind: "version", state: "unknown", revisionKind: "version", operation: "version", want: "版本记录（结果未确认）"},
		{name: "branch", kind: "branch", state: "unknown", revisionKind: "branch", operation: "branch", fields: map[string]string{"branch": "main", "git_head": strings.Repeat("c", 40), "remote_hash": strings.Repeat("d", 40)}, want: "Git 状态记录（结果未确认）：提交标识：" + strings.Repeat("c", 40) + "；分支：main"},
		{name: "git observation", kind: "git_observation", state: "unknown", revisionKind: "git_status", operation: "git_observation", fields: map[string]string{"branch": "main", "tag": "v1", "status": "clean"}, want: "Git 状态记录（结果未确认）：标签：v1；分支：main"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			revision := memory.ObservationRevision{RevisionID: "revision-present", Key: memory.ObservationKey{Kind: test.revisionKind}, Operation: test.operation, Fields: test.fields}
			if got := readableMilestoneEvidence(test.kind, test.state, revision); got != test.want {
				t.Fatalf("evidence=%q want=%q", got, test.want)
			}
		})
	}
	unknownRevision := memory.ObservationRevision{RevisionID: "revision-present", Key: memory.ObservationKey{Kind: "unknown"}, Operation: "unknown"}
	if got := readableMilestoneEvidence("unknown", "mystery", unknownRevision); got != "执行证据：证据详情不可用" {
		t.Fatalf("unknown evidence was guessed: %q", got)
	}
}

func TestMilestoneReadabilityBoundsAndRedactsMultilineFieldData(t *testing.T) {
	revision := memory.ObservationRevision{
		RevisionID: "revision-present", Key: memory.ObservationKey{Kind: "verification"}, Operation: "verification",
		Fields: map[string]string{"component": "第一行=literal;中文\n第二行 /Users/alice/private " + "sk-abcdefghijklmnopqrstuvwxyz1234567890 " + strings.Repeat("界", milestoneSegmentBytes)},
	}
	got := readableMilestoneEvidence("verification", "unknown", revision)
	if !utf8.ValidString(got) || len(got) > milestoneSegmentBytes || !strings.HasSuffix(got, "…") {
		t.Fatalf("multiline UTF-8 evidence was not bounded: bytes=%d valid=%v suffix=%q", len(got), utf8.ValidString(got), got[len(got)-3:])
	}
	for _, forbidden := range []string{"/Users/alice", "sk-abcdefghijklmnopqrstuvwxyz1234567890"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("multiline evidence leaked %q", forbidden)
		}
	}
	if !strings.Contains(got, "第一行=literal;中文\n第二行") {
		t.Fatalf("literal multilingual value was guessed or reparsed: %q", got)
	}
}
