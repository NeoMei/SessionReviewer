package opencode

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/redact"
)

func bounded(value string, n int) string {
	if len(value) <= n {
		return value
	}
	value = value[:n]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
func visibleRecordBytes(r canonicalRecord, limit int64) []byte {
	// The record hash authenticates the original atom, but hidden reasoning is
	// never part of the explicit analysis/read result.
	parts := []partInfo{}
	for _, p := range r.Parts {
		if p.Type == "text" && !p.Synthetic && !p.Ignored || p.Type == "tool" {
			parts = append(parts, p)
		}
	}
	body, _ := json.Marshal(struct {
		Role  string     `json:"role"`
		Parts []partInfo `json:"parts"`
	}{r.Info.Role, parts})
	return []byte(bounded(redact.Default().Text(string(body)).Text, int(limit)))
}

var simpleVerification = regexp.MustCompile(`^(go test(?: [A-Za-z0-9_./=-]+)*|npm (?:test|run (?:test|check|lint))|pnpm (?:test|check|lint))$`)

func (s *snapshotSession) observations(r redact.Redactor) ([]memory.ObservationRevision, []memory.Diagnostic) {
	diagnostics := []memory.Diagnostic{}
	output := []memory.ObservationRevision{}
	add := func(i int, subject, kind, operation, outcome string, fields map[string]string) {
		rec := s.records[i]
		v := memory.ObservationRevision{SchemaVersion: memory.MemorySchemaVersion, Key: memory.ObservationKey{Provider: "opencode", SessionID: s.row.ID, SourceIdentity: s.identity, Sequence: i + 1, ProjectID: s.binding.ProjectID, Kind: kind, Subject: subject}, Ref: memory.SourceRef{Provider: "opencode", SessionID: s.row.ID, SourceIdentity: s.identity, Location: memory.SourceLocation{Kind: memory.SourceLocationCanonical, Canonical: &memory.CanonicalSourceLocation{Record: i + 1}}, SourceHash: rec.Hash}, Timestamp: timestamp(rec.Created), Operation: operation, Outcome: outcome, Fields: fields, AdapterID: "opencode-sqlite", AdapterVersion: adapterVersion}
		v.RevisionID = memory.ObservationRevisionID(v)
		output = append(output, v)
	}
	add(0, s.row.ID, "artifact", "session_started", "", nil)
	for i, record := range s.records {
		for j, p := range record.Parts {
			if p.Type != "tool" {
				continue
			}
			// A native part ID prevents reused tool call IDs from colliding.
			subject := record.PartIDs[j]
			toolID := bounded(p.CallID, 256)
			var input struct {
				Command string `json:"command"`
			}
			validCommand := p.Tool == "bash" && json.Unmarshal(p.State.Input, &input) == nil && input.Command != ""
			if !validCommand {
				toolName := bounded(redact.AbsolutePaths(r.Text(p.Tool).Text), 256)
				add(i, subject+"-call", "tool", "tool_call", "", map[string]string{"tool_id": toolID, "tool_name": toolName})
				add(i, subject+"-result", "tool", "tool_result", p.State.Status, map[string]string{"tool_id": toolID, "tool_name": toolName, "status": p.State.Status})
				if p.Tool == "bash" && len(diagnostics) == 0 {
					diagnostics = append(diagnostics, memory.Diagnostic{Code: "tool_input_unsupported"})
				}
				continue
			}
			signature := bounded(redact.AbsolutePaths(r.Text(input.Command).Text), 512)
			add(i, subject+"-start", "command", "command_started", "", map[string]string{"tool_id": toolID, "command_signature": signature})
			fields := map[string]string{"tool_id": toolID, "command_signature": signature}
			outcome := "unknown"
			var exit int
			raw, hasExit := p.State.Metadata["exit"]
			if hasExit && json.Unmarshal(raw, &exit) == nil && string(raw) != "null" {
				fields["exit_code"] = strconv.Itoa(exit)
				outcome = "failure"
				if exit == 0 {
					outcome = "success"
				}
			}
			add(i, subject+"-finish", "command", "command_finished", outcome, fields)
			if outcome != "unknown" && simpleVerification.MatchString(strings.TrimSpace(input.Command)) {
				passed := outcome == "success"
				add(i, subject+"-verification", "verification", "verification", outcome, map[string]string{"tool_id": toolID, "component": "project", "exit_code": strconv.Itoa(exit), "passed": strconv.FormatBool(passed), "failed": strconv.FormatBool(!passed)})
			}
		}
	}
	return output, diagnostics
}
