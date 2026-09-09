package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/source"
)

type decodedLine struct {
	envelope transcriptLine
	message  claudeMessage
	line     int
	offset   int64
	hash     string
	time     time.Time
}

func (a *adapter) Decode(ctx context.Context, boundary source.Boundary, visit func(memory.ObservationRevision) error) (source.DecodeReport, error) {
	report := source.DecodeReport{TerminalState: boundary.TerminalState}
	a.mu.RLock()
	handle, leased := a.boundaryLease[boundary.Lease]
	frozen, found := a.frozen[boundary.Handle]
	a.mu.RUnlock()
	if !leased || handle != boundary.Handle || !found || boundary.Candidate.Provider != providerClaude || !sameBoundary(boundary, frozen.boundary) {
		return report, errors.New("boundary was not frozen by this Claude adapter")
	}
	defer a.AbandonBoundary(boundary)
	if visit == nil {
		return report, errors.New("observation visitor is required")
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if boundary.TerminalState != memory.Indexed {
		return report, nil
	}
	file, _, err := a.openStored(frozen.stored)
	if err != nil {
		return report, err
	}
	defer file.Close()
	if err := verifyFrozenPrefix(ctx, file, frozen); err != nil {
		return report, err
	}

	reader := bufio.NewReaderSize(io.NewSectionReader(file, 0, frozen.prefixSize), 64<<10)
	started, err := time.Parse(time.RFC3339Nano, boundary.Candidate.StartedAt)
	if err != nil {
		return report, errors.New("frozen Claude boundary has invalid start time")
	}
	ended := started
	models := make(map[string]accounting.TokenUsage)
	projectIDs := make(map[string]struct{})
	var observations []memory.ObservationRevision
	var firstConversation *decodedLine
	var offset int64
	recordCount := 0
	for lineNumber := 1; ; lineNumber++ {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		raw, readErr := readBoundedLine(reader, maxRecordBytes)
		if len(raw) == 0 && readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != io.EOF {
			return report, readErr
		}
		lineOffset := offset
		offset += int64(len(raw))
		recordCount++
		trimmed := bytes.TrimSpace(raw)
		var envelope transcriptLine
		if !utf8ValidJSON(trimmed) || json.Unmarshal(trimmed, &envelope) != nil {
			report.MalformedLines++
			report.Diagnostics = appendDiagnostic(report.Diagnostics, "malformed_jsonl")
			if readErr == io.EOF {
				break
			}
			continue
		}
		conversation := envelope.Type == "user" || envelope.Type == "assistant"
		if envelope.SessionID != "" && envelope.SessionID != boundary.Candidate.SessionID {
			report.UndecodableRecords++
			report.Diagnostics = appendDiagnostic(report.Diagnostics, "session_identity_mismatch")
			if readErr == io.EOF {
				break
			}
			continue
		}
		var instant time.Time
		if envelope.Timestamp != "" {
			instant, err = time.Parse(time.RFC3339Nano, envelope.Timestamp)
			if err != nil {
				report.UndecodableRecords++
				report.Diagnostics = appendDiagnostic(report.Diagnostics, "invalid_source_timestamp")
				if readErr == io.EOF {
					break
				}
				continue
			}
			if instant.After(ended) {
				ended = instant
			}
		}
		if !conversation {
			if !knownBookkeepingType(envelope.Type) {
				report.UnsupportedRecords++
				report.Diagnostics = appendDiagnostic(report.Diagnostics, "unsupported_source_record")
			}
			if readErr == io.EOF {
				break
			}
			continue
		}
		if envelope.SessionID == "" || envelope.CWD == "" || envelope.Timestamp == "" || authenticateCWD(envelope.CWD, frozen.stored.binding) != nil {
			report.UndecodableRecords++
			report.Diagnostics = appendDiagnostic(report.Diagnostics, "source_identity_mismatch")
			if readErr == io.EOF {
				break
			}
			continue
		}
		var message claudeMessage
		if json.Unmarshal(envelope.Message, &message) != nil || message.Role != envelope.Type {
			report.UndecodableRecords++
			report.Diagnostics = appendDiagnostic(report.Diagnostics, "malformed_message")
			if readErr == io.EOF {
				break
			}
			continue
		}
		sum := sha256.Sum256(trimmed)
		decoded := decodedLine{envelope: envelope, message: message, line: lineNumber, offset: lineOffset, hash: hex.EncodeToString(sum[:]), time: instant}
		if firstConversation == nil {
			copy := decoded
			firstConversation = &copy
		}
		projectIDs[frozen.stored.binding.ProjectID] = struct{}{}
		if message.Usage != nil {
			usage, usageErr := tokenUsage(*message.Usage)
			if usageErr != nil || strings.TrimSpace(message.Model) == "" {
				report.UndecodableRecords++
				report.Diagnostics = appendDiagnostic(report.Diagnostics, "invalid_accounting")
			} else {
				current := models[message.Model]
				if err := addTokenUsage(&current, usage); err != nil {
					return report, err
				}
				models[message.Model] = current
			}
		}
		if _, unsupported, contentErr := visibleMessage(decoded); contentErr != nil {
			report.UndecodableRecords++
			report.Diagnostics = appendDiagnostic(report.Diagnostics, "malformed_message_content")
		} else if unsupported {
			report.UnsupportedRecords++
			report.Diagnostics = appendDiagnostic(report.Diagnostics, "unsupported_source_record")
		}
		if readErr == io.EOF {
			break
		}
	}
	recordCountValue := uint64(recordCount)
	report.RecordCount = &recordCountValue
	if err := verifyFrozenPrefix(ctx, file, frozen); err != nil {
		return report, err
	}
	if firstConversation == nil || len(projectIDs) == 0 {
		return report, errors.New("Claude boundary has no authenticated conversation records")
	}
	observation := a.sessionStartedObservation(frozen, *firstConversation)
	if err := memory.ValidateObservationRevision(observation); err != nil {
		return report, err
	}
	observations = append(observations, observation)

	modelNames := make([]string, 0, len(models))
	for model := range models {
		modelNames = append(modelNames, model)
	}
	sort.Strings(modelNames)
	usage := accounting.SessionUsage{StartedAt: started.Format(time.RFC3339Nano), EndedAt: ended.Format(time.RFC3339Nano), DurationMS: ended.Sub(started).Milliseconds()}
	for _, model := range modelNames {
		item := accounting.ModelUsage{Model: model, TokenUsage: models[model]}
		usage.Models = append(usage.Models, item)
		usage.TotalTokens += item.TotalTokens
	}
	if err := accounting.ValidateSessionUsage(&usage); err != nil {
		return report, fmt.Errorf("validate decoded Claude accounting: %w", err)
	}
	ids := make([]string, 0, len(projectIDs))
	for projectID := range projectIDs {
		ids = append(ids, projectID)
	}
	sort.Strings(ids)
	record := memory.SourceRecord{
		SchemaVersion: memory.MemorySchemaVersion, Provider: providerClaude, SessionID: boundary.Candidate.SessionID,
		SourceIdentity: boundary.SourceIdentity, StartedAt: usage.StartedAt, EndedAt: usage.EndedAt,
		FrozenBoundary: boundary.Frozen, Availability: memory.SourceAvailable, Usage: usage, ProjectIDs: ids,
	}
	if err := memory.ValidateSourceRecord(record); err != nil {
		return report, fmt.Errorf("validate proposed Claude source: %w", err)
	}
	relation, expected, err := classifyBoundaryRelation(ctx, file, frozen, record)
	if err != nil {
		return report, err
	}
	report.BoundaryRelation, report.ExpectedCatalogDigest, report.ProposedSource = relation, expected, record
	switch {
	case report.MalformedLines > 0 || report.UndecodableRecords > 0:
		report.TerminalState = memory.Unreadable
	default:
		report.TerminalState = memory.Indexed
	}
	for _, item := range observations {
		if err := visit(item); err != nil {
			return report, err
		}
		report.EmittedRevisions++
		stable, err := memory.Digest(item.Key)
		if err != nil {
			return report, err
		}
		for _, predecessor := range a.supersedes {
			report.Supersessions = append(report.Supersessions, source.RevisionSupersession{Key: item.Key, StableKeyDigest: stable, SuccessorRevisionID: item.RevisionID, SupersededAdapter: predecessor, SuccessorAdapter: a.version})
		}
	}
	return report, nil
}

func (a *adapter) sessionStartedObservation(frozen frozenSource, line decodedLine) memory.ObservationRevision {
	object := redact.AbsolutePaths(a.redactor.Text(line.envelope.CWD).Text)
	value := memory.ObservationRevision{
		SchemaVersion: memory.MemorySchemaVersion,
		Key:           memory.ObservationKey{Provider: providerClaude, SessionID: frozen.boundary.Candidate.SessionID, SourceIdentity: frozen.boundary.SourceIdentity, Sequence: line.line, ProjectID: frozen.stored.binding.ProjectID, Kind: "artifact", Subject: frozen.boundary.Candidate.SessionID},
		Ref:           memory.SourceRef{Provider: providerClaude, SessionID: frozen.boundary.Candidate.SessionID, SourceIdentity: frozen.boundary.SourceIdentity, Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: line.line, ByteOffset: line.offset}}, SourceHash: line.hash},
		Timestamp:     line.time.Format(time.RFC3339Nano), Operation: "session_started", Object: boundedText(object, 512), AdapterID: adapterID, AdapterVersion: a.version,
	}
	value.RevisionID = memory.ObservationRevisionID(value)
	return value
}

