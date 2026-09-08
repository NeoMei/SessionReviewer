package codex

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/source"
)

const maxVisibleRecordBytes = 64 << 10

type visibleSegment struct {
	relative string
	file     *os.File
	info     os.FileInfo
	started  time.Time
}
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// ReadPublishedVisible opens only rollout filenames for the authenticated
// logical Session. It hashes exactly the accepted logical prefix while
// decoding bounded records, never reading newer content into the result.
func ReadPublishedVisible(ctx context.Context, root string, record memory.SourceRecord) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage, error) {
	var coverage conversationchain.VisibleCoverage
	if ctx == nil || record.Provider != "codex" || record.FrozenBoundary.Location.JSONL == nil {
		return nil, coverage, errors.New("invalid visible source boundary")
	}
	boundary := record.FrozenBoundary.Location.JSONL
	if boundary.ByteOffset < 1 || boundary.ByteOffset > 1<<30 || boundary.Line < 1 {
		return nil, coverage, errors.New("visible source boundary exceeds budget")
	}
	directory, err := pathguard.Open(root)
	if err != nil {
		return nil, coverage, err
	}
	defer directory.Close()
	segments, err := selectedVisibleSegments(ctx, directory, record.SessionID)
	if err != nil {
		return nil, coverage, err
	}
	defer func() {
		for _, segment := range segments {
			_ = segment.file.Close()
		}
	}()
	readers := []io.Reader{}
	remaining := boundary.ByteOffset
	for _, segment := range segments {
		if remaining == 0 {
			break
		}
		size := segment.info.Size()
		amount := size
		if amount > remaining {
			amount = remaining
		}
		readers = append(readers, io.NewSectionReader(segment.file, 0, amount))
		remaining -= amount
		if amount == size && size > 0 && remaining > 0 {
			var last [1]byte
			if _, err := segment.file.ReadAt(last[:], size-1); err != nil {
				return nil, coverage, err
			}
			if last[0] != '\n' {
				readers = append(readers, strings.NewReader("\n"))
				remaining--
			}
		}
	}
	if remaining != 0 {
		return nil, coverage, errors.New("source prefix shrank")
	}
	hash := sha256.New()
	reader := bufio.NewReaderSize(io.TeeReader(contextReader{ctx, io.MultiReader(readers...)}, hash), maxVisibleRecordBytes)
	messages := []conversationchain.SourceMessage{}
	capturedBytes := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, coverage, err
		}
		raw, oversized, readErr := readVisibleLine(reader)
		if len(raw) == 0 && !oversized && readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != io.EOF {
			return nil, coverage, readErr
		}
		coverage.SourceRecords++
		if oversized {
			coverage.OversizedRecords++
			continue
		}
		trimmed := strings.TrimSpace(string(raw))
		if trimmed == "" {
			continue
		}
		if !utf8.Valid(raw) || !json.Valid(raw) {
			coverage.MalformedRecords++
			continue
		}
		message, visible, malformed := decodeVisibleRecord(raw)
		if malformed {
			coverage.MalformedRecords++
			continue
		}
		if !visible {
			continue
		}
		message.RecordOrdinal = coverage.SourceRecords
		sum := sha256.Sum256([]byte(trimmed))
		message.RecordHash = hex.EncodeToString(sum[:])
		capturedBytes += len(message.Text)
		if capturedBytes > 64<<20 || len(messages) >= 100000 {
			return nil, coverage, errors.New("visible conversation exceeds budget")
		}
		messages = append(messages, message)
	}
	if coverage.SourceRecords != uint64(boundary.Line) || hex.EncodeToString(hash.Sum(nil)) != record.FrozenBoundary.SourceHash {
		return nil, coverage, errors.New("published source prefix hash mismatch")
	}
	// Namespace checks detect redirects/replacements during the read. Logical
	// same-content copies made before this read are valid, as in the adapter.
	current, err := pathguard.Open(root)
	if err != nil {
		return nil, coverage, err
	}
	defer current.Close()
	if !os.SameFile(current.Info(), directory.Info()) {
		return nil, coverage, errors.New("source root changed")
	}
	for _, segment := range segments {
		file, info, err := current.OpenRegular(segment.relative)
		if err != nil {
			return nil, coverage, err
		}
		file.Close()
		if !os.SameFile(info, segment.info) || info.Size() < segment.info.Size() {
			return nil, coverage, errors.New("source file changed")
		}
	}
	_, segmented := conversationchain.MaterializeVisible(record.Provider, record.SessionID, record.SourceIdentity, messages)
	segmented.SourceRecords = coverage.SourceRecords
	segmented.OversizedRecords = coverage.OversizedRecords
	segmented.MalformedRecords = coverage.MalformedRecords
	segmented.Complete = segmented.Complete && segmented.OversizedRecords == 0 && segmented.MalformedRecords == 0 && segmented.OrphanMessages == 0 && segmented.TruncatedBodies == 0
	return messages, segmented, nil
}

