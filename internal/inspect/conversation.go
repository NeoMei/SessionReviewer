package inspect

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/source/codex"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
	"unicode/utf8"
)

type ConversationRequest struct {
	DataRoot, ProjectID, Provider, SessionID, ExpectedGenerationID string
	SessionViewDigest, TurnUnitID, Cursor, MessageCursor           string
	Limit                                                          int
}

const MaxConversationResponseBytes = 1 << 20
const conversationPageItemsBytes = 768 << 10
const conversationCursorReserveBytes = 32 << 10
const conversationRedactionVersion = "visible-redaction-v1"

// ConversationPage is a separate paged contract; it is not a truncated
// conversation-chain-v1 Document or a session-event-page-v1 event page.
type ConversationPage struct {
	SchemaVersion             int                                `json:"schema_version"`
	MinimumReaderVersion      string                             `json:"minimum_reader_version"`
	Mode                      string                             `json:"mode"`
	ProjectID                 string                             `json:"project_id"`
	Provider                  string                             `json:"provider"`
	SessionID                 string                             `json:"session_id"`
	GenerationID              string                             `json:"generation_id"`
	SessionViewDigest         string                             `json:"session_view_digest"`
	DependencyDigest          string                             `json:"dependency_digest"`
	RedactionVersion          string                             `json:"redaction_version"`
	TurnUnitID                *string                            `json:"turn_unit_id"`
	Total                     uint64                             `json:"total"`
	RangeStart                uint64                             `json:"range_start"`
	RangeEnd                  uint64                             `json:"range_end"`
	FirstCursor               *string                            `json:"first_cursor"`
	PreviousCursor            *string                            `json:"previous_cursor"`
	NextCursor                *string                            `json:"next_cursor"`
	LastCursor                *string                            `json:"last_cursor"`
	TurnUnits                 []conversationchain.VisibleTurn    `json:"turn_units"`
	Messages                  []conversationchain.VisibleMessage `json:"messages"`
	Coverage                  conversationchain.VisibleCoverage  `json:"coverage"`
	EvidenceSessionViewDigest *string                            `json:"evidence_session_view_digest,omitempty"`
	BodyAvailability          string                             `json:"body_availability,omitempty"`
	Actions                   []conversationchain.Action         `json:"actions,omitempty"`
	Results                   []conversationchain.Result         `json:"results,omitempty"`
	ActionTotal               uint64                             `json:"action_total,omitempty"`
	ResultTotal               uint64                             `json:"result_total,omitempty"`
	EvidenceTruncated         bool                               `json:"evidence_truncated,omitempty"`
}