func tokenUsage(value claudeUsage) (accounting.TokenUsage, error) {
	input, err := addSafe(value.InputTokens, value.CacheReadInputTokens)
	if err != nil {
		return accounting.TokenUsage{}, err
	}
	input, err = addSafe(input, value.CacheCreationInputTokens)
	if err != nil {
		return accounting.TokenUsage{}, err
	}
	total, err := addSafe(input, value.OutputTokens)
	if err != nil {
		return accounting.TokenUsage{}, err
	}
	usage := accounting.TokenUsage{InputTokens: input, CachedInputTokens: value.CacheReadInputTokens, CacheWriteInputTokens: value.CacheCreationInputTokens, OutputTokens: value.OutputTokens, TotalTokens: total}
	return usage, accounting.ValidateTokenUsage(usage)
}

func addTokenUsage(target *accounting.TokenUsage, value accounting.TokenUsage) error {
	fields := []*int64{&target.InputTokens, &target.CachedInputTokens, &target.CacheWriteInputTokens, &target.OutputTokens, &target.ReasoningOutputTokens, &target.TotalTokens}
	adds := []int64{value.InputTokens, value.CachedInputTokens, value.CacheWriteInputTokens, value.OutputTokens, value.ReasoningOutputTokens, value.TotalTokens}
	for index := range fields {
		next, err := addSafe(*fields[index], adds[index])
		if err != nil {
			return err
		}
		*fields[index] = next
	}
	return accounting.ValidateTokenUsage(*target)
}

