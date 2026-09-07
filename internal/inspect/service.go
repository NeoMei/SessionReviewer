package inspect

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

const (
	CodeInvalidArgument    = "invalid_argument"
	CodeGenerationMismatch = "generation_mismatch"
	CodeStaleCursor        = "stale_cursor"
	CodeAnchorOutOfRange   = "anchor_out_of_range"
	eventExcerptBytes      = 512
)

// Error is a bounded, public diagnostic for the read-only inspection API.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// EventPageRequest names one immutable published Session event page.
type EventPageRequest struct {
	DataRoot             string
	ProjectID            string
	Provider             string
	SessionID            string
	ExpectedGenerationID string
	Cursor               string
	Anchor               int
	Limit                int
}

type eventCursor struct {
	Version      int    `json:"version"`
	ProjectID    string `json:"project_id"`
	Provider     string `json:"provider"`
	SessionID    string `json:"session_id"`
	GenerationID string `json:"generation_id"`
	ViewDigest   string `json:"view_digest"`
	Limit        int    `json:"limit"`
	Offset       uint64 `json:"offset"`
	Checksum     string `json:"checksum"`
}

// LoadSessionEventPage authenticates an existing configured project and its
// published immutable generation. It never opens source logs or a writable
// store and derives event rows only from the selected validated SessionView.
func LoadSessionEventPage(ctx context.Context, request EventPageRequest) (_ SessionEventPage, retErr error) {
	if ctx == nil {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "inspection context is required")
	}
	if err := context.Cause(ctx); err != nil {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "inspection timed out")
	}
	if !filepath.IsAbs(request.DataRoot) || filepath.Clean(request.DataRoot) != request.DataRoot || request.Limit < 1 || request.Limit > 100 || request.ProjectID == "" || request.Provider == "" || request.SessionID == "" || request.ExpectedGenerationID == "" {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "inspection request is invalid")
	}
	if request.Cursor != "" && request.Anchor != 0 {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "cursor and anchor are mutually exclusive")
	}

	cfg, err := config.Load(filepath.Join(request.DataRoot, "config.toml"))
	if err != nil {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "configured project mapping is unavailable")
	}
	mapping, found := cfg.ProjectByID(request.ProjectID)
	if !found {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "configured project mapping is unavailable")
	}
	binding, err := projectidentity.Resolve(mapping, mapping.Root, runtime.GOOS)
	if err != nil || binding.ProjectID != request.ProjectID {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "configured project mapping is not authenticated")
	}

	store, err := memorystore.OpenReadOnly(request.DataRoot, request.ProjectID)
	if err != nil {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "published private state is unavailable")
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil && retErr == nil {
			retErr = publicError(CodeInvalidArgument, "published private state could not be closed safely")
		}
	}()
	publishedID, manifest, err := store.LoadPublished()
	if err != nil {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "published private state is unavailable")
	}
	if publishedID != request.ExpectedGenerationID || manifest.GenerationID != request.ExpectedGenerationID {
		return SessionEventPage{}, publicError(CodeGenerationMismatch, "published generation does not match the expected generation")
	}
	if manifest.ProjectID != request.ProjectID || manifest.SessionIndexDigest == "" {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "published generation identity is invalid")
	}
	if err := context.Cause(ctx); err != nil {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "inspection timed out")
	}

	projectBody, err := store.LoadObject(memorystore.ObjectProjectView, manifest.ProjectViewDigest)
	if err != nil {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "published project view is unavailable or corrupt")
	}
	var project memory.ProjectView
	if err := strictjson.Decode(projectBody, &project); err != nil || project.ProjectID != request.ProjectID || project.Digest != manifest.ProjectViewDigest || !sameSessionDependencies(project.SessionViewDependencies, manifest.SessionViews) {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "published project view identity is invalid")
	}

	indexBody, err := store.LoadObject(memorystore.ObjectSessionIndex, manifest.SessionIndexDigest)
	if err != nil {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "published Session index is unavailable or corrupt")
	}
	index, err := sessionindex.Parse(indexBody)
	if err != nil || index.ProjectID != request.ProjectID || index.GenerationID != publishedID || index.ProjectViewDigest != manifest.ProjectViewDigest || index.Digest != manifest.SessionIndexDigest {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "published Session index identity is invalid")
	}

	dependency, exists := selectedDependency(manifest, request.Provider, request.SessionID)
	entry, indexed := selectedIndexEntry(index, request.Provider, request.SessionID)
	if !exists || !indexed || entry.SessionViewDigest == nil || *entry.SessionViewDigest != dependency.Digest {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "requested Session is not in the published generation")
	}
	viewBody, err := store.LoadObject(memorystore.ObjectSessionView, dependency.Digest)
	if err != nil {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "published Session view is unavailable or corrupt")
	}
	var view memory.SessionView
	if err := json.Unmarshal(viewBody, &view); err != nil || view.ProjectID != request.ProjectID || view.Provider != request.Provider || view.SessionID != request.SessionID || view.Digest != dependency.Digest {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "published Session view identity is invalid")
	}
	if uint64(len(view.ObservationSummaries)) != entry.IndexedEventCount || entry.Coverage.Indexed != entry.IndexedEventCount {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "published Session event coverage is inconsistent")
	}
	if err := projectidentity.Reauthenticate(binding); err != nil {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "configured project mapping changed during inspection")
	}
	if err := context.Cause(ctx); err != nil {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "inspection timed out")
	}

	items := make([]EventItem, len(view.ObservationSummaries))
	for index, summary := range view.ObservationSummaries {
		kind, ok := publicEventKind(summary.Kind)
		if !ok {
			return SessionEventPage{}, publicError(CodeInvalidArgument, "published Session contains an unsupported event kind")
		}
		items[index] = EventItem{Kind: kind, Excerpt: safeEventExcerpt(summary.Excerpt), RevisionID: summary.RevisionID, Sequence: uint64(summary.Sequence), OccurredAt: summary.OccurredAt}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].OccurredAt != items[j].OccurredAt {
			return items[i].OccurredAt < items[j].OccurredAt
		}
		if items[i].Sequence != items[j].Sequence {
			return items[i].Sequence < items[j].Sequence
		}
		return items[i].RevisionID < items[j].RevisionID
	})

	total := uint64(len(items))
	cursorKey := cursorAuthenticationKey(view)
	offset, err := eventPageOffset(request, dependency.Digest, cursorKey, total)
	if err != nil {
		return SessionEventPage{}, err
	}
	end := offset + uint64(request.Limit)
	if end > total {
		end = total
	}
	page := SessionEventPage{
		SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: request.ProjectID,
		Provider: request.Provider, SessionID: request.SessionID, GenerationID: publishedID,
		SessionViewDigest: dependency.Digest, Total: total, RangeStart: offset, RangeEnd: end,
		Items: append([]EventItem(nil), items[offset:end]...), Coverage: Coverage{
			Seen: entry.Coverage.Seen, Indexed: entry.Coverage.Indexed, Collapsed: entry.Coverage.Collapsed,
			Unprojected: entry.Coverage.Unprojected, Undecodable: entry.Coverage.Undecodable, Truncated: entry.Coverage.Truncated,
		},
	}
	if total != 0 {
		page.FirstCursor = cursorPointer(request, dependency.Digest, cursorKey, 0)
		lastOffset := ((total - 1) / uint64(request.Limit)) * uint64(request.Limit)
		page.LastCursor = cursorPointer(request, dependency.Digest, cursorKey, lastOffset)
		if offset > 0 {
			previous := uint64(0)
			if offset > uint64(request.Limit) {
				previous = offset - uint64(request.Limit)
			}
			page.PreviousCursor = cursorPointer(request, dependency.Digest, cursorKey, previous)
		}
		if end < total {
			page.NextCursor = cursorPointer(request, dependency.Digest, cursorKey, end)
		}
	}
	if _, err := RenderEventPage(page); err != nil {
		return SessionEventPage{}, publicError(CodeInvalidArgument, "published Session event page is invalid")
	}
	return page, nil
}