func LoadConversationPage(ctx context.Context, request ConversationRequest) (ConversationPage, error) {
	if request.Limit < 1 || request.Limit > 64 || request.Cursor != "" && request.TurnUnitID != "" || request.MessageCursor != "" && request.TurnUnitID == "" || len(request.Cursor) > 8192 || len(request.MessageCursor) > 8192 || len(request.TurnUnitID) > 256 || request.SessionViewDigest != "" && !digestRE.MatchString(request.SessionViewDigest) {
		return ConversationPage{}, publicError(CodeInvalidArgument, "conversation request is invalid")
	}
	var page ConversationPage
	_, err := inspectPublishedSession(ctx, EventPageRequest{DataRoot: request.DataRoot, ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, ExpectedGenerationID: request.ExpectedGenerationID, Limit: request.Limit}, nil, func(authenticated authenticatedSession) error {
		if request.SessionViewDigest != "" {
			selected, err := selectRetainedConversationByView(ctx, authenticated, request.SessionViewDigest)
			if err != nil {
				return err
			}
			turns, coverage, err := retainedVisibleConversation(selected)
			if err != nil {
				return publicError(CodeInvalidArgument, "published retained conversation is unavailable or corrupt")
			}
			bodyAvailability := conversationBodyRetained
			if record, available := selectedSourceRecord(ctx, request, selected.view); available && record.Availability == memory.SourceAvailable {
				sourceTurns, sourceCoverage, sourceLoaded, _, readErr := loadConversationSource(ctx, request, selected.view, record)
				if readErr != nil {
					return readErr
				}
				if sourceLoaded {
					turns, coverage = sourceTurns, sourceCoverage
					if err := mergeRetainedEvidence(turns, selected.document); err != nil {
						return publicError(CodeInvalidArgument, "published retained conversation does not match source")
					}
					bodyAvailability = conversationBodySource
				}
			}
			page, err = selectedSnapshotConversationPage(request, selected, turns, coverage, bodyAvailability)
			return err
		}
		view := authenticated.view
		record, err := sourcecatalog.ReadAuthenticated(ctx, request.DataRoot, request.Provider, request.SessionID, view.SourceRecordDigest)
		if err != nil || record.SourceIdentity != view.SourceIdentity {
			return publicError("source_unavailable", "authenticated source prefix is unavailable")
		}
		associated := false
		for _, id := range record.ProjectIDs {
			if id == request.ProjectID {
				associated = true
			}
		}
		if !associated {
			return publicError("source_unavailable", "source project association is unavailable")
		}
		retained, retainedErr := selectRetainedConversation(ctx, authenticated, record)
		if retainedErr != nil {
			var contractErr *Error
			if errors.As(retainedErr, &contractErr) {
				return retainedErr
			}
			return publicError(CodeInvalidArgument, "published retained conversation is unavailable or corrupt")
		}
		var turns []conversationchain.VisibleTurn
		var coverage conversationchain.VisibleCoverage
		bodyAvailability := ""
		evidenceView := view.Digest
		turns, coverage, sourceLoaded, visibleReaderUnsupported, err := loadConversationSource(ctx, request, view, record)
		if err != nil {
			return err
		}
		if sourceLoaded {
			bodyAvailability = conversationBodySource
		}
		if !sourceLoaded && retained != nil {
			turns, coverage, err = retainedVisibleConversation(*retained)
			if err != nil {
				return publicError(CodeInvalidArgument, "published retained conversation is unavailable or corrupt")
			}
			bodyAvailability = conversationBodyRetained
			evidenceView = retained.view.Digest
		} else if sourceLoaded && retained != nil {
			if err := mergeRetainedEvidence(turns, retained.document); err != nil {
				return publicError(CodeInvalidArgument, "published retained conversation does not match source")
			}
		}
		if !sourceLoaded && retained == nil {
			if visibleReaderUnsupported || hasSessionDiagnostic(view, "visible_reader_unsupported") {
				return publicError("visible_reader_unsupported", "authenticated visible conversation reader is unavailable")
			}
			if record.Availability != memory.SourceAvailable {
				return publicError("retained_evidence_unavailable", "retained conversation evidence is unavailable")
			}
			return publicError("source_unavailable", "authenticated source prefix is unavailable")
		}
		page, err = conversationPage(request, view, record, turns, coverage, evidenceView, bodyAvailability)
		return err
	})
	if err != nil {
		return ConversationPage{}, err
	}
	return page, nil
}

func loadConversationSource(ctx context.Context, request ConversationRequest, view memory.SessionView, record memory.SourceRecord) ([]conversationchain.VisibleTurn, conversationchain.VisibleCoverage, bool, bool, error) {
	if record.Availability != memory.SourceAvailable {
		return nil, conversationchain.VisibleCoverage{}, false, false, nil
	}
	visible, sourceCoverage, err := readPublishedVisible(ctx, record)
	if context.Cause(ctx) != nil {
		return nil, conversationchain.VisibleCoverage{}, false, false, publicError(CodeInvalidArgument, "inspection timed out")
	}
	if errors.Is(err, source.ErrVisibleReaderUnsupported) {
		return nil, conversationchain.VisibleCoverage{}, false, true, nil
	}
	if err != nil {
		return nil, conversationchain.VisibleCoverage{}, false, false, nil
	}
	redactor := redact.Default()
	for i := range visible {
		if err := inspectionCheckpoint(ctx, "conversation_message"); err != nil {
			return nil, conversationchain.VisibleCoverage{}, false, false, publicError(CodeInvalidArgument, "inspection timed out")
		}
		visible[i].Text = redactAbsolutePaths(redactor.Text(visible[i].Text).Text)
	}
	turns, coverage := conversationchain.MaterializeVisible(request.Provider, request.SessionID, view.SourceIdentity, visible)
	coverage.SourceRecords = sourceCoverage.SourceRecords
	coverage.OversizedRecords = sourceCoverage.OversizedRecords
	coverage.MalformedRecords = sourceCoverage.MalformedRecords
	coverage.Complete = sourceCoverage.Complete && coverage.TruncatedBodies == 0 && coverage.OrphanMessages == 0
	conversationchain.ApplyVisibleCoverage(turns, coverage)
	return turns, coverage, true, false, nil
}