func knownBookkeepingType(value string) bool {
	switch value {
	case "attachment", "system", "progress", "agent-name", "custom-title", "last-prompt", "permission-mode", "pr-link", "queue-operation", "file-history-snapshot":
		return true
	default:
		return false
	}
}

func appendDiagnostic(values []memory.Diagnostic, code string) []memory.Diagnostic {
	for _, value := range values {
		if value.Code == code {
			return values
		}
	}
	if len(values) >= 4096 {
		return values
	}
	return append(values, memory.Diagnostic{Code: code})
}

func utf8ValidJSON(raw []byte) bool { return len(raw) > 0 && utf8.Valid(raw) && json.Valid(raw) }

func classifyBoundaryRelation(ctx context.Context, file *os.File, frozen frozenSource, incoming memory.SourceRecord) (source.BoundaryRelation, string, error) {
	if !frozen.priorFound {
		return source.BoundaryInitial, "", nil
	}
	existing := frozen.prior
	if existing.SourceIdentity != incoming.SourceIdentity || existing.StartedAt != incoming.StartedAt {
		return source.BoundaryUnchanged, frozen.priorDigest, nil
	}
	oldLocation, newLocation := existing.FrozenBoundary.Location.JSONL, incoming.FrozenBoundary.Location.JSONL
	if oldLocation == nil || newLocation == nil {
		return "", "", errors.New("Claude catalog boundary is not JSONL")
	}
	if oldLocation.Line == newLocation.Line && oldLocation.ByteOffset == newLocation.ByteOffset {
		if existing.FrozenBoundary.SourceHash == incoming.FrozenBoundary.SourceHash {
			return source.BoundaryUnchanged, frozen.priorDigest, nil
		}
		return source.BoundaryReplacement, frozen.priorDigest, nil
	}
	if newLocation.Line < oldLocation.Line || newLocation.ByteOffset < oldLocation.ByteOffset {
		return source.BoundaryReplacement, frozen.priorDigest, nil
	}
	prefixHash, _, err := hashPrefix(ctx, file, oldLocation.ByteOffset)
	if err != nil {
		return "", "", err
	}
	if prefixHash == existing.FrozenBoundary.SourceHash {
		return source.BoundaryAppend, frozen.priorDigest, nil
	}
	return source.BoundaryReplacement, frozen.priorDigest, nil
}

func verifyFrozenPrefix(ctx context.Context, file *os.File, frozen frozenSource) error {
	info, err := file.Stat()
	if err != nil || info.Size() < frozen.prefixSize {
		return errors.Join(errors.New("frozen Claude source shrank"), err)
	}
	hash, lines, err := hashPrefix(ctx, file, frozen.prefixSize)
	if err != nil {
		return err
	}
	if hash != frozen.boundary.Frozen.SourceHash || lines != frozen.lineCount {
		return errors.New("frozen Claude source changed")
	}
	return nil
}

