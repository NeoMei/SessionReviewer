package conversationchain

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SourceMessage contains only an authenticated, explicitly visible source
// message. Callers redact text before materialization; no machine tools run.
type SourceMessage struct {
	Role                         Role
	Phase                        string
	Text, OccurredAt, RecordHash string
	RecordOrdinal                uint64
}

type VisibleMessage struct {
	Role           Role      `json:"role"`
	Phase          *string   `json:"phase"`
	RevisionID     string    `json:"revision_id"`
	SourceRef      SourceRef `json:"source_ref"`
	OccurredAt     string    `json:"occurred_at"`
	VisibleExcerpt string    `json:"visible_excerpt"`
	Truncated      bool      `json:"truncated"`
	Text           *string   `json:"text"`
	TextTruncated  bool      `json:"text_truncated"`
}

type VisibleTurn struct {
	TurnUnitID            string         `json:"turn_unit_id"`
	Ordinal               uint64         `json:"ordinal"`
	StartedAt             string         `json:"started_at"`
	EndedAt               *string        `json:"ended_at"`
	UserMessage           VisibleMessage `json:"user_message"`
	AnswerState           AnswerState    `json:"answer_state"`
	AssistantMessageCount uint64         `json:"assistant_message_count"`
	// Messages stay private to materialization and are paged separately.
	Messages []VisibleMessage `json:"-"`
}

type VisibleCoverage struct {
	SourceRecords     uint64 `json:"source_records"`
	VisibleMessages   uint64 `json:"visible_messages"`
	CapturedMessages  uint64 `json:"captured_messages"`
	TruncatedMessages uint64 `json:"truncated_messages"`
	TruncatedBodies   uint64 `json:"truncated_bodies"`
	ContextMessages   uint64 `json:"context_messages"`
	OrphanMessages    uint64 `json:"orphan_messages"`
	OversizedRecords  uint64 `json:"oversized_records"`
	MalformedRecords  uint64 `json:"malformed_records"`
	Complete          bool   `json:"complete"`
}

// VisibleUserText removes only known leading ambient envelopes. Arbitrary XML,
// code blocks and quoted user content are preserved. A source-only environment
// message must not split a real question from its answer.
func VisibleUserText(text string) string {
	value := strings.TrimSpace(text)
	ambient := false
	for {
		changed := false
		if strings.HasPrefix(value, "# AGENTS.md instructions\n") {
			rest := strings.TrimSpace(strings.TrimPrefix(value, "# AGENTS.md instructions\n"))
			if strings.HasPrefix(rest, "<INSTRUCTIONS>") {
				if at := strings.Index(rest, "</INSTRUCTIONS>"); at >= 0 {
					value = strings.TrimSpace(rest[at+len("</INSTRUCTIONS>"):])
					changed = true
					ambient = true
				}
			}
		}
		if strings.HasPrefix(value, `<in-app-browser-context source="ambient-ui-state">`) {
			if at := strings.Index(value, "</in-app-browser-context>"); at >= 0 {
				value = strings.TrimSpace(value[at+len("</in-app-browser-context>"):])
				changed = true
				ambient = true
			}
		}
		for _, tag := range []string{"environment_context", "recommended_plugins"} {
			start, end := "<"+tag+">", "</"+tag+">"
			if strings.HasPrefix(value, start) {
				if at := strings.Index(value, end); at >= 0 {
					value = strings.TrimSpace(value[at+len(end):])
					changed = true
					ambient = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}
	if ambient && strings.HasPrefix(value, "## My request:") {
		value = strings.TrimSpace(strings.TrimPrefix(value, "## My request:"))
	}
	if strings.HasPrefix(value, "# Context from my IDE setup:") {
		if at := strings.Index(value, "## My request:"); at >= 0 {
			value = strings.TrimSpace(value[at+len("## My request:"):])
		}
	}
	return value
}

// MaterializeVisible starts each unit at a real visible user request. An
// assistant final_answer completes it; commentary alone remains partial.
func MaterializeVisible(provider, sessionID, sourceIdentity string, messages []SourceMessage) ([]VisibleTurn, VisibleCoverage) {
	turns := []VisibleTurn{}
	coverage := VisibleCoverage{Complete: true}
	for _, source := range messages {
		coverage.VisibleMessages++
		if source.Role == RoleUser {
			source.Text = VisibleUserText(source.Text)
			if source.Text == "" {
				coverage.ContextMessages++
				continue
			}
		}
		if source.Role != RoleUser && source.Role != RoleAssistant {
			continue
		}
		if source.Role == RoleAssistant && len(turns) == 0 {
			coverage.OrphanMessages++
			continue
		}
		text, textTruncated := boundedVisibleText(source.Text, 64<<10)
		if textTruncated {
			coverage.TruncatedBodies++
		}
		excerpt := text
		truncated := len(excerpt) > 4096
		if truncated {
			end := 4096 - len("…")
			for end > 0 && !utf8.RuneStart(excerpt[end]) {
				end--
			}
			excerpt = excerpt[:end] + "…"
			coverage.TruncatedMessages++
		}
		ref := SourceRef{Provider: provider, SessionID: sessionID, SourceIdentity: sourceIdentity, RecordOrdinal: source.RecordOrdinal, SourceHash: source.RecordHash}
		key := sourceIdentity + "\x00" + strconv.FormatUint(source.RecordOrdinal, 10)
		revision := sha256.Sum256([]byte(key + "\x00" + source.RecordHash))
		message := VisibleMessage{Role: source.Role, RevisionID: "sha256:" + hex.EncodeToString(revision[:]), SourceRef: ref, OccurredAt: source.OccurredAt, VisibleExcerpt: excerpt, Truncated: truncated, Text: &text, TextTruncated: textTruncated}
		if source.Phase != "" {
			phase := source.Phase
			message.Phase = &phase
		}
		coverage.CapturedMessages++
		if source.Role == RoleUser {
			if len(turns) > 0 {
				at := source.OccurredAt
				turns[len(turns)-1].EndedAt = &at
			}
			id := sha256.Sum256([]byte(provider + "\x00" + sessionID + "\x00" + key))
			preview := message
			preview.Text = nil
			preview.TextTruncated = false
			turns = append(turns, VisibleTurn{TurnUnitID: "turn-" + hex.EncodeToString(id[:]), Ordinal: uint64(len(turns) + 1), StartedAt: source.OccurredAt, UserMessage: preview, AnswerState: AnswerNone, Messages: []VisibleMessage{message}})
		} else {
			turn := &turns[len(turns)-1]
			turn.Messages = append(turn.Messages, message)
			turn.AssistantMessageCount++
			if source.Phase == "final_answer" || source.Phase == "" {
				turn.AnswerState = AnswerAnswered
			} else if turn.AnswerState == AnswerNone {
				turn.AnswerState = AnswerPartial
			}
		}
	}
	return turns, coverage
}

func boundedVisibleText(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	end := limit - len("…")
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "…", true
}