func publicError(code, message string) error { return &Error{Code: code, Message: message} }

func sameSessionDependencies(left, right []memory.SessionViewDependency) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func selectedDependency(manifest memory.GenerationManifest, provider, sessionID string) (memory.SessionViewDependency, bool) {
	for _, dependency := range append(append([]memory.SessionViewDependency(nil), manifest.SessionViews...), manifest.RetainedSessionViews...) {
		if dependency.Provider == provider && dependency.SessionID == sessionID {
			return dependency, true
		}
	}
	return memory.SessionViewDependency{}, false
}

func selectedIndexEntry(index sessionindex.Document, provider, sessionID string) (sessionindex.Entry, bool) {
	for _, entry := range index.Sessions {
		if entry.Provider == provider && entry.SessionID == sessionID {
			return entry, true
		}
	}
	return sessionindex.Entry{}, false
}

func publicEventKind(kind string) (string, bool) {
	switch kind {
	case "request":
		return "message", true
	case "tool":
		return "tool_call", true
	case "command":
		return "command", true
	case "file":
		return "file_change", true
	case "test", "build", "lint", "verification":
		return "verification", true
	case "error":
		return "error", true
	case "artifact", "commit", "release", "deployment", "branch", "git_status", "sync", "tag", "version":
		return "artifact", true
	default:
		return "", false
	}
}

func safeEventExcerpt(value string) string {
	value = redact.Default().Text(value).Text
	value = redactAbsolutePaths(value)
	if len(value) <= eventExcerptBytes {
		return value
	}
	const suffix = "…"
	end := eventExcerptBytes - len(suffix)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + suffix
}

