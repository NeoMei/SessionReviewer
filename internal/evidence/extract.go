package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/session"
)

var (
	ErrPacketFull    = errors.New("evidence packet is full")
	ErrInvalidLimits = errors.New("invalid evidence limits")
)

const truncationMarker = "…[TRUNCATED]"

type Limits struct {
	MaxEvents       int
	MaxSummaryRunes int
	MaxPacketRunes  int
}

func DefaultLimits() Limits {
	return Limits{
		MaxEvents:       500,
		MaxSummaryRunes: 1200,
		MaxPacketRunes:  300000,
	}
}

func (l Limits) Validate() error {
	if l.MaxEvents <= 0 || l.MaxSummaryRunes <= 0 || l.MaxPacketRunes <= 0 {
		return ErrInvalidLimits
	}
	return nil
}

type Extractor struct {
	packet        Packet
	redactor      redact.Redactor
	limits        Limits
	currentCWD    string
	warningCounts map[string]int
	usedIDs       map[string]struct{}
	initErr       error
	full          bool
}

func New(sessionID, cwd string, from int, redactor redact.Redactor, limits Limits) *Extractor {
	x := &Extractor{
		redactor:      redactor,
		limits:        limits,
		currentCWD:    cwd,
		warningCounts: make(map[string]int),
		usedIDs:       make(map[string]struct{}),
		initErr:       limits.Validate(),
	}

	safeSessionID, sessionFindings := x.redact(sessionID)
	safeCWD, cwdFindings := x.redact(cwd)
	x.mergeFindings(sessionFindings)
	x.mergeFindings(cwdFindings)
	x.packet = Packet{
		SchemaVersion: 1,
		SessionID:     safeSessionID,
		CWD:           safeCWD,
		FromCursor:    from,
		ToCursor:      from - 1,
		Events:        []Item{},
	}
	x.refreshWarnings()
	return x
}

func (x *Extractor) Add(record session.Record) error {
	if x == nil {
		return ErrInvalidLimits
	}
	if x.initErr != nil {
		return x.initErr
	}
	if x.full {
		return ErrPacketFull
	}

	switch record.Type {
	case "turn_context":
		var payload struct {
			CWD string `json:"cwd"`
		}
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return errors.New("malformed turn_context payload")
		}
		if payload.CWD == "" || payload.CWD == x.currentCWD {
			x.advance(record.Line)
			return nil
		}
		if err := x.append(record, "cwd_change", "", "", payload.CWD, ""); err != nil {
			return err
		}
		x.currentCWD = payload.CWD
		x.advance(record.Line)
		return nil
	case "response_item":
		return x.addResponseItem(record)
	default:
		x.advance(record.Line)
		return nil
	}
}

