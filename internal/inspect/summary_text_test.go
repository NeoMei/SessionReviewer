package inspect

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/memory"
)

func TestSummaryFactTextRendersTypedCommandOutcomes(t *testing.T) {
	tests := []struct {
		name string
		fact memory.ObservationRevision
		want string
	}{
		{
			name: "started without result",
			fact: memory.ObservationRevision{Key: memory.ObservationKey{Kind: "command"}, Operation: "command_started"},
			want: "命令已启动 · 结果未记录",
		},
		{
			name: "successful finish",
			fact: memory.ObservationRevision{Key: memory.ObservationKey{Kind: "command"}, Operation: "command_finished", Outcome: "success", Fields: map[string]string{"exit_code": "0"}},
			want: "命令已完成 · 成功 · 退出码 0",
		},
		{
			name: "failed excerpt cannot override typed result",
			fact: memory.ObservationRevision{Key: memory.ObservationKey{Kind: "command"}, Operation: "command_finished", Outcome: "failure", Fields: map[string]string{"exit_code": "1"}, Excerpt: "all tests passed"},
			want: "命令已完成 · 失败 · 退出码 1 · all tests passed",
		},
		{
			name: "contradictory result",
			fact: memory.ObservationRevision{Key: memory.ObservationKey{Kind: "command"}, Operation: "command_finished", Outcome: "success", Fields: map[string]string{"exit_code": "1"}},
			want: "命令已完成 · 结果冲突 · 退出码 1",
		},
		{
			name: "failure contradicts zero exit",
			fact: memory.ObservationRevision{Key: memory.ObservationKey{Kind: "command"}, Operation: "command_finished", Outcome: "failure", Fields: map[string]string{"exit_code": "0"}},
			want: "命令已完成 · 结果冲突 · 退出码 0",
		},
		{
			name: "unknown outcome does not inherit zero exit success",
			fact: memory.ObservationRevision{Key: memory.ObservationKey{Kind: "command"}, Operation: "command_finished", Outcome: "unknown", Fields: map[string]string{"exit_code": "0"}},
			want: "命令已完成 · 结果未知 · 退出码 0",
		},
		{
			name: "malformed exit code omitted",
			fact: memory.ObservationRevision{Key: memory.ObservationKey{Kind: "command"}, Operation: "command_finished", Outcome: "failure", Fields: map[string]string{"exit_code": "1 /Users/private"}},
			want: "命令已完成 · 失败",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := summaryFactText(test.fact); got != test.want {
				t.Fatalf("text=%q want=%q", got, test.want)
			}
		})
	}
}

func TestSummaryFactTextRendersVerificationOutcomesWithoutInventingSuccess(t *testing.T) {
	tests := []struct {
		name, outcome, want string
	}{
		{name: "passed", outcome: "passed", want: "验证结果 · 通过"},
		{name: "failed", outcome: "failed", want: "验证结果 · 失败"},
		{name: "unknown", outcome: "maybe", want: "验证结果 · 结果未知"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := summaryFactText(memory.ObservationRevision{
				Key: memory.ObservationKey{Kind: "verification"}, Operation: "verification", Outcome: test.outcome,
				Fields: map[string]string{"status": "arbitrary status", "component": "private component"},
			})
			if got != test.want {
				t.Fatalf("text=%q want=%q", got, test.want)
			}
		})
	}

	got := summaryFactText(memory.ObservationRevision{
		Key: memory.ObservationKey{Kind: "unsupported"}, Operation: "totally_custom", Outcome: "green",
		Fields: map[string]string{"status": "SUCCESS", "component": "verified"},
	})
	if got != "观察记录 · 结果未知" || strings.Contains(got, "成功") || strings.Contains(got, "通过") || strings.Contains(got, "SUCCESS") || strings.Contains(got, "verified") {
		t.Fatalf("unsupported fact was promoted or leaked fields: %q", got)
	}
	got = summaryFactText(memory.ObservationRevision{
		Key: memory.ObservationKey{Kind: "command"}, Operation: "custom_command", Outcome: "success",
	})
	if got != "命令记录 · 结果未知" || strings.Contains(got, "成功") {
		t.Fatalf("unknown operation was promoted through known kind: %q", got)
	}
}

func TestSummaryFactTextUsesNeutralLabelsForTypedOperations(t *testing.T) {
	tests := []struct {
		kind, operation, wantPrefix string
	}{
		{kind: "file", operation: "file_change", wantPrefix: "文件变更"},
		{kind: "artifact", operation: "artifact_created", wantPrefix: "产物记录"},
		{kind: "commit", operation: "commit_created", wantPrefix: "提交记录"},
		{kind: "release", operation: "release_published", wantPrefix: "发布记录"},
		{kind: "error", operation: "error_observed", wantPrefix: "错误记录"},
		{kind: "test", operation: "test", wantPrefix: "测试结果"},
		{kind: "build", operation: "build", wantPrefix: "构建结果"},
		{kind: "lint", operation: "lint", wantPrefix: "静态检查结果"},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			got := summaryFactText(memory.ObservationRevision{Key: memory.ObservationKey{Kind: test.kind}, Operation: test.operation})
			if !strings.HasPrefix(got, test.wantPrefix+" · ") || strings.HasSuffix(got, " · ") {
				t.Fatalf("text=%q prefix=%q", got, test.wantPrefix)
			}
		})
	}
}

func TestSummaryFactTextRedactsAndCapsOnlyTheOptionalExcerpt(t *testing.T) {
	fact := memory.ObservationRevision{
		Key:       memory.ObservationKey{Kind: "command"},
		Operation: "command_finished",
		Outcome:   "failure",
		Fields: map[string]string{
			"path":      "/Users/alice/hidden/project",
			"component": "sk-abcdefghijklmnopqrstuvwxyz1234567890",
			"status":    strings.Repeat("A", 180),
		},
		Excerpt: "see /Users/bob/private token sk-abcdefghijklmnopqrstuvwxyz1234567890 " + strings.Repeat("界", 300),
	}
	got := summaryFactText(fact)
	if !strings.HasPrefix(got, "命令已完成 · 失败 · ") || !utf8.ValidString(got) || len(got) > 512 {
		t.Fatalf("unsafe prefix or truncation: bytes=%d valid=%v text=%q", len(got), utf8.ValidString(got), got)
	}
	for _, secret := range []string{"/Users/alice", "/Users/bob", "sk-abcdefghijklmnopqrstuvwxyz1234567890", strings.Repeat("A", 80)} {
		if strings.Contains(got, secret) {
			t.Fatalf("text leaked %q: %q", secret, got)
		}
	}
}

func TestSessionSummaryTypedTextFlowsToErrorsAndUnresolvedEntries(t *testing.T) {
	failure := summaryTestRevision(t, 1, "command", "failure", "", map[string]string{"status": "command_finished", "exit_code": "1"})
	got, err := reduceSessionSummary(context.Background(), summaryTestInput([]memory.ObservationRevision{failure}))
	if err != nil {
		t.Fatal(err)
	}
	want := "命令已完成 · 失败 · 退出码 1"
	if len(got.Errors.Items) != 1 || got.Errors.Items[0].Text != want || len(got.UnresolvedQuestions.Items) != 1 || got.UnresolvedQuestions.Items[0].Text != want {
		t.Fatalf("typed failure did not flow to every consumer: errors=%+v unresolved=%+v", got.Errors, got.UnresolvedQuestions)
	}
}
