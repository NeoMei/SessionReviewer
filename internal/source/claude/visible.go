package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/redact"
)

func (a *adapter) ReadVisiblePrefix(ctx context.Context, record memory.SourceRecord) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage, error) {
	var coverage conversationchain.VisibleCoverage
	if err := memory.ValidateSourceRecord(record); err != nil || record.Provider != providerClaude || record.Availability != memory.SourceAvailable {
		return nil, coverage, errors.Join(errors.New("invalid Claude visible source record"), err)
	}
	knownProject := false
	for _, projectID := range record.ProjectIDs {
		for _, binding := range a.bindings {
			if projectID == binding.ProjectID {
				knownProject = true
			}
		}
	}
	if !knownProject {
		return nil, coverage, errors.New("Claude visible source is not associated with an authenticated project")
	}
	location := record.FrozenBoundary.Location.JSONL
	if location == nil || location.Line < 1 || location.ByteOffset < 1 {
		return nil, coverage, errors.New("invalid Claude visible boundary")
	}
	frozen, err := a.frozenFor(record.Provider, record.SessionID, record.SourceIdentity, func(value frozenSource) bool {
		return reflect.DeepEqual(value.boundary.Frozen, record.FrozenBoundary)
	})
	if err != nil {
		return nil, coverage, err
	}
	file, _, err := a.openStored(frozen.stored)
	if err != nil {
		return nil, coverage, err
	}
	defer file.Close()
	if err := verifyFrozenPrefix(ctx, file, frozen); err != nil {
		return nil, coverage, err
	}
	return readVisiblePrefix(ctx, file, record, a.redactor)
}

// ReadPublishedVisible reopens a published Claude prefix from its supported
// local projects root. The retained-chain path remains available if the local
// source is later removed.
func ReadPublishedVisible(ctx context.Context, sessionsRoot string, record memory.SourceRecord) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage, error) {
	var coverage conversationchain.VisibleCoverage
	if ctx == nil || memory.ValidateSourceRecord(record) != nil || record.Provider != providerClaude || record.FrozenBoundary.Location.JSONL == nil || !uuidPattern.MatchString(record.SessionID) {
		return nil, coverage, errors.New("invalid Claude visible source boundary")
	}
	location := record.FrozenBoundary.Location.JSONL
	if location.Line < 1 || location.ByteOffset < 1 || location.ByteOffset > 1<<30 {
		return nil, coverage, errors.New("Claude visible source boundary exceeds budget")
	}
	if len(record.ProjectIDs) != 1 {
		return nil, coverage, errors.New("Claude visible source has ambiguous project association")
	}
	// Claude's native directory key is derived from the source cwd. SourceRecord
	// intentionally carries no host path, so search only direct project children
	// for the exact UUID and accept exactly one authenticated boundary hash.
	root, err := pathguard.Open(sessionsRoot)
	if err != nil {
		return nil, coverage, err
	}
	defer root.Close()
	rootFile, err := root.Root.Open(".")
	if err != nil {
		return nil, coverage, err
	}
	entries, readErr := rootFile.ReadDir(-1)
	closeErr := rootFile.Close()
	if readErr != nil || closeErr != nil {
		return nil, coverage, errors.Join(readErr, closeErr)
	}
	if len(entries) > maxDiscoveryFiles {
		return nil, coverage, errors.New("Claude visible source directory budget exceeded")
	}
	var match *os.File
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		file, info, openErr := root.OpenRegular(filepath.Join(entry.Name(), record.SessionID+".jsonl"))
		if openErr != nil {
			continue
		}
		if info.Size() < location.ByteOffset {
			_ = file.Close()
			continue
		}
		hash, lines, hashErr := hashPrefix(ctx, file, location.ByteOffset)
		if hashErr != nil || lines != location.Line || hash != record.FrozenBoundary.SourceHash {
			_ = file.Close()
			continue
		}
		if match != nil {
			_ = match.Close()
			_ = file.Close()
			return nil, coverage, errors.New("Claude visible source boundary is ambiguous")
		}
		match = file
	}
	if match == nil {
		return nil, coverage, errors.New("Claude visible source files are unavailable")
	}
	defer match.Close()
	return readVisiblePrefix(ctx, match, record, redact.Default())
}