func (a *adapter) ReadVisiblePrefix(ctx context.Context, record memory.SourceRecord) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage, error) {
	if err := memory.ValidateSourceRecord(record); err != nil || record.Provider != providerCodex || record.Availability != memory.SourceAvailable {
		return nil, conversationchain.VisibleCoverage{}, errors.Join(errors.New("invalid Codex visible source record"), err)
	}
	known := make(map[string]struct{}, len(a.bindings))
	for _, binding := range a.bindings {
		known[binding.ProjectID] = struct{}{}
	}
	if len(record.ProjectIDs) == 0 {
		return nil, conversationchain.VisibleCoverage{}, errors.New("Codex visible source has no authenticated project association")
	}
	for _, projectID := range record.ProjectIDs {
		if _, exists := known[projectID]; !exists {
			return nil, conversationchain.VisibleCoverage{}, errors.New("Codex visible source project association is not bound to this adapter")
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(record.SessionID) {
		return nil, conversationchain.VisibleCoverage{}, &source.UnsupportedCapabilityError{Provider: record.Provider}
	}
	return ReadPublishedVisible(ctx, a.sessionsRoot, record)
}

func selectedVisibleSegments(ctx context.Context, directory *pathguard.Directory, sessionID string) (result []visibleSegment, retErr error) {
	// Names are filtered before opening file content, including unrelated
	// malformed or oversized logs. Optional continuation IDs are supported.
	uuid := `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`
	if !regexp.MustCompile("^" + uuid + "$").MatchString(sessionID) {
		return nil, errors.New("unsupported Codex filename identity")
	}
	pattern := regexp.MustCompile(`^rollout-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-` + regexp.QuoteMeta(sessionID) + `(?:[-_]` + uuid + `)?\.jsonl$`)
	defer func() {
		if retErr != nil {
			for _, segment := range result {
				segment.file.Close()
			}
		}
	}()
	entries := 0
	err := fs.WalkDir(directory.Root.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > 1000000 {
			return errors.New("source directory budget exceeded")
		}
		if entry.IsDir() {
			return nil
		}
		if !pattern.MatchString(entry.Name()) {
			return nil
		}
		if len(result) >= 256 {
			return errors.New("source segment budget exceeded")
		}
		file, info, err := directory.OpenRegular(relative)
		if err != nil {
			return err
		}
		keep := false
		defer func() {
			if !keep {
				file.Close()
			}
		}()
		raw, oversized, err := readVisibleLine(bufio.NewReaderSize(contextReader{ctx, io.NewSectionReader(file, 0, maxVisibleRecordBytes+1)}, maxVisibleRecordBytes))
		if err != nil && err != io.EOF || oversized {
			return errors.New("source metadata unavailable")
		}
		var env struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &env) != nil || env.Type != "session_meta" || env.Payload.ID != sessionID {
			return errors.New("source metadata identity mismatch")
		}
		started, err := time.Parse(time.RFC3339Nano, env.Timestamp)
		if err != nil {
			return errors.New("invalid source timestamp")
		}
		result = append(result, visibleSegment{relative: relative, file: file, info: info, started: started})
		keep = true
		return nil
	})
	if err != nil {
		return result, err
	}
	if len(result) == 0 {
		return result, errors.New("source files missing")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].started.Before(result[j].started) })
	for i := 1; i < len(result); i++ {
		if !result[i].started.After(result[i-1].started) {
			return result, errors.New("ambiguous source segment ordering")
		}
	}
	return result, nil
}

func readVisibleLine(reader *bufio.Reader) ([]byte, bool, error) {
	var line []byte
	oversized := false
	for {
		chunk, err := reader.ReadSlice('\n')
		if !oversized {
			if len(line)+len(chunk) > maxVisibleRecordBytes {
				oversized = true
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, oversized, err
	}
}

func decodeVisibleRecord(raw []byte) (conversationchain.SourceMessage, bool, bool) {
	var env struct {
		Timestamp string          `json:"timestamp"`
		Type      string          `json:"type"`
		Payload   json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return conversationchain.SourceMessage{}, false, true
	}
	if env.Type != "response_item" {
		return conversationchain.SourceMessage{}, false, false
	}
	var item struct {
		Type      string                 `json:"type"`
		Role      conversationchain.Role `json:"role"`
		Phase     string                 `json:"phase"`
		Channel   string                 `json:"channel"`
		Recipient string                 `json:"recipient"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(env.Payload, &item) != nil {
		return conversationchain.SourceMessage{}, false, true
	}
	if item.Type != "message" || (item.Role != conversationchain.RoleUser && item.Role != conversationchain.RoleAssistant) || (item.Phase != "" && item.Phase != "commentary" && item.Phase != "final_answer") || (item.Channel != "" && item.Channel != "commentary" && item.Channel != "final") || (item.Recipient != "" && item.Recipient != "all") {
		return conversationchain.SourceMessage{}, false, false
	}
	// Timestamps are source metadata, not trusted display strings. Quarantine
	// malformed visible records before they can populate public time fields.
	if _, err := time.Parse(time.RFC3339Nano, env.Timestamp); err != nil {
		return conversationchain.SourceMessage{}, false, true
	}
	// Older messages supply a visible channel instead of phase. Preserve that
	// completion signal; only messages with neither use the legacy fallback.
	if item.Phase == "" {
		switch item.Channel {
		case "commentary":
			item.Phase = "commentary"
		case "final":
			item.Phase = "final_answer"
		}
	}
	text := []string{}
	for _, part := range item.Content {
		if part.Type == "input_text" || part.Type == "output_text" {
			text = append(text, part.Text)
		}
	}
	value := strings.Join(text, "\n")
	if strings.TrimSpace(value) == "" {
		return conversationchain.SourceMessage{}, false, false
	}
	return conversationchain.SourceMessage{Role: item.Role, Phase: item.Phase, Text: value, OccurredAt: env.Timestamp}, true, false
}

// SessionsRoot follows the same environment precedence as the Codex adapter.
func SessionsRoot() (string, error) {
	root, err := platform.ResolveSessionsRoot("", platform.CurrentEnv())
	return root.Path, err
}