func (a *adapter) Read(ctx context.Context, ref memory.SourceRef, limit int64) ([]byte, error) {
	if err := source.ValidateReadLimit(limit); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.Provider != providerClaude || ref.SessionID == "" || ref.SourceIdentity == "" || !shaPattern.MatchString(ref.SourceHash) || ref.Location.Kind != memory.SourceLocationJSONL || ref.Location.JSONL == nil || ref.Location.JSONL.Line < 1 || ref.Location.JSONL.ByteOffset < 0 {
		return nil, errors.New("invalid Claude source reference")
	}
	frozen, err := a.frozenFor(ref.Provider, ref.SessionID, ref.SourceIdentity, func(value frozenSource) bool {
		location := value.boundary.Frozen.Location.JSONL
		return location != nil && ref.Location.JSONL.Line <= location.Line && ref.Location.JSONL.ByteOffset < location.ByteOffset
	})
	if err != nil {
		return nil, err
	}
	file, _, err := a.openStored(frozen.stored)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := verifyFrozenPrefix(ctx, file, frozen); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(io.NewSectionReader(file, 0, frozen.prefixSize), 64<<10)
	var offset int64
	for lineNumber := 1; ; lineNumber++ {
		raw, readErr := readBoundedLine(reader, maxRecordBytes)
		if len(raw) == 0 && readErr == io.EOF {
			return nil, errors.New("source reference is outside frozen Claude boundary")
		}
		start := offset
		offset += int64(len(raw))
		if lineNumber == ref.Location.JSONL.Line {
			if start != ref.Location.JSONL.ByteOffset {
				return nil, errors.New("Claude source byte offset mismatch")
			}
			trimmed := bytes.TrimSpace(raw)
			sum := sha256.Sum256(trimmed)
			if hex.EncodeToString(sum[:]) != ref.SourceHash {
				return nil, errors.New("Claude source hash mismatch")
			}
			if int64(len(trimmed)) > limit {
				trimmed = trimmed[:limit]
			}
			return bytes.Clone(trimmed), nil
		}
		if readErr != nil && readErr != io.EOF {
			return nil, readErr
		}
		if readErr == io.EOF {
			return nil, errors.New("source reference is outside frozen Claude boundary")
		}
	}
}

func (a *adapter) frozenFor(provider, sessionID, identity string, accept func(frozenSource) bool) (frozenSource, error) {
	key := frozenKey(provider, sessionID, identity)
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, handle := range a.frozenBySource[key] {
		value := a.frozen[handle]
		if accept(value) {
			return value, nil
		}
	}
	return frozenSource{}, errors.New("source reference is outside every frozen Claude boundary")
}

func visibleMessage(line decodedLine) (conversationchain.SourceMessage, bool, error) {
	message := line.message
	if message.Role != "user" && message.Role != "assistant" {
		return conversationchain.SourceMessage{}, false, errors.New("unsupported Claude role")
	}
	var scalar string
	if json.Unmarshal(message.Content, &scalar) == nil {
		if strings.TrimSpace(scalar) == "" {
			return conversationchain.SourceMessage{}, false, nil
		}
		return conversationchain.SourceMessage{Role: conversationchain.Role(message.Role), Phase: visiblePhase(message.Role), Text: scalar, OccurredAt: line.time.Format(time.RFC3339Nano), RecordOrdinal: uint64(line.line), RecordHash: line.hash}, false, nil
	}
	var parts []claudePart
	if json.Unmarshal(message.Content, &parts) != nil {
		return conversationchain.SourceMessage{}, false, errors.New("malformed Claude content")
	}
	text := make([]string, 0, len(parts))
	unsupported := false
	for _, part := range parts {
		switch part.Type {
		case "text":
			if part.Text != "" {
				text = append(text, part.Text)
			}
		case "thinking":
			if message.Role != "assistant" || part.Thinking == "" {
				return conversationchain.SourceMessage{}, false, errors.New("invalid thinking part")
			}
		case "redacted_thinking":
			if message.Role != "assistant" || part.Data == "" {
				return conversationchain.SourceMessage{}, false, errors.New("invalid redacted thinking part")
			}
		case "tool_use":
			if message.Role != "assistant" || part.ID == "" || part.Name == "" {
				return conversationchain.SourceMessage{}, false, errors.New("invalid tool_use part")
			}
		case "tool_result":
			if message.Role != "user" || part.ToolUseID == "" {
				return conversationchain.SourceMessage{}, false, errors.New("invalid tool_result part")
			}
		case "attachment":
			if message.Role != "user" || part.FileName == "" {
				return conversationchain.SourceMessage{}, false, errors.New("invalid attachment part")
			}
		default:
			unsupported = true
		}
	}
	value := strings.Join(text, "\n")
	if strings.TrimSpace(value) == "" {
		return conversationchain.SourceMessage{}, unsupported, nil
	}
	return conversationchain.SourceMessage{Role: conversationchain.Role(message.Role), Phase: visiblePhase(message.Role), Text: value, OccurredAt: line.time.Format(time.RFC3339Nano), RecordOrdinal: uint64(line.line), RecordHash: line.hash}, unsupported, nil
}

func visiblePhase(role string) string {
	if role == "assistant" {
		return "final_answer"
	}
	return ""
}
