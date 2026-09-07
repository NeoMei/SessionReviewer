package inspect

import (
	"strconv"
	"strings"

	"github.com/neomei/SessionReviewer/internal/memory"
)

type summaryOutcome int

const (
	summaryOutcomeUnrecorded summaryOutcome = iota
	summaryOutcomeUnknown
	summaryOutcomeSuccess
	summaryOutcomeFailure
	summaryOutcomeConflict
)

type summaryTextType struct {
	label        string
	known        bool
	verification bool
	started      bool
}

func isSummaryOperation(fact memory.ObservationRevision) bool {
	switch normalizedSummaryToken(fact.Operation) {
	case "session_started", "cwd_changed":
		return false
	}
	switch normalizedSummaryToken(fact.Key.Kind) {
	case "command", "file", "artifact", "commit", "release":
		return true
	default:
		return false
	}
}

func summaryFactText(fact memory.ObservationRevision) string {
	typed := summaryTextMapping(fact)
	outcome := summaryTypedOutcome(fact, typed)
	parts := []string{typed.label, summaryOutcomeText(outcome, typed.verification)}
	if exitCode, ok := summaryExitCode(fact.Fields["exit_code"]); ok && typed.known && !typed.started {
		parts = append(parts, "退出码 "+strconv.FormatInt(exitCode, 10))
	}
	prefix := strings.Join(parts, " · ")
	if strings.TrimSpace(fact.Excerpt) != "" {
		prefix += " · " + fact.Excerpt
	}
	return safeEventExcerpt(prefix)
}

func summaryTextMapping(fact memory.ObservationRevision) summaryTextType {
	kind := normalizedSummaryToken(fact.Key.Kind)
	operation := normalizedSummaryToken(fact.Operation)
	switch kind {
	case "command":
		switch operation {
		case "command_started":
			return summaryTextType{label: "命令已启动", known: true, started: true}
		case "command_finished":
			return summaryTextType{label: "命令已完成", known: true}
		default:
			return summaryTextType{label: "命令记录"}
		}
	case "file":
		return summaryTextType{label: "文件变更", known: operation == "file_change"}
	case "verification":
		return summaryTextType{label: "验证结果", known: summaryVerificationOperation(operation), verification: true}
	case "test":
		return summaryTextType{label: "测试结果", known: operation == "test", verification: true}
	case "build":
		return summaryTextType{label: "构建结果", known: operation == "build", verification: true}
	case "lint":
		return summaryTextType{label: "静态检查结果", known: operation == "lint", verification: true}
	case "artifact":
		return summaryTextType{label: "产物记录", known: operation == "artifact_created"}
	case "commit":
		return summaryTextType{label: "提交记录", known: operation == "commit" || operation == "commit_created"}
	case "release":
		return summaryTextType{label: "发布记录", known: operation == "release" || operation == "release_created" || operation == "release_published"}
	case "error":
		return summaryTextType{label: "错误记录", known: operation == "error" || operation == "error_observed"}
	default:
		return summaryTextType{label: "观察记录"}
	}
}

func summaryVerificationOperation(operation string) bool {
	switch operation {
	case "verification", "test", "build", "lint":
		return true
	default:
		return false
	}
}

func summaryTypedOutcome(fact memory.ObservationRevision, typed summaryTextType) summaryOutcome {
	if typed.started {
		return summaryOutcomeUnrecorded
	}
	if !typed.known {
		return summaryOutcomeUnknown
	}
	var outcome summaryOutcome
	switch normalizedSummaryToken(fact.Outcome) {
	case "success", "passed":
		outcome = summaryOutcomeSuccess
	case "failure", "failed", "error":
		outcome = summaryOutcomeFailure
	case "":
		outcome = summaryOutcomeUnrecorded
	default:
		outcome = summaryOutcomeUnknown
	}
	exitCode, hasExitCode := summaryExitCode(fact.Fields["exit_code"])
	if hasExitCode && (outcome == summaryOutcomeSuccess && exitCode != 0 || outcome == summaryOutcomeFailure && exitCode == 0) {
		return summaryOutcomeConflict
	}
	return outcome
}

func summaryOutcomeText(outcome summaryOutcome, verification bool) string {
	switch outcome {
	case summaryOutcomeUnrecorded:
		return "结果未记录"
	case summaryOutcomeSuccess:
		if verification {
			return "通过"
		}
		return "成功"
	case summaryOutcomeFailure:
		return "失败"
	case summaryOutcomeConflict:
		return "结果冲突"
	default:
		return "结果未知"
	}
}

func summaryExitCode(value string) (int64, bool) {
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || strconv.FormatInt(parsed, 10) != value {
		return 0, false
	}
	return parsed, true
}

func normalizedSummaryToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
