package presentation

import (
	"strings"

	"github.com/neomei/SessionReviewer/internal/memory"
)

func milestoneTitle(category string) string {
	switch category {
	case "verification":
		return "已记录验证通过"
	case "commit":
		return "已记录提交"
	case "release":
		return "已记录发布"
	case "deployment":
		return "已记录部署"
	case "version":
		return "已记录版本变化"
	default:
		return "已记录执行证据"
	}
}

func readableMilestoneEvidence(kind, state string, revision memory.ObservationRevision) string {
	label := readableMilestoneKind(kind)
	if !milestoneEvidenceRevisionMatches(kind, revision) {
		return boundedMilestoneText(label+"：证据详情不可用", milestoneSegmentBytes)
	}
	if state != "" {
		label += "（" + readableMilestoneState(state) + "）"
	}
	fields := readableMilestoneFields(kind, revision.Fields)
	if len(fields) != 0 {
		label += "：" + strings.Join(fields, "；")
	}
	return boundedMilestoneText(label, milestoneSegmentBytes)
}

func readableMilestoneKind(kind string) string {
	switch kind {
	case "command_started":
		return "命令执行已开始"
	case "command_finished":
		return "命令执行已结束"
	case "file_change":
		return "文件变更记录"
	case "verification":
		return "验证记录"
	case "commit", "commit_created":
		return "提交记录"
	case "release", "release_created", "release_published":
		return "发布记录"
	case "deployment":
		return "部署记录"
	case "version":
		return "版本记录"
	case "branch", "git_observation":
		return "Git 状态记录"
	default:
		return "执行证据"
	}
}

func readableMilestoneState(state string) string {
	switch state {
	case "passed":
		return "通过"
	case "failed":
		return "失败"
	case "conflict":
		return "证据冲突"
	case "unknown":
		return "结果未确认"
	default:
		return "结果未确认"
	}
}

func milestoneEvidenceRevisionMatches(kind string, revision memory.ObservationRevision) bool {
	if revision.RevisionID == "" {
		return false
	}
	switch kind {
	case "command_started", "command_finished":
		return revision.Key.Kind == "command" && revision.Operation == kind
	case "file_change":
		return revision.Key.Kind == "file" && revision.Operation == kind
	case "verification":
		return revision.Key.Kind == "verification" && revision.Operation == kind
	case "commit", "commit_created":
		return revision.Key.Kind == "commit" && revision.Operation == kind
	case "release", "release_created", "release_published":
		return revision.Key.Kind == "release" && revision.Operation == kind
	case "deployment", "version":
		return revision.Key.Kind == kind && revision.Operation == kind
	case "branch":
		return revision.Key.Kind == "branch" && revision.Operation == kind
	case "git_observation":
		return (revision.Key.Kind == "branch" || revision.Key.Kind == "git_status") && revision.Operation == kind
	default:
		return false
	}
}

func readableMilestoneFields(kind string, fields map[string]string) []string {
	var allowed []string
	switch kind {
	case "command_started":
		allowed = []string{"command_signature"}
	case "command_finished":
		allowed = []string{"command_signature", "exit_code"}
	case "file_change":
		allowed = []string{"path"}
	case "verification":
		allowed = []string{"component", "exit_code"}
	case "commit", "commit_created":
		allowed = []string{"git_head"}
	case "release", "release_created", "release_published":
		allowed = []string{"tag", "version", "target", "release_id"}
	case "deployment":
		allowed = []string{"component", "version", "target", "release_id"}
	case "version":
		allowed = []string{"component", "version"}
	case "branch":
		allowed = []string{"git_head", "branch"}
	case "git_observation":
		allowed = []string{"git_head", "tag", "branch"}
	}
	result := make([]string, 0, len(allowed))
	for _, key := range allowed {
		if value, present := fields[key]; present {
			result = append(result, readableMilestoneFieldLabel(key)+"："+boundedMilestoneText(value, milestoneSegmentBytes))
		}
	}
	return result
}

func readableMilestoneFieldLabel(field string) string {
	switch field {
	case "command_signature":
		return "命令类型"
	case "component":
		return "组件"
	case "exit_code":
		return "退出码"
	case "path":
		return "项目内文件"
	case "git_head":
		return "提交标识"
	case "tag":
		return "标签"
	case "version":
		return "版本"
	case "target":
		return "目标"
	case "release_id":
		return "发布标识"
	case "branch":
		return "分支"
	default:
		return "证据"
	}
}