// SessionsRoot follows Claude Code's supported local projects store and permits
// an explicit test/runtime override without exposing transcript paths publicly.
func SessionsRoot() (string, error) {
	if configured := os.Getenv("SESSION_REVIEWER_CLAUDE_SESSIONS_ROOT"); configured != "" {
		if !filepath.IsAbs(configured) || filepath.Clean(configured) != configured {
			return "", errors.New("Claude sessions root override must be an absolute clean path")
		}
		return configured, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

func readVisiblePrefix(ctx context.Context, file *os.File, record memory.SourceRecord, redactor redact.Redactor) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage, error) {
	var coverage conversationchain.VisibleCoverage
	location := record.FrozenBoundary.Location.JSONL
	hash := sha256.New()
	reader := bufio.NewReaderSize(io.TeeReader(io.NewSectionReader(file, 0, location.ByteOffset), hash), 64<<10)
	messages := []conversationchain.SourceMessage{}
	for lineNumber := 1; ; lineNumber++ {
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
			if readErr == io.EOF {
				break
			}
			continue
		}
		trimmed := bytes.TrimSpace(raw)
		if !utf8.Valid(trimmed) || !json.Valid(trimmed) {
			coverage.MalformedRecords++
			if readErr == io.EOF {
				break
			}
			continue
		}
		var envelope transcriptLine
		if json.Unmarshal(trimmed, &envelope) != nil {
			coverage.MalformedRecords++
			if readErr == io.EOF {
				break
			}
			continue
		}
		if envelope.Type != "user" && envelope.Type != "assistant" {
			if readErr == io.EOF {
				break
			}
			continue
		}
		if envelope.SessionID != record.SessionID || envelope.CWD == "" || envelope.Timestamp == "" {
			coverage.MalformedRecords++
			if readErr == io.EOF {
				break
			}
			continue
		}
		instant, parseErr := time.Parse(time.RFC3339Nano, envelope.Timestamp)
		if parseErr != nil {
			coverage.MalformedRecords++
			if readErr == io.EOF {
				break
			}
			continue
		}
		var message claudeMessage
		if json.Unmarshal(envelope.Message, &message) != nil || message.Role != envelope.Type {
			coverage.MalformedRecords++
			if readErr == io.EOF {
				break
			}
			continue
		}
		visible, unsupported, visibleErr := visibleMessage(decodedLine{envelope: envelope, message: message, line: lineNumber, hash: sha256Bytes(trimmed), time: instant})
		if visibleErr != nil {
			coverage.MalformedRecords++
		} else if unsupported {
			coverage.ContextMessages++
		}
		if visible.RecordOrdinal != 0 {
			visible.Text = redactor.Text(visible.Text).Text
			messages = append(messages, visible)
		}
		if readErr == io.EOF {
			break
		}
	}
	if coverage.SourceRecords != uint64(location.Line) {
		return nil, coverage, errors.New("Claude visible boundary line count mismatch")
	}
	if hex.EncodeToString(hash.Sum(nil)) != record.FrozenBoundary.SourceHash {
		return nil, coverage, errors.New("Claude visible source prefix hash mismatch")
	}
	_, segmented := conversationchain.MaterializeVisible(record.Provider, record.SessionID, record.SourceIdentity, messages)
	segmented.SourceRecords, segmented.OversizedRecords, segmented.MalformedRecords = coverage.SourceRecords, coverage.OversizedRecords, coverage.MalformedRecords
	segmented.ContextMessages += coverage.ContextMessages
	segmented.Complete = segmented.Complete && segmented.OversizedRecords == 0 && segmented.MalformedRecords == 0 && segmented.OrphanMessages == 0 && segmented.TruncatedBodies == 0
	return messages, segmented, nil
}

func readVisibleLine(reader *bufio.Reader) ([]byte, bool, error) {
	var line []byte
	oversized := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if !oversized {
			if len(line)+len(fragment) > maxVisibleRecordBytes {
				oversized, line = true, nil
			} else {
				line = append(line, fragment...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, oversized, err
	}
}

func sha256Bytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