func redactAbsolutePaths(value string) string {
	const marker = "[REDACTED:ABSOLUTE_PATH]"
	var result strings.Builder
	wrote, copied := false, 0
	for start := 0; start < len(value); start++ {
		if !absolutePathStart(value, start) || !absolutePathBoundary(value, start) {
			continue
		}
		end := start
		quote := byte(0)
		if start > 0 && (value[start-1] == '\'' || value[start-1] == '"') {
			quote = value[start-1]
		}
		for end < len(value) {
			if quote != 0 {
				if value[end] == quote {
					break
				}
			} else if end > start && (value[end] == ' ' || value[end] == '\t' || value[end] == '\r' || value[end] == '\n' || strings.ContainsRune(",;)]}>", rune(value[end]))) {
				break
			}
			end++
		}
		if end == start {
			continue
		}
		result.WriteString(value[copied:start])
		result.WriteString(marker)
		copied, start, wrote = end, end-1, true
	}
	if !wrote {
		return value
	}
	result.WriteString(value[copied:])
	return result.String()
}

func absolutePathStart(value string, offset int) bool {
	if value[offset] == '/' {
		return offset+1 < len(value) && value[offset+1] != '/'
	}
	if value[offset] == '\\' {
		return offset+2 < len(value) && value[offset+1] == '\\'
	}
	return offset+3 < len(value) && ((value[offset] >= 'A' && value[offset] <= 'Z') || (value[offset] >= 'a' && value[offset] <= 'z')) && value[offset+1] == ':' && (value[offset+2] == '/' || value[offset+2] == '\\')
}

func absolutePathBoundary(value string, offset int) bool {
	if offset == 0 {
		return true
	}
	previous := value[offset-1]
	return previous == ' ' || previous == '\t' || previous == '\r' || previous == '\n' || strings.ContainsRune("\"'([{<=", rune(previous))
}

func eventPageOffset(request EventPageRequest, viewDigest string, cursorKey []byte, total uint64) (uint64, error) {
	if request.Cursor != "" {
		cursor, err := decodeEventCursor(request.Cursor, cursorKey)
		if err != nil || cursor.ProjectID != request.ProjectID || cursor.Provider != request.Provider || cursor.SessionID != request.SessionID || cursor.GenerationID != request.ExpectedGenerationID || cursor.ViewDigest != viewDigest || cursor.Limit != request.Limit || cursor.Offset >= total || cursor.Offset%uint64(request.Limit) != 0 {
			return 0, publicError(CodeStaleCursor, "cursor does not match the published Session page")
		}
		return cursor.Offset, nil
	}
	if request.Anchor != 0 {
		if request.Anchor < 1 || uint64(request.Anchor) > total {
			return 0, publicError(CodeAnchorOutOfRange, "anchor is outside the published Session event range")
		}
		return ((uint64(request.Anchor) - 1) / uint64(request.Limit)) * uint64(request.Limit), nil
	}
	return 0, nil
}

func cursorPointer(request EventPageRequest, viewDigest string, cursorKey []byte, offset uint64) *string {
	value := encodeEventCursor(eventCursor{Version: 1, ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, GenerationID: request.ExpectedGenerationID, ViewDigest: viewDigest, Limit: request.Limit, Offset: offset}, cursorKey)
	return &value
}

func cursorAuthenticationKey(view memory.SessionView) []byte {
	material := "session-reviewer/event-cursor/key/v1\x00" + view.SourceRecordDigest + "\x00" + view.DependencyDigest + "\x00" + strings.Join(view.ObservationChunkDigests, "\x00")
	sum := sha256.Sum256([]byte(material))
	return sum[:]
}

func encodeEventCursor(cursor eventCursor, binding []byte) string {
	cursor.Checksum = ""
	body, _ := json.Marshal(cursor)
	mac := hmac.New(sha256.New, binding)
	_, _ = mac.Write(body)
	cursor.Checksum = hex.EncodeToString(mac.Sum(nil))
	body, _ = json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(body)
}

func decodeEventCursor(value string, binding []byte) (eventCursor, error) {
	var cursor eventCursor
	body, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(body) > 4096 || base64.RawURLEncoding.EncodeToString(body) != value {
		return cursor, errors.New("invalid cursor")
	}
	if err := strictjson.Decode(body, &cursor); err != nil || cursor.Version != 1 || cursor.Checksum == "" {
		return eventCursor{}, errors.New("invalid cursor")
	}
	want := cursor.Checksum
	wantMAC, err := hex.DecodeString(want)
	if err != nil || len(wantMAC) != sha256.Size || want != strings.ToLower(want) {
		return eventCursor{}, errors.New("invalid cursor checksum")
	}
	cursor.Checksum = ""
	canonical, err := json.Marshal(cursor)
	if err != nil {
		return eventCursor{}, err
	}
	mac := hmac.New(sha256.New, binding)
	_, _ = mac.Write(canonical)
	if !hmac.Equal(wantMAC, mac.Sum(nil)) {
		return eventCursor{}, fmt.Errorf("invalid cursor checksum")
	}
	cursor.Checksum = want
	return cursor, nil
}