func readPublishedVisible(ctx context.Context, record memory.SourceRecord) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage, error) {
	if record.Provider != "codex" {
		return nil, conversationchain.VisibleCoverage{}, &source.UnsupportedCapabilityError{Provider: record.Provider}
	}
	resolved, err := platform.ResolveSessionsRoot("", platform.CurrentEnv())
	if err != nil {
		return nil, conversationchain.VisibleCoverage{}, err
	}
	return codex.ReadPublishedVisible(ctx, resolved.Path, record)
}

func hasSessionDiagnostic(view memory.SessionView, code string) bool {
	for _, diagnostic := range view.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func RenderConversationPage(page ConversationPage) ([]byte, error) {
	if validateIdentity(page.SchemaVersion, page.MinimumReaderVersion, page.ProjectID, page.Provider, page.SessionID, page.GenerationID, page.SessionViewDigest) != nil || !digestRE.MatchString(page.DependencyDigest) || page.RedactionVersion != conversationRedactionVersion || page.Total > maxWireInteger || page.Mode != "turn_index" && page.Mode != "turn_messages" || page.RangeStart > page.RangeEnd || page.RangeEnd > page.Total || len(page.TurnUnits) > 64 || len(page.Messages) > 64 || page.BodyAvailability != "" && page.BodyAvailability != conversationBodySource && page.BodyAvailability != conversationBodyRetained || page.EvidenceSessionViewDigest != nil && !digestRE.MatchString(*page.EvidenceSessionViewDigest) {
		return nil, publicError(CodeInvalidArgument, "conversation page is invalid")
	}
	if page.Mode == "turn_index" && (uint64(len(page.TurnUnits)) != page.RangeEnd-page.RangeStart || len(page.Messages) != 0 || page.TurnUnitID != nil) || page.Mode == "turn_messages" && (uint64(len(page.Messages)) != page.RangeEnd-page.RangeStart || len(page.TurnUnits) != 1 || page.TurnUnitID == nil || page.TurnUnits[0].TurnUnitID != *page.TurnUnitID) {
		return nil, publicError(CodeInvalidArgument, "conversation page range is inconsistent")
	}
	if page.BodyAvailability == conversationBodyRetained {
		for _, message := range page.Messages {
			if message.Text != nil || message.TextTruncated || message.Phase != nil {
				return nil, publicError(CodeInvalidArgument, "retained conversation body state is invalid")
			}
		}
	}
	for _, cursor := range []*string{page.FirstCursor, page.PreviousCursor, page.NextCursor, page.LastCursor} {
		if cursor != nil && (*cursor == "" || len(*cursor) > 4096 || !utf8.ValidString(*cursor)) {
			return nil, publicError(CodeInvalidArgument, "conversation cursor is invalid")
		}
	}
	if page.Total == 0 {
		if page.RangeStart != 0 || page.RangeEnd != 0 || page.FirstCursor != nil || page.PreviousCursor != nil || page.NextCursor != nil || page.LastCursor != nil {
			return nil, publicError(CodeInvalidArgument, "empty conversation page has invalid cursors")
		}
	} else if page.FirstCursor == nil || page.LastCursor == nil || (page.RangeStart > 0) != (page.PreviousCursor != nil) || (page.RangeEnd < page.Total) != (page.NextCursor != nil) {
		return nil, publicError(CodeInvalidArgument, "conversation page cursor topology is invalid")
	}
	coverage := page.Coverage
	for _, count := range []uint64{coverage.SourceRecords, coverage.VisibleMessages, coverage.CapturedMessages, coverage.TruncatedMessages, coverage.TruncatedBodies, coverage.ContextMessages, coverage.OrphanMessages, coverage.OversizedRecords, coverage.MalformedRecords} {
		if count > maxWireInteger {
			return nil, publicError(CodeInvalidArgument, "conversation coverage is invalid")
		}
	}
	diagnosticsKnown := coverage.DiagnosticsAvailable == nil || *coverage.DiagnosticsAvailable
	if diagnosticsKnown && coverage.CapturedMessages+coverage.ContextMessages+coverage.OrphanMessages != coverage.VisibleMessages || !diagnosticsKnown && coverage.CapturedMessages > coverage.VisibleMessages || coverage.TruncatedMessages > coverage.CapturedMessages || coverage.TruncatedBodies > coverage.CapturedMessages || coverage.VisibleMessages+coverage.OversizedRecords+coverage.MalformedRecords > coverage.SourceRecords || !diagnosticsKnown && coverage.Complete || diagnosticsKnown && coverage.Complete != (coverage.OversizedRecords == 0 && coverage.MalformedRecords == 0 && coverage.OrphanMessages == 0 && coverage.TruncatedBodies == 0) {
		return nil, publicError(CodeInvalidArgument, "conversation coverage is inconsistent")
	}
	for _, turn := range page.TurnUnits {
		if !validID(turn.TurnUnitID) || turn.Ordinal == 0 || turn.Ordinal > maxWireInteger || turn.ActionCount > maxWireInteger || turn.ResultCount > maxWireInteger || len(turn.StartedAt) > 128 || turn.EndedAt != nil && len(*turn.EndedAt) > 128 || turn.UserMessage.Role != conversationchain.RoleUser || turn.UserMessage.Text != nil || turn.AnswerState != conversationchain.AnswerNone && turn.AnswerState != conversationchain.AnswerPartial && turn.AnswerState != conversationchain.AnswerAnswered {
			return nil, publicError(CodeInvalidArgument, "conversation turn is invalid")
		}
		if err := validateVisibleMessage(turn.UserMessage); err != nil {
			return nil, err
		}
		if turn.UserMessage.SourceRef.Provider != page.Provider || turn.UserMessage.SourceRef.SessionID != page.SessionID {
			return nil, publicError(CodeInvalidArgument, "conversation source reference is invalid")
		}
	}
	for _, message := range page.Messages {
		if err := validateVisibleMessage(message); err != nil {
			return nil, err
		}
		if page.BodyAvailability != conversationBodyRetained && message.Text == nil || message.SourceRef.Provider != page.Provider || message.SourceRef.SessionID != page.SessionID {
			return nil, publicError(CodeInvalidArgument, "conversation source reference is invalid")
		}
	}
	if page.Mode == "turn_index" && (len(page.Actions) != 0 || len(page.Results) != 0 || page.ActionTotal != 0 || page.ResultTotal != 0 || page.EvidenceTruncated) {
		return nil, publicError(CodeInvalidArgument, "conversation index page contains selected evidence")
	}
	if page.Mode == "turn_messages" {
		turn := page.TurnUnits[0]
		if page.ActionTotal != turn.ActionCount || page.ResultTotal != turn.ResultCount || uint64(len(page.Actions)) > page.ActionTotal || uint64(len(page.Results)) > page.ResultTotal || page.EvidenceTruncated != (uint64(len(page.Actions)) < page.ActionTotal || uint64(len(page.Results)) < page.ResultTotal) {
			return nil, publicError(CodeInvalidArgument, "conversation evidence coverage is inconsistent")
		}
		if err := validateConversationEvidenceItems(page.Actions, page.Results, page.Provider, page.SessionID, turn.UserMessage.SourceRef.SourceIdentity); err != nil {
			return nil, err
		}
	}
	body, err := json.Marshal(page)
	if err != nil {
		return nil, publicError(CodeInvalidArgument, "conversation page is invalid")
	}
	if len(body) > MaxConversationResponseBytes {
		return nil, publicError("response_too_large", "conversation page exceeds its byte limit")
	}
	return body, nil
}

func validateVisibleMessage(message conversationchain.VisibleMessage) error {
	if message.Role != conversationchain.RoleUser && message.Role != conversationchain.RoleAssistant || message.Phase != nil && *message.Phase != "commentary" && *message.Phase != "final_answer" || !digestRE.MatchString(message.RevisionID) || !validID(message.SourceRef.SourceIdentity) || message.SourceRef.RecordOrdinal == 0 || message.SourceRef.RecordOrdinal > maxWireInteger || !digestRE.MatchString("sha256:"+message.SourceRef.SourceHash) || len(message.OccurredAt) > 128 || !utf8.ValidString(message.VisibleExcerpt) || len(message.VisibleExcerpt) > 4096 || message.Text != nil && (!utf8.ValidString(*message.Text) || len(*message.Text) > 64<<10) {
		return publicError(CodeInvalidArgument, "conversation message is invalid")
	}
	return nil
}

func conversationPage(request ConversationRequest, view memory.SessionView, source memory.SourceRecord, turns []conversationchain.VisibleTurn, coverage conversationchain.VisibleCoverage, evidenceView, bodyAvailability string) (ConversationPage, error) {
	dependency, err := memory.Digest([]string{view.Digest, evidenceView, view.SourceRecordDigest, source.FrozenBoundary.SourceHash, bodyAvailability, "visible-turn-v1", conversationRedactionVersion})
	if err != nil {
		return ConversationPage{}, publicError(CodeInvalidArgument, "conversation dependencies are invalid")
	}
	return conversationPageBound(request, view, turns, coverage, evidenceView, bodyAvailability, dependency)
}

func selectedSnapshotConversationPage(request ConversationRequest, selected retainedConversation, turns []conversationchain.VisibleTurn, coverage conversationchain.VisibleCoverage, bodyAvailability string) (ConversationPage, error) {
	dependency, err := memory.Digest([]string{selected.view.Digest, selected.document.Digest, selected.document.DependencyDigest, bodyAvailability, "visible-turn-v1", conversationRedactionVersion})
	if err != nil {
		return ConversationPage{}, publicError(CodeInvalidArgument, "conversation dependencies are invalid")
	}
	return conversationPageBound(request, selected.view, turns, coverage, selected.view.Digest, bodyAvailability, dependency)
}

func conversationPageBound(request ConversationRequest, view memory.SessionView, turns []conversationchain.VisibleTurn, coverage conversationchain.VisibleCoverage, evidenceView, bodyAvailability, dependency string) (ConversationPage, error) {
	page := ConversationPage{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", Mode: "turn_index", ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, GenerationID: request.ExpectedGenerationID, SessionViewDigest: view.Digest, DependencyDigest: dependency, RedactionVersion: conversationRedactionVersion, TurnUnits: []conversationchain.VisibleTurn{}, Messages: []conversationchain.VisibleMessage{}, Coverage: coverage, BodyAvailability: bodyAvailability}
	if evidenceView != "" && evidenceView != view.Digest {
		page.EvidenceSessionViewDigest = &evidenceView
	}
	var sizes []int
	var messages []conversationchain.VisibleMessage
	if request.TurnUnitID != "" {
		page.Mode = "turn_messages"
		id := request.TurnUnitID
		page.TurnUnitID = &id
		found := false
		for _, turn := range turns {
			if turn.TurnUnitID == request.TurnUnitID {
				page.TurnUnits = append(page.TurnUnits, turn)
				messages = turn.Messages
				page.ActionTotal = uint64(len(turn.Actions))
				page.ResultTotal = uint64(len(turn.Results))
				page.Actions = append([]conversationchain.Action(nil), turn.Actions[:min(len(turn.Actions), conversationEvidenceMax)]...)
				page.Results = append([]conversationchain.Result(nil), turn.Results[:min(len(turn.Results), conversationEvidenceMax)]...)
				page.EvidenceTruncated = len(page.Actions) != len(turn.Actions) || len(page.Results) != len(turn.Results)
				found = true
				break
			}
		}
		if !found {
			return ConversationPage{}, publicError(CodeInvalidArgument, "selected conversation turn is unavailable")
		}
	}
	baseline, err := json.Marshal(page)
	if err != nil || len(baseline)+conversationCursorReserveBytes >= MaxConversationResponseBytes {
		return ConversationPage{}, publicError("response_too_large", "conversation evidence exceeds its byte limit")
	}
	itemBudget := min(conversationPageItemsBytes, MaxConversationResponseBytes-len(baseline)-conversationCursorReserveBytes)
	if page.Mode == "turn_messages" {
		for _, message := range messages {
			body, _ := json.Marshal(message)
			sizes = append(sizes, len(body)+1)
		}
	} else {
		for _, turn := range turns {
			body, _ := json.Marshal(turn)
			sizes = append(sizes, len(body)+1)
		}
	}
	page.Total = uint64(len(sizes))
	starts := []uint64{0}
	count, bytes := 0, 0
	for i, size := range sizes {
		if size > itemBudget {
			return ConversationPage{}, publicError("response_too_large", "conversation item exceeds its byte limit")
		}
		if count > 0 && (count == request.Limit || bytes+size > itemBudget) {
			starts = append(starts, uint64(i))
			count = 0
			bytes = 0
		}
		count++
		bytes += size
	}
	keyMaterial := fmt.Sprintf("conversation-page-v1\x00%x\x00%s\x00%s\x00%s", cursorAuthenticationKey(view), dependency, page.Mode, request.TurnUnitID)
	key := sha256.Sum256([]byte(keyMaterial))
	eventRequest := EventPageRequest{ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, ExpectedGenerationID: request.ExpectedGenerationID, Limit: request.Limit, Cursor: request.Cursor}
	if page.Mode == "turn_messages" {
		eventRequest.Cursor = request.MessageCursor
	}
	var offset uint64
	if eventRequest.Cursor != "" {
		cursor, err := decodeEventCursor(eventRequest.Cursor, key[:])
		if err != nil || cursor.ProjectID != request.ProjectID || cursor.Provider != request.Provider || cursor.SessionID != request.SessionID || cursor.GenerationID != request.ExpectedGenerationID || cursor.ViewDigest != view.Digest || cursor.Limit != request.Limit || cursor.Offset >= page.Total {
			return ConversationPage{}, publicError(CodeStaleCursor, "cursor does not match the published conversation page")
		}
		offset = cursor.Offset
	}
	at := -1
	for i, start := range starts {
		if offset == start {
			at = i
			break
		}
	}
	if at < 0 {
		return ConversationPage{}, publicError(CodeStaleCursor, "conversation cursor is not a page boundary")
	}
	page.RangeStart = offset
	page.RangeEnd = page.Total
	if at+1 < len(starts) {
		page.RangeEnd = starts[at+1]
	}
	if page.Mode == "turn_index" {
		page.TurnUnits = append(page.TurnUnits, turns[page.RangeStart:page.RangeEnd]...)
	} else {
		page.Messages = append(page.Messages, messages[page.RangeStart:page.RangeEnd]...)
	}
	if page.Total > 0 {
		page.FirstCursor = cursorPointer(eventRequest, view.Digest, key[:], 0)
		page.LastCursor = cursorPointer(eventRequest, view.Digest, key[:], starts[len(starts)-1])
		if at > 0 {
			page.PreviousCursor = cursorPointer(eventRequest, view.Digest, key[:], starts[at-1])
		}
		if at+1 < len(starts) {
			page.NextCursor = cursorPointer(eventRequest, view.Digest, key[:], starts[at+1])
		}
	}
	if _, err := RenderConversationPage(page); err != nil {
		return ConversationPage{}, err
	}
	return page, nil
}
