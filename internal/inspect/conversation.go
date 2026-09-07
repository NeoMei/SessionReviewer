package inspect

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/source/codex"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
	"unicode/utf8"
)

type ConversationRequest struct {
	DataRoot, ProjectID, Provider, SessionID, ExpectedGenerationID string
	TurnUnitID, Cursor, MessageCursor                              string
	Limit                                                          int
}

const MaxConversationResponseBytes = 1 << 20
const conversationPageItemsBytes = 768 << 10
const conversationRedactionVersion = "visible-redaction-v1"

// ConversationPage is a separate paged contract; it is not a truncated
// conversation-chain-v1 Document or a session-event-page-v1 event page.
type ConversationPage struct {
	SchemaVersion        int                                `json:"schema_version"`
	MinimumReaderVersion string                             `json:"minimum_reader_version"`
	Mode                 string                             `json:"mode"`
	ProjectID            string                             `json:"project_id"`
	Provider             string                             `json:"provider"`
	SessionID            string                             `json:"session_id"`
	GenerationID         string                             `json:"generation_id"`
	SessionViewDigest    string                             `json:"session_view_digest"`
	DependencyDigest     string                             `json:"dependency_digest"`
	RedactionVersion     string                             `json:"redaction_version"`
	TurnUnitID           *string                            `json:"turn_unit_id"`
	Total                uint64                             `json:"total"`
	RangeStart           uint64                             `json:"range_start"`
	RangeEnd             uint64                             `json:"range_end"`
	FirstCursor          *string                            `json:"first_cursor"`
	PreviousCursor       *string                            `json:"previous_cursor"`
	NextCursor           *string                            `json:"next_cursor"`
	LastCursor           *string                            `json:"last_cursor"`
	TurnUnits            []conversationchain.VisibleTurn    `json:"turn_units"`
	Messages             []conversationchain.VisibleMessage `json:"messages"`
	Coverage             conversationchain.VisibleCoverage  `json:"coverage"`
}

