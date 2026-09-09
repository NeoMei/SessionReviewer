// Package opencode reads explicit, authenticated OpenCode SQLite sources without
// starting OpenCode or writing to its store.
package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

const maxContentBytes = 128 << 20
const maxRows = 100000
const maxSessions = 65536

type sessionRow struct {
	ID        string
	Directory string
	Created   int64
}
type rawPart struct {
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}
type rawMessage struct {
	ID      string          `json:"id"`
	Created int64           `json:"created"`
	Data    json.RawMessage `json:"data"`
	Parts   []rawPart       `json:"parts"`
}
type messageInfo struct {
	Role    string `json:"role"`
	Finish  string `json:"finish"`
	Summary bool   `json:"summary"`
	Time    struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Model    string `json:"modelID"`
	Provider string `json:"providerID"`
	Tokens   *struct {
		Input     *int64          `json:"input"`
		Output    *int64          `json:"output"`
		Reasoning *int64          `json:"reasoning"`
		Total     json.RawMessage `json:"total"`
		Cache     *struct {
			Read  *int64 `json:"read"`
			Write *int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
}
type partInfo struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Synthetic bool   `json:"synthetic"`
	Ignored   bool   `json:"ignored"`
	CallID    string `json:"callID"`
	Tool      string `json:"tool"`
	State     struct {
		Status   string                     `json:"status"`
		Input    json.RawMessage            `json:"input"`
		Output   string                     `json:"output"`
		Error    string                     `json:"error"`
		Metadata map[string]json.RawMessage `json:"metadata"`
	} `json:"state"`
}
type canonicalRecord struct {
	Raw     []byte
	Hash    string
	Info    messageInfo
	Parts   []partInfo
	PartIDs []string
	Created int64
}

func normalizeObject(raw []byte, ids map[string]string) ([]byte, error) {
	var obj map[string]any
	if err := strictjson.Decode(raw, &obj); err != nil || obj == nil {
		return nil, errors.New("invalid OpenCode JSON object")
	}
	for name, want := range ids {
		if got, ok := obj[name]; ok && got != want {
			return nil, fmt.Errorf("OpenCode %s mismatch", name)
		}
	}
	return json.Marshal(obj)
}

func stableRecords(row sessionRow, messages []rawMessage) ([]canonicalRecord, bool, error) {
	records := make([]canonicalRecord, 0, len(messages))
	stable := 0
	turnStart := -1
	blocked := false
	bytes := 0
	for _, m := range messages {
		var err error
		m.Data, err = normalizeObject(m.Data, map[string]string{"id": m.ID, "sessionID": row.ID})
		if err != nil {
			return nil, false, err
		}
		var info messageInfo
		if json.Unmarshal(m.Data, &info) != nil || (info.Role != "user" && info.Role != "assistant") || info.Time.Created < 1 || m.Created < 1 {
			return nil, false, errors.New("unsupported OpenCode message schema")
		}
		if info.Time.Created != m.Created {
			return nil, false, errors.New("OpenCode message timestamp mismatch")
		}
		if info.Role == "user" {
			if turnStart >= stable {
				blocked = true
			}
			turnStart = len(records)
		}
		if turnStart < 0 {
			return nil, false, errors.New("OpenCode assistant has no owning user")
		}
		// A later assistant record still belongs to this user. Its unfinished
		// suffix can invalidate an earlier apparent final answer in that turn.
		if info.Role == "assistant" && stable > turnStart {
			stable = turnStart
		}
		parts := make([]partInfo, 0, len(m.Parts))
		ids := make([]string, 0, len(m.Parts))
		for i := range m.Parts {
			p := &m.Parts[i]
			p.Data, err = normalizeObject(p.Data, map[string]string{"id": p.ID, "sessionID": row.ID, "messageID": m.ID})
			if err != nil {
				return nil, false, err
			}
			var part partInfo
			if json.Unmarshal(p.Data, &part) != nil {
				return nil, false, errors.New("invalid OpenCode part")
			}
			switch part.Type {
			case "text", "reasoning", "step-start", "step-finish", "snapshot", "patch", "agent", "retry", "compaction", "subtask", "file":
			case "tool":
				if part.CallID == "" || part.Tool == "" {
					return nil, false, errors.New("OpenCode tool identity missing")
				}
				switch part.State.Status {
				case "completed", "error":
				case "pending", "running":
					blocked = true
				default:
					return nil, false, errors.New("unsupported OpenCode tool state")
				}
			default:
				return nil, false, errors.New("unsupported OpenCode part type")
			}
			parts = append(parts, part)
			ids = append(ids, p.ID)
		}
		raw, err := json.Marshal(struct {
			Session   string     `json:"session"`
			Directory string     `json:"directory"`
			Started   int64      `json:"started"`
			Message   rawMessage `json:"message"`
		}{row.ID, row.Directory, row.Created, m})
		if err != nil {
			return nil, false, err
		}
		bytes += len(raw)
		if bytes > maxContentBytes {
			return nil, false, errors.New("OpenCode content budget exceeded")
		}
		records = append(records, canonicalRecord{Raw: raw, Hash: hashBytes(raw), Info: info, Parts: parts, PartIDs: ids, Created: m.Created})
		if !blocked && info.Role == "assistant" && info.Time.Completed >= info.Time.Created && info.Time.Completed > 0 && info.Finish != "" && info.Finish != "tool-calls" && info.Finish != "unknown" {
			stable = len(records)
		}
	}
	return append([]canonicalRecord(nil), records[:stable]...), stable < len(records), nil
}
func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func prefixHash(records []canonicalRecord) string {
	h := sha256.New()
	for _, r := range records {
		h.Write(r.Raw)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func timestamp(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) }
func visibleRecords(records []canonicalRecord, session, identity string) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage) {
	messages := []conversationchain.SourceMessage{}
	contextMessages := uint64(0)
	for i, r := range records {
		if r.Info.Summary {
			contextMessages++
			continue
		}
		var texts []string
		for _, p := range r.Parts {
			if p.Type == "text" && !p.Synthetic && !p.Ignored {
				texts = append(texts, p.Text)
			}
		}
		text := strings.Join(texts, "\n")
		if text == "" {
			continue
		}
		phase := ""
		if r.Info.Role == "assistant" {
			phase = "commentary"
			if r.Info.Finish != "" && r.Info.Finish != "tool-calls" && r.Info.Finish != "unknown" {
				phase = "final_answer"
			}
		}
		messages = append(messages, conversationchain.SourceMessage{Role: conversationchain.Role(r.Info.Role), Phase: phase, Text: redact.Default().Text(text).Text, OccurredAt: timestamp(r.Created), RecordHash: r.Hash, RecordOrdinal: uint64(i + 1)})
	}
	_, coverage := conversationchain.MaterializeVisible("opencode", session, identity, messages)
	coverage.SourceRecords = uint64(len(records))
	coverage.ContextMessages += contextMessages
	return messages, coverage
}
func recordUsage(records []canonicalRecord) (accounting.SessionUsage, error) {
	usage := accounting.SessionUsage{Models: []accounting.ModelUsage{}}
	if len(records) == 0 {
		return usage, errors.New("no finalized OpenCode records")
	}
	usage.StartedAt = timestamp(records[0].Created)
	end := records[len(records)-1].Info.Time.Completed
	usage.EndedAt = timestamp(end)
	usage.DurationMS = end - records[0].Created
	models := map[string]accounting.TokenUsage{}
	for _, r := range records {
		if r.Info.Role != "assistant" {
			continue
		}
		v := r.Info.Tokens
		if v == nil || v.Cache == nil || r.Info.Model == "" || r.Info.Provider == "" {
			return usage, errors.New("missing OpenCode usage")
		}
		for _, n := range []*int64{v.Input, v.Output, v.Reasoning, v.Cache.Read, v.Cache.Write} {
			if n == nil || *n < 0 || *n > 1<<50 {
				return usage, errors.New("invalid OpenCode token count")
			}
		}
		added := accounting.TokenUsage{InputTokens: *v.Input + *v.Cache.Read + *v.Cache.Write, CachedInputTokens: *v.Cache.Read, CacheWriteInputTokens: *v.Cache.Write, OutputTokens: *v.Output + *v.Reasoning, ReasoningOutputTokens: *v.Reasoning, TotalTokens: *v.Input + *v.Output + *v.Reasoning + *v.Cache.Read + *v.Cache.Write}
		if len(v.Total) != 0 {
			var total *int64
			if json.Unmarshal(v.Total, &total) != nil || total == nil || *total != added.TotalTokens {
				return usage, errors.New("OpenCode total tokens do not reconcile")
			}
		}
		name := r.Info.Provider + "/" + r.Info.Model
		current := models[name]
		current.InputTokens += added.InputTokens
		current.CachedInputTokens += added.CachedInputTokens
		current.CacheWriteInputTokens += added.CacheWriteInputTokens
		current.OutputTokens += added.OutputTokens
		current.ReasoningOutputTokens += added.ReasoningOutputTokens
		current.TotalTokens += added.TotalTokens
		if err := accounting.ValidateTokenUsage(current); err != nil {
			return usage, err
		}
		models[name] = current
	}
	names := []string{}
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v := models[name]
		usage.Models = append(usage.Models, accounting.ModelUsage{Model: name, TokenUsage: v})
		usage.TotalTokens += v.TotalTokens
	}
	return usage, accounting.ValidateSessionUsage(&usage)
}

func reconcileSessionTokenTotals(usage accounting.SessionUsage, totals sessionTokenTotals) error {
	if totals.Invalid {
		return errors.New("invalid OpenCode Session token totals")
	}
	expected := map[string]int64{}
	// recordUsage has already validated the complete aggregate safe-integer bound.
	for _, model := range usage.Models {
		expected["tokens_input"] += model.InputTokens - model.CachedInputTokens - model.CacheWriteInputTokens
		expected["tokens_output"] += model.OutputTokens - model.ReasoningOutputTokens
		expected["tokens_reasoning"] += model.ReasoningOutputTokens
		expected["tokens_cache_read"] += model.CachedInputTokens
		expected["tokens_cache_write"] += model.CacheWriteInputTokens
	}
	for name, supplied := range totals.Values {
		if expected[name] != supplied {
			return errors.New("OpenCode Session token totals do not reconcile")
		}
	}
	return nil
}
