package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/session"
)

var (
	ErrPacketFull    = errors.New("evidence packet is full")
	ErrInvalidLimits = errors.New("invalid evidence limits")
)

var lowercaseSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

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
	packet             Packet
	redactor           redact.Redactor
	limits             Limits
	currentCWD         string
	warningCounts      map[string]int
	usageWarningCounts map[string]int
	usedIDs            map[string]struct{}
	full               bool
	expectedSet        bool
}

// SessionUsageState is an opaque snapshot used to restore usage metadata and
// its redaction audit trail together after a candidate record is rejected.
type SessionUsageState struct {
	usage         *accounting.SessionUsage
	warningCounts map[string]int
}

// NewWithProjectID constructs an extractor whose packet-size accounting includes
// the final project identifier from the beginning of extraction.
func NewWithProjectID(projectID, sessionID, cwd string, from int, redactor redact.Redactor, limits Limits) (*Extractor, error) {
	if projectID == "" || sessionID == "" || cwd == "" || from < 1 {
		return nil, fmt.Errorf("project id, session id, cwd, and positive from cursor are required")
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	x := &Extractor{
		redactor:           redactor,
		limits:             limits,
		currentCWD:         cwd,
		warningCounts:      make(map[string]int),
		usageWarningCounts: make(map[string]int),
		usedIDs:            make(map[string]struct{}),
	}

	safeProjectID, projectFindings := x.redact(projectID)
	safeSessionID, sessionFindings := x.redact(sessionID)
	safeCWD, cwdFindings := x.redact(cwd)
	x.mergeFindings(projectFindings)
	x.mergeFindings(sessionFindings)
	x.mergeFindings(cwdFindings)
	x.packet = Packet{
		SchemaVersion: 2,
		ProjectID:     safeProjectID,
		SessionID:     safeSessionID,
		CWD:           safeCWD,
		FromCursor:    from,
		ToCursor:      from - 1,
		Events:        []Item{},
	}
	x.refreshWarnings()
	if packetTextRunes(x.packet) > limits.MaxPacketRunes {
		return nil, fmt.Errorf("%w: max packet runes smaller than schema envelope", ErrInvalidLimits)
	}
	return x, nil
}

// SetExpectedCursor binds the packet to the accepted cursor snapshot that
// immediately precedes FromCursor.
func (x *Extractor) SetExpectedCursor(boundary CursorBoundary) error {
	if x == nil {
		return ErrInvalidLimits
	}
	if boundary.Line < 0 || boundary.Line != x.packet.FromCursor-1 {
		return fmt.Errorf("invalid expected cursor line")
	}
	if x.packet.ToCursor != boundary.Line {
		return fmt.Errorf("expected cursor cannot replace a consumed boundary")
	}
	if boundary.Line == 0 {
		if boundary.SourceHash != "" {
			return fmt.Errorf("invalid expected cursor hash at line zero")
		}
	} else if !lowercaseSHA256.MatchString(boundary.SourceHash) {
		return fmt.Errorf("invalid expected cursor source hash")
	}
	candidate := x.Packet()
	candidate.ExpectedCursor = boundary
	candidate.NextCursor = boundary
	if packetTextRunes(candidate) > x.limits.MaxPacketRunes {
		return fmt.Errorf("%w: expected cursor exceeds packet limit", ErrInvalidLimits)
	}
	x.packet.ExpectedCursor = boundary
	x.packet.NextCursor = boundary
	x.expectedSet = true
	return nil
}

func (x *Extractor) Add(record session.Record) error {
	if x == nil {
		return ErrInvalidLimits
	}
	if x.full {
		return ErrPacketFull
	}
	if !x.expectedSet {
		return fmt.Errorf("expected cursor is required before adding records")
	}
	if record.Line <= 0 || record.Line <= x.packet.NextCursor.Line {
		return fmt.Errorf("record line is not strictly beyond the current boundary")
	}
	if !lowercaseSHA256.MatchString(record.SourceHash) {
		return fmt.Errorf("record source hash is not a lowercase SHA-256 digest")
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
			return x.advance(record)
		}
		if err := x.append(record, "cwd_change", "", "", payload.CWD, ""); err != nil {
			return err
		}
		x.currentCWD = payload.CWD
		return nil
	case "response_item":
		return x.addResponseItem(record)
	default:
		return x.advance(record)
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
		var messageHeader struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(record.Payload, &messageHeader); err != nil {
			return errors.New("malformed message payload")
		}
		if messageHeader.Role != "user" && messageHeader.Role != "assistant" {
			return x.advance(record)
		}
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
		parts := make([]string, 0, len(payload.Content))
		for _, content := range payload.Content {
			if content.Type == "input_text" || content.Type == "output_text" {
				parts = append(parts, content.Text)
			}
		}
		if len(parts) == 0 {
			return x.advance(record)
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
		return x.advance(record)
	}
	if err != nil {
		return err
	}
	return nil
}

func (x *Extractor) append(record session.Record, kind, itemID, role, summary, toolName string) error {
	if len(x.packet.Events) >= x.limits.MaxEvents {
		return x.markFull()
	}

	safeItemID, itemFindings := x.redact(itemID)
	safeTimestamp, timestampFindings := x.redact(record.Timestamp)
	safeSourceHash := record.SourceHash
	safeToolName, toolFindings := x.redact(toolName)
	safeSummary, summaryFindings := x.redact(summary)
	safeSummary = bound(safeSummary, x.limits.MaxSummaryRunes)

	findings := make([]redact.Finding, 0, len(itemFindings)+len(timestampFindings)+len(toolFindings)+len(summaryFindings))
	findings = append(findings, itemFindings...)
	findings = append(findings, timestampFindings...)
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
	candidate.Warnings = warningsWith(x.combinedWarningCounts(), findings)
	advancePacket(&candidate, record)
	if packetTextRunes(candidate) > x.limits.MaxPacketRunes {
		return x.markFull()
	}

	x.packet.Events = append(x.packet.Events, item)
	x.packet.ToCursor = candidate.ToCursor
	x.packet.NextCursor = candidate.NextCursor
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
	if x.packet.SessionUsage != nil {
		usage := *x.packet.SessionUsage
		usage.Models = append([]accounting.ModelUsage{}, usage.Models...)
		snapshot.SessionUsage = &usage
	}
	return snapshot
}

func (x *Extractor) SnapshotSessionUsage() SessionUsageState {
	if x == nil {
		return SessionUsageState{}
	}
	return SessionUsageState{
		usage:         cloneSessionUsage(x.packet.SessionUsage),
		warningCounts: cloneCounts(x.usageWarningCounts),
	}
}

func (x *Extractor) RestoreSessionUsage(state SessionUsageState) error {
	if x == nil {
		return ErrInvalidLimits
	}
	candidate := x.Packet()
	candidate.SessionUsage = cloneSessionUsage(state.usage)
	candidate.Warnings = formatWarnings(mergeWarningCounts(x.warningCounts, state.warningCounts))
	if packetTextRunes(candidate) > x.limits.MaxPacketRunes {
		return ErrPacketFull
	}
	x.packet.SessionUsage = cloneSessionUsage(state.usage)
	x.usageWarningCounts = cloneCounts(state.warningCounts)
	x.refreshWarnings()
	return nil
}

func (x *Extractor) SetSessionUsage(usage *accounting.SessionUsage) error {
	if x == nil {
		return ErrInvalidLimits
	}
	if usage == nil {
		x.packet.SessionUsage = nil
		x.usageWarningCounts = make(map[string]int)
		x.refreshWarnings()
		return nil
	}
	if len(usage.Models) > 128 {
		return errors.New("session usage has too many models")
	}
	copyUsage := *cloneSessionUsage(usage)
	usageWarnings := make(map[string]int)
	for index := range copyUsage.Models {
		safe, findings := x.redact(copyUsage.Models[index].Model)
		copyUsage.Models[index].Model = safe
		for _, finding := range findings {
			if finding.Rule != "" && finding.Count > 0 {
				usageWarnings[finding.Rule] += finding.Count
			}
		}
	}
	sort.Slice(copyUsage.Models, func(i, j int) bool { return copyUsage.Models[i].Model < copyUsage.Models[j].Model })
	for index := 1; index < len(copyUsage.Models); index++ {
		if copyUsage.Models[index-1].Model == copyUsage.Models[index].Model {
			return errors.New("redacted session model identifiers collide")
		}
	}
	candidate := x.Packet()
	candidate.SessionUsage = &copyUsage
	candidate.Warnings = formatWarnings(mergeWarningCounts(x.warningCounts, usageWarnings))
	if packetTextRunes(candidate) > x.limits.MaxPacketRunes {
		return ErrPacketFull
	}
	x.packet.SessionUsage = &copyUsage
	x.usageWarningCounts = usageWarnings
	x.refreshWarnings()
	return nil
}

func cloneSessionUsage(usage *accounting.SessionUsage) *accounting.SessionUsage {
	if usage == nil {
		return nil
	}
	copyUsage := *usage
	copyUsage.Models = append([]accounting.ModelUsage{}, usage.Models...)
	return &copyUsage
}

func cloneCounts(counts map[string]int) map[string]int {
	result := make(map[string]int, len(counts))
	for rule, count := range counts {
		result[rule] = count
	}
	return result
}

// AddWarning appends a structural warning without bypassing packet bounds.
func (x *Extractor) AddWarning(warning string) error {
	if x == nil || warning == "" {
		return ErrInvalidLimits
	}
	candidate := x.Packet()
	candidate.Warnings = append(candidate.Warnings, warning)
	if packetTextRunes(candidate) > x.limits.MaxPacketRunes {
		return x.markFull()
	}
	x.packet.Warnings = candidate.Warnings
	return nil
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
	x.packet.Warnings = formatWarnings(x.combinedWarningCounts())
}

func (x *Extractor) combinedWarningCounts() map[string]int {
	return mergeWarningCounts(x.warningCounts, x.usageWarningCounts)
}

func mergeWarningCounts(first, second map[string]int) map[string]int {
	result := make(map[string]int, len(first)+len(second))
	for rule, count := range first {
		result[rule] += count
	}
	for rule, count := range second {
		result[rule] += count
	}
	return result
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
	candidate := x.Packet()
	candidate.HasMore = true
	if packetTextRunes(candidate) > x.limits.MaxPacketRunes {
		return fmt.Errorf("%w: packet state exceeds max packet runes", ErrInvalidLimits)
	}
	x.full = true
	x.packet.HasMore = candidate.HasMore
	return ErrPacketFull
}

func (x *Extractor) advance(record session.Record) error {
	candidate := x.Packet()
	advancePacket(&candidate, record)
	if packetTextRunes(candidate) > x.limits.MaxPacketRunes {
		return x.markFull()
	}
	x.packet.ToCursor = candidate.ToCursor
	x.packet.NextCursor = candidate.NextCursor
	return nil
}

func advancePacket(packet *Packet, record session.Record) {
	if record.Line > packet.ToCursor {
		packet.ToCursor = record.Line
		packet.NextCursor = CursorBoundary{Line: record.Line, SourceHash: record.SourceHash}
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