func (x *Extractor) addResponseItem(record session.Record) error {
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(record.Payload, &header); err != nil {
		return errors.New("malformed response_item payload")
	}

	var err error
	switch header.Type {
	case "message":
		var payload struct {
			ID      string `json:"id"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return errors.New("malformed message payload")
		}
		if payload.Role != "user" && payload.Role != "assistant" {
			x.advance(record.Line)
			return nil
		}
		parts := make([]string, 0, len(payload.Content))
		for _, content := range payload.Content {
			if content.Type == "input_text" || content.Type == "output_text" {
				parts = append(parts, content.Text)
			}
		}
		if len(parts) == 0 {
			x.advance(record.Line)
			return nil
		}
		err = x.append(record, "message", payload.ID, payload.Role, strings.Join(parts, "\n"), "")
	case "custom_tool_call":
		var payload struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Input string `json:"input"`
		}
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return errors.New("malformed custom_tool_call payload")
		}
		err = x.append(record, "tool_call", payload.ID, "", payload.Input, payload.Name)
	case "custom_tool_call_output":
		var payload struct {
			ID     string `json:"id"`
			Output string `json:"output"`
		}
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return errors.New("malformed custom_tool_call_output payload")
		}
		err = x.append(record, "tool_result", payload.ID, "", payload.Output, "")
	default:
		x.advance(record.Line)
		return nil
	}
	if err != nil {
		return err
	}
	x.advance(record.Line)
	return nil
}

func (x *Extractor) append(record session.Record, kind, itemID, role, summary, toolName string) error {
	if len(x.packet.Events) >= x.limits.MaxEvents {
		return x.markFull()
	}

	safeItemID, itemFindings := x.redact(itemID)
	safeTimestamp, timestampFindings := x.redact(record.Timestamp)
	safeSourceHash, hashFindings := x.redact(record.SourceHash)
	safeToolName, toolFindings := x.redact(toolName)
	safeSummary, summaryFindings := x.redact(summary)
	safeSummary = bound(safeSummary, x.limits.MaxSummaryRunes)

	findings := make([]redact.Finding, 0, len(itemFindings)+len(timestampFindings)+len(hashFindings)+len(toolFindings)+len(summaryFindings))
	findings = append(findings, itemFindings...)
	findings = append(findings, timestampFindings...)
	findings = append(findings, hashFindings...)
	findings = append(findings, toolFindings...)
	findings = append(findings, summaryFindings...)

	item := Item{
		ID:         x.eventID(record.Line, safeSourceHash, kind, safeItemID),
		ItemID:     safeItemID,
		Timestamp:  safeTimestamp,
		JSONLLine:  record.Line,
		SourceHash: safeSourceHash,
		Kind:       kind,
		Role:       role,
		ToolName:   safeToolName,
		Summary:    safeSummary,
	}
	candidate := x.Packet()
	candidate.Events = append(candidate.Events, item)
	candidate.Warnings = warningsWith(x.warningCounts, findings)
	if packetTextRunes(candidate) > x.limits.MaxPacketRunes {
		return x.markFull()
	}

	x.packet.Events = append(x.packet.Events, item)
	x.usedIDs[item.ID] = struct{}{}
	x.mergeFindings(findings)
	x.refreshWarnings()
	return nil
}

func (x *Extractor) Packet() Packet {
	if x == nil {
		return Packet{}
	}
	snapshot := x.packet
	snapshot.Events = append([]Item(nil), x.packet.Events...)
	if snapshot.Events == nil {
		snapshot.Events = []Item{}
	}
	snapshot.Warnings = append([]string(nil), x.packet.Warnings...)
	return snapshot
}

func (x *Extractor) eventID(line int, sourceHash, kind, itemID string) string {
	seed := fmt.Sprintf("v1\x00%s\x00%d\x00%s\x00%s\x00%s", x.packet.SessionID, line, sourceHash, kind, itemID)
	for collision := 0; ; collision++ {
		value := seed
		if collision > 0 {
			value = fmt.Sprintf("%s\x00%d", seed, collision)
		}
		hash := sha256.Sum256([]byte(value))
		id := "ev-" + hex.EncodeToString(hash[:6])
		if _, exists := x.usedIDs[id]; !exists {
			return id
		}
	}
}

func (x *Extractor) redact(value string) (string, []redact.Finding) {
	result := x.redactor.Text(value)
	return result.Text, result.Findings
}

func (x *Extractor) mergeFindings(findings []redact.Finding) {
	for _, finding := range findings {
		if finding.Rule != "" && finding.Count > 0 {
			x.warningCounts[finding.Rule] += finding.Count
		}
	}
}

func (x *Extractor) refreshWarnings() {
	x.packet.Warnings = formatWarnings(x.warningCounts)
}

func warningsWith(existing map[string]int, findings []redact.Finding) []string {
	counts := make(map[string]int, len(existing)+len(findings))
	for rule, count := range existing {
		counts[rule] = count
	}
	for _, finding := range findings {
		if finding.Rule != "" && finding.Count > 0 {
			counts[finding.Rule] += finding.Count
		}
	}
	return formatWarnings(counts)
}

func formatWarnings(counts map[string]int) []string {
	rules := make([]string, 0, len(counts))
	for rule, count := range counts {
		if rule != "" && count > 0 {
			rules = append(rules, rule)
		}
	}
	sort.Strings(rules)
	warnings := make([]string, 0, len(rules))
	for _, rule := range rules {
		warnings = append(warnings, fmt.Sprintf("redacted:%s:%d", rule, counts[rule]))
	}
	return warnings
}

func (x *Extractor) markFull() error {
	x.full = true
	x.packet.HasMore = true
	return ErrPacketFull
}

func (x *Extractor) advance(line int) {
	if line > x.packet.ToCursor {
		x.packet.ToCursor = line
	}
}

func bound(value string, max int) string {
	valueRunes := []rune(value)
	if len(valueRunes) <= max {
		return value
	}
	markerRunes := []rune(truncationMarker)
	if max <= len(markerRunes) {
		return string(markerRunes[:max])
	}
	prefixRunes := max - len(markerRunes)
	return string(valueRunes[:prefixRunes]) + truncationMarker
}

func packetTextRunes(packet Packet) int {
	encoded, err := json.Marshal(packet)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return utf8.RuneCount(encoded)
}