func LoadConversationPage(ctx context.Context, request ConversationRequest) (ConversationPage, error) {
	if request.Limit < 1 || request.Limit > 64 || request.Cursor != "" && request.TurnUnitID != "" || request.MessageCursor != "" && request.TurnUnitID == "" || len(request.Cursor) > 8192 || len(request.MessageCursor) > 8192 || len(request.TurnUnitID) > 256 {
		return ConversationPage{}, publicError(CodeInvalidArgument, "conversation request is invalid")
	}
	// Private schema v1 currently admits Codex only. Report the capability
	// boundary without opening any store or source for other providers.
	if request.Provider != "codex" {
		return ConversationPage{}, publicError("unsupported_provider", "visible conversation is unsupported for this provider")
	}
	var page ConversationPage
	_, err := inspectPublishedSession(ctx, EventPageRequest{DataRoot: request.DataRoot, ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, ExpectedGenerationID: request.ExpectedGenerationID, Limit: request.Limit}, func(view memory.SessionView) error {
		record, err := sourcecatalog.ReadAuthenticated(ctx, request.DataRoot, request.Provider, request.SessionID, view.SourceRecordDigest)
		if err != nil || record.SourceIdentity != view.SourceIdentity || record.Availability != memory.SourceAvailable {
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
		root, err := codex.SessionsRoot()
		if err != nil {
			return publicError("source_unavailable", "authenticated source prefix is unavailable")
		}
		source, sourceCoverage, err := codex.ReadPublishedVisible(ctx, root, record)
		if context.Cause(ctx) != nil {
			return publicError(CodeInvalidArgument, "inspection timed out")
		}
		if err != nil {
			return publicError("source_unavailable", "authenticated source prefix is unavailable")
		}
		redactor := redact.Default()
		for i := range source {
			if err := inspectionCheckpoint(ctx, "conversation_message"); err != nil {
				return publicError(CodeInvalidArgument, "inspection timed out")
			}
			source[i].Text = redactAbsolutePaths(redactor.Text(source[i].Text).Text)
		}
		turns, coverage := conversationchain.MaterializeVisible(request.Provider, request.SessionID, view.SourceIdentity, source)
		coverage.SourceRecords = sourceCoverage.SourceRecords
		coverage.OversizedRecords = sourceCoverage.OversizedRecords
		coverage.MalformedRecords = sourceCoverage.MalformedRecords
		coverage.Complete = coverage.OversizedRecords == 0 && coverage.MalformedRecords == 0 && coverage.OrphanMessages == 0
		page, err = conversationPage(request, view, record, turns, coverage)
		return err
	})
	if err != nil {
		return ConversationPage{}, err
	}
	return page, nil
}

func RenderConversationPage(page ConversationPage) ([]byte, error) {
	if validateIdentity(page.SchemaVersion, page.MinimumReaderVersion, page.ProjectID, page.Provider, page.SessionID, page.GenerationID, page.SessionViewDigest) != nil || page.Provider != "codex" || !digestRE.MatchString(page.DependencyDigest) || page.RedactionVersion != conversationRedactionVersion || page.Total > maxWireInteger || page.Mode != "turn_index" && page.Mode != "turn_messages" || page.RangeStart > page.RangeEnd || page.RangeEnd > page.Total || len(page.TurnUnits) > 64 || len(page.Messages) > 64 {
		return nil, publicError(CodeInvalidArgument, "conversation page is invalid")
	}
	if page.Mode == "turn_index" && (uint64(len(page.TurnUnits)) != page.RangeEnd-page.RangeStart || len(page.Messages) != 0 || page.TurnUnitID != nil) || page.Mode == "turn_messages" && (uint64(len(page.Messages)) != page.RangeEnd-page.RangeStart || len(page.TurnUnits) != 1 || page.TurnUnitID == nil || page.TurnUnits[0].TurnUnitID != *page.TurnUnitID) {
		return nil, publicError(CodeInvalidArgument, "conversation page range is inconsistent")
	}
	for _, cursor := range []*string{page.FirstCursor, page.PreviousCursor, page.NextCursor, page.LastCursor} {
		if cursor != nil && (len(*cursor) > 4096 || !utf8.ValidString(*cursor)) {
			return nil, publicError(CodeInvalidArgument, "conversation cursor is invalid")
		}
	}
	coverage := page.Coverage
	for _, count := range []uint64{coverage.SourceRecords, coverage.VisibleMessages, coverage.CapturedMessages, coverage.TruncatedMessages, coverage.TruncatedBodies, coverage.ContextMessages, coverage.OrphanMessages, coverage.OversizedRecords, coverage.MalformedRecords} {
		if count > maxWireInteger {
			return nil, publicError(CodeInvalidArgument, "conversation coverage is invalid")
		}
	}
	if coverage.CapturedMessages+coverage.ContextMessages+coverage.OrphanMessages != coverage.VisibleMessages || coverage.TruncatedMessages > coverage.CapturedMessages || coverage.TruncatedBodies > coverage.TruncatedMessages || coverage.VisibleMessages+coverage.OversizedRecords+coverage.MalformedRecords > coverage.SourceRecords || coverage.Complete != (coverage.OversizedRecords == 0 && coverage.MalformedRecords == 0 && coverage.OrphanMessages == 0) {
		return nil, publicError(CodeInvalidArgument, "conversation coverage is inconsistent")
	}
	for _, turn := range page.TurnUnits {
		if !validID(turn.TurnUnitID) || turn.Ordinal == 0 || turn.Ordinal > maxWireInteger || len(turn.StartedAt) > 128 || turn.EndedAt != nil && len(*turn.EndedAt) > 128 || turn.UserMessage.Role != conversationchain.RoleUser || turn.UserMessage.Text != nil || turn.AnswerState != conversationchain.AnswerNone && turn.AnswerState != conversationchain.AnswerPartial && turn.AnswerState != conversationchain.AnswerAnswered {
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
		if message.Text == nil || message.SourceRef.Provider != page.Provider || message.SourceRef.SessionID != page.SessionID {
			return nil, publicError(CodeInvalidArgument, "conversation source reference is invalid")
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

func conversationPage(request ConversationRequest, view memory.SessionView, source memory.SourceRecord, turns []conversationchain.VisibleTurn, coverage conversationchain.VisibleCoverage) (ConversationPage, error) {
	dependency, err := memory.Digest([]string{view.Digest, view.SourceRecordDigest, source.FrozenBoundary.SourceHash, "visible-turn-v1", conversationRedactionVersion})
	if err != nil {
		return ConversationPage{}, publicError(CodeInvalidArgument, "conversation dependencies are invalid")
	}
	page := ConversationPage{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", Mode: "turn_index", ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, GenerationID: request.ExpectedGenerationID, SessionViewDigest: view.Digest, DependencyDigest: dependency, RedactionVersion: conversationRedactionVersion, TurnUnits: []conversationchain.VisibleTurn{}, Messages: []conversationchain.VisibleMessage{}, Coverage: coverage}
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
				found = true
				break
			}
		}
		if !found {
			return ConversationPage{}, publicError(CodeInvalidArgument, "selected conversation turn is unavailable")
		}
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
		if size > conversationPageItemsBytes {
			return ConversationPage{}, publicError("response_too_large", "conversation item exceeds its byte limit")
		}
		if count > 0 && (count == request.Limit || bytes+size > conversationPageItemsBytes) {
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
