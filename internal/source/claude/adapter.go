// Package claude implements deterministic, zero-token access to Claude Code
// JSONL transcripts stored below ~/.claude/projects.
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
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

const (
	providerClaude        = "claude"
	adapterID             = "claude-jsonl"
	maxRecordBytes        = 64 << 20
	maxVisibleRecordBytes = 64 << 10
	maxDiscoveryFiles     = 65536
)

var (
	identityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	uuidPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	shaPattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type AdapterOptions struct {
	SessionsRoot              string
	Bindings                  []projectidentity.Binding
	Catalog                   *sourcecatalog.Catalog
	Redactor                  *redact.Redactor
	AdapterVersion            string
	SupersedesAdapterVersions []string
}

type adapter struct {
	sessionsRoot string
	bindings     []projectidentity.Binding
	catalog      *sourcecatalog.Catalog
	redactor     redact.Redactor
	version      string
	supersedes   []string

	mu             sync.RWMutex
	leaseSequence  uint64
	candidates     map[string]storedCandidate
	candidateLease map[string]string
	frozen         map[string]frozenSource
	boundaryLease  map[string]string
	frozenBySource map[string][]string
}

type storedCandidate struct {
	public        source.Candidate
	binding       projectidentity.Binding
	childName     string
	fileName      string
	rootIdentity  pathguard.IdentityToken
	childIdentity pathguard.IdentityToken
	fileIdentity  pathguard.IdentityToken
	baseline      source.CatalogBaselineSnapshot
}

type frozenSource struct {
	boundary    source.Boundary
	stored      storedCandidate
	prefixSize  int64
	lineCount   int
	prior       memory.SourceRecord
	priorFound  bool
	priorDigest string
}

func New(options AdapterOptions) (source.Adapter, error) {
	if !filepath.IsAbs(options.SessionsRoot) || filepath.Clean(options.SessionsRoot) != options.SessionsRoot {
		return nil, errors.New("Claude sessions root must be an absolute clean path")
	}
	if len(options.Bindings) == 0 {
		return nil, errors.New("at least one authenticated project binding is required")
	}
	seen := make(map[string]struct{}, len(options.Bindings))
	for _, binding := range options.Bindings {
		if !identityPattern.MatchString(binding.ProjectID) || !filepath.IsAbs(binding.CanonicalRoot) || filepath.Clean(binding.CanonicalRoot) != binding.CanonicalRoot || !binding.RootIdentity.Valid() {
			return nil, errors.New("invalid authenticated project binding")
		}
		if _, duplicate := seen[binding.ProjectID]; duplicate {
			return nil, fmt.Errorf("duplicate authenticated project binding %q", binding.ProjectID)
		}
		seen[binding.ProjectID] = struct{}{}
		if err := authenticateBindingRoot(binding); err != nil {
			return nil, err
		}
	}
	if options.Catalog == nil {
		return nil, errors.New("source catalog is required")
	}
	if options.Redactor == nil {
		return nil, errors.New("redactor is required")
	}
	if !identityPattern.MatchString(options.AdapterVersion) {
		return nil, errors.New("invalid Claude adapter version")
	}
	supersedes := append([]string(nil), options.SupersedesAdapterVersions...)
	sort.Strings(supersedes)
	for index, version := range supersedes {
		if !identityPattern.MatchString(version) || version == options.AdapterVersion || index > 0 && version == supersedes[index-1] {
			return nil, errors.New("invalid superseded Claude adapter version")
		}
	}
	return &adapter{
		sessionsRoot: options.SessionsRoot, bindings: append([]projectidentity.Binding(nil), options.Bindings...),
		catalog: options.Catalog, redactor: *options.Redactor, version: options.AdapterVersion, supersedes: supersedes,
		candidates: make(map[string]storedCandidate), candidateLease: make(map[string]string),
		frozen: make(map[string]frozenSource), boundaryLease: make(map[string]string), frozenBySource: make(map[string][]string),
	}, nil
}

func authenticateBindingRoot(binding projectidentity.Binding) error {
	directory, err := pathguard.Open(binding.CanonicalRoot)
	if err != nil {
		return fmt.Errorf("authenticate project binding %q: %w", binding.ProjectID, err)
	}
	identity, identityErr := directory.PhysicalIdentity()
	closeErr := directory.Close()
	if identityErr != nil || closeErr != nil || identity != binding.RootIdentity {
		return errors.Join(fmt.Errorf("project binding %q identity changed", binding.ProjectID), identityErr, closeErr)
	}
	return nil
}

func (a *adapter) Discover(ctx context.Context) (source.Discovery, error) {
	if err := ctx.Err(); err != nil {
		return source.Discovery{}, err
	}
	root, err := pathguard.Open(a.sessionsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return source.Discovery{}, source.ErrProviderUnavailable
	}
	if err != nil {
		return source.Discovery{}, fmt.Errorf("open Claude sessions root: %w", err)
	}
	defer root.Close()
	rootIdentity, err := root.PhysicalIdentity()
	if err != nil {
		return source.Discovery{}, err
	}

	type foundCandidate struct {
		stored storedCandidate
	}
	found := make(map[string][]foundCandidate)
	issues := []source.Issue{}
	filesSeen := 0
	for _, binding := range a.bindings {
		if err := ctx.Err(); err != nil {
			return source.Discovery{}, err
		}
		if err := authenticateBindingRoot(binding); err != nil {
			return source.Discovery{}, err
		}
		childName, err := encodedProjectKey(binding.CanonicalRoot)
		if err != nil {
			return source.Discovery{}, err
		}
		child, childInfo, err := root.OpenDirectory(childName)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return source.Discovery{}, fmt.Errorf("open Claude project directory: %w", err)
		}
		childHandle, err := child.Open(".")
		if err != nil {
			_ = child.Close()
			return source.Discovery{}, err
		}
		childIdentity, identityErr := pathguard.PhysicalFileIdentity(childHandle)
		closeChildHandleErr := childHandle.Close()
		if identityErr != nil || closeChildHandleErr != nil {
			_ = child.Close()
			return source.Discovery{}, errors.Join(identityErr, closeChildHandleErr)
		}
		entriesFile, err := child.Open(".")
		if err != nil {
			_ = child.Close()
			return source.Discovery{}, err
		}
		entries, readErr := entriesFile.ReadDir(-1)
		closeErr := entriesFile.Close()
		if readErr != nil || closeErr != nil {
			_ = child.Close()
			return source.Discovery{}, errors.Join(readErr, closeErr)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				_ = child.Close()
				return source.Discovery{}, err
			}
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
				continue
			}
			filesSeen++
			if filesSeen > maxDiscoveryFiles {
				_ = child.Close()
				return source.Discovery{}, errors.New("Claude discovery file budget exceeded")
			}
			sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
			if !uuidPattern.MatchString(sessionID) {
				continue
			}
			file, info, err := root.OpenRegular(filepath.Join(childName, entry.Name()))
			if err != nil {
				issues = append(issues, source.Issue{Code: "unreadable_segment", Provider: providerClaude, SessionID: sessionID, Path: entry.Name(), TerminalState: memory.Unreadable})
				continue
			}
			fileIdentity, identityErr := pathguard.PhysicalFileIdentity(file)
			prefixSize, prefixErr := finalizedPrefixSize(file, info.Size())
			metadata, metadataErr := inspectClaudePrefix(ctx, file, prefixSize, sessionID, binding)
			closeErr := file.Close()
			if identityErr != nil || prefixErr != nil || metadataErr != nil || closeErr != nil {
				state, code := memory.Unreadable, "unreadable_segment"
				if errors.Is(prefixErr, errNoFinalizedRecords) {
					state, code = memory.Unsupported, "no_finalized_records"
				}
				issues = append(issues, source.Issue{Code: code, Provider: providerClaude, SessionID: sessionID, Path: entry.Name(), TerminalState: state})
				continue
			}
			public := source.Candidate{Provider: providerClaude, SessionID: sessionID, StartedAt: metadata.startedAt, InitialCWD: metadata.cwd}
			stored := storedCandidate{public: public, binding: binding, childName: childName, fileName: entry.Name(), rootIdentity: rootIdentity, childIdentity: childIdentity, fileIdentity: fileIdentity}
			found[sessionID] = append(found[sessionID], foundCandidate{stored: stored})
		}
		if err := verifyChild(root, childName, child, childInfo, childIdentity); err != nil {
			_ = child.Close()
			return source.Discovery{}, err
		}
		if err := child.Close(); err != nil {
			return source.Discovery{}, err
		}
	}

	ids := make([]string, 0, len(found))
	for sessionID := range found {
		ids = append(ids, sessionID)
	}
	sort.Strings(ids)
	keys := make([]sourcecatalog.SnapshotKey, 0, len(ids))
	for _, sessionID := range ids {
		keys = append(keys, sourcecatalog.SnapshotKey{Provider: providerClaude, SessionID: sessionID})
	}
	baselines, err := a.catalog.SnapshotSources(keys)
	if err != nil {
		return source.Discovery{}, err
	}
	result := source.Discovery{Issues: issues}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, sessionID := range ids {
		matches := found[sessionID]
		if len(matches) != 1 {
			result.Issues = append(result.Issues, source.Issue{Code: "duplicate_candidate", Provider: providerClaude, SessionID: sessionID, TerminalState: memory.Ambiguous})
			continue
		}
		stored := matches[0].stored
		snapshot := baselines[sourcecatalog.SnapshotKey{Provider: providerClaude, SessionID: sessionID}]
		baseline := source.CatalogBaselineSnapshot{ExpectedDigest: snapshot.Digest}
		if snapshot.Found {
			prior := cloneSourceRecord(snapshot.Record)
			baseline.PriorSource = &prior
		}
		handle := opaqueHandle("candidate", sessionID, stored.childName, stored.fileName, stored.public.StartedAt, snapshot.Digest)
		baseline.Handle = opaqueHandle("catalog-baseline", handle, snapshot.Digest)
		stored.public.Handle, stored.public.CatalogBaseline = handle, cloneCatalogBaseline(baseline)
		stored.baseline = cloneCatalogBaseline(baseline)
		lease := a.newLeaseLocked("candidate", handle)
		stored.public.Lease = lease
		a.candidates[handle] = stored
		a.candidateLease[lease] = handle
		result.Candidates = append(result.Candidates, stored.public)
	}
	sort.Slice(result.Issues, func(i, j int) bool {
		if result.Issues[i].SessionID != result.Issues[j].SessionID {
			return result.Issues[i].SessionID < result.Issues[j].SessionID
		}
		return result.Issues[i].Path < result.Issues[j].Path
	})
	return result, nil
}

func verifyChild(root *pathguard.Directory, name string, child *os.Root, childInfo os.FileInfo, identity pathguard.IdentityToken) error {
	current, err := root.Root.Lstat(name)
	if err != nil || !current.IsDir() || !os.SameFile(current, childInfo) {
		return errors.New("Claude project directory changed")
	}
	file, err := child.Open(".")
	if err != nil {
		return err
	}
	actual, identityErr := pathguard.PhysicalFileIdentity(file)
	closeErr := file.Close()
	if identityErr != nil || closeErr != nil || actual != identity {
		return errors.Join(errors.New("Claude project directory identity changed"), identityErr, closeErr)
	}
	return nil
}

func encodedProjectKey(projectRoot string) (string, error) {
	if !filepath.IsAbs(projectRoot) || filepath.Clean(projectRoot) != projectRoot {
		return "", errors.New("Claude project root must be an absolute clean path")
	}
	return strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(projectRoot), nil
}

type discoveryMetadata struct{ cwd, startedAt string }

func inspectClaudePrefix(ctx context.Context, file *os.File, prefixSize int64, sessionID string, binding projectidentity.Binding) (discoveryMetadata, error) {
	if prefixSize <= 0 {
		return discoveryMetadata{}, errNoFinalizedRecords
	}
	reader := bufio.NewReaderSize(io.NewSectionReader(file, 0, prefixSize), 64<<10)
	var result discoveryMetadata
	for lineNumber := 1; ; lineNumber++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		raw, err := readBoundedLine(reader, maxRecordBytes)
		if len(raw) == 0 && err == io.EOF {
			break
		}
		if err != nil && err != io.EOF {
			return result, err
		}
		var line transcriptLine
		if json.Unmarshal(bytes.TrimSpace(raw), &line) != nil {
			return result, fmt.Errorf("invalid Claude JSON at line %d", lineNumber)
		}
		conversation := line.Type == "user" || line.Type == "assistant"
		if line.SessionID != "" && line.SessionID != sessionID {
			return result, fmt.Errorf("Claude session identity mismatch at line %d", lineNumber)
		}
		if conversation {
			if line.SessionID == "" || line.CWD == "" || line.Timestamp == "" {
				return result, fmt.Errorf("Claude conversation identity is incomplete at line %d", lineNumber)
			}
			if err := authenticateCWD(line.CWD, binding); err != nil {
				return result, err
			}
			if result.cwd == "" {
				result.cwd = line.CWD
			}
		}
		if line.Timestamp != "" {
			instant, parseErr := time.Parse(time.RFC3339Nano, line.Timestamp)
			if parseErr != nil {
				return result, fmt.Errorf("invalid Claude timestamp at line %d", lineNumber)
			}
			if result.startedAt == "" {
				result.startedAt = instant.Format(time.RFC3339Nano)
			}
		}
		if err == io.EOF {
			break
		}
	}
	if result.cwd == "" || result.startedAt == "" {
		return result, errors.New("Claude transcript has no authenticated conversation metadata")
	}
	return result, nil
}

func authenticateCWD(value string, binding projectidentity.Binding) error {
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return errors.New("Claude cwd is not an absolute clean path")
	}
	directory, err := pathguard.Open(value)
	if err != nil {
		return err
	}
	identity, identityErr := directory.PhysicalIdentity()
	closeErr := directory.Close()
	if identityErr != nil || closeErr != nil || identity != binding.RootIdentity {
		return errors.Join(errors.New("Claude cwd does not match authenticated project"), identityErr, closeErr)
	}
	return nil
}

var errNoFinalizedRecords = errors.New("Claude transcript has no finalized records")

func finalizedPrefixSize(file *os.File, size int64) (int64, error) {
	if file == nil || size <= 0 {
		return 0, errNoFinalizedRecords
	}
	const chunkSize int64 = 64 << 10
	buffer := make([]byte, chunkSize)
	for end := size; end > 0; {
		start := end - chunkSize
		if start < 0 {
			start = 0
		}
		count, err := file.ReadAt(buffer[:end-start], start)
		if err != nil && err != io.EOF {
			return 0, err
		}
		if at := bytes.LastIndexByte(buffer[:count], '\n'); at >= 0 {
			return start + int64(at) + 1, nil
		}
		end = start
	}
	return 0, errNoFinalizedRecords
}

func (a *adapter) Freeze(ctx context.Context, candidate source.Candidate) (source.Boundary, error) {
	a.mu.RLock()
	stored, found := a.candidates[candidate.Handle]
	handle, leased := a.candidateLease[candidate.Lease]
	a.mu.RUnlock()
	if !found || !leased || handle != candidate.Handle || candidate.Provider != providerClaude || !sameCandidate(candidate, stored.public) {
		return source.Boundary{}, errors.New("candidate was not returned by this Claude adapter")
	}
	defer a.AbandonCandidate(candidate)
	if err := ctx.Err(); err != nil {
		return source.Boundary{}, err
	}
	file, info, err := a.openStored(stored)
	if err != nil {
		state, code := memory.Unreadable, "unreadable_segment"
		if errors.Is(err, os.ErrNotExist) {
			state, code = memory.Missing, "missing_segment"
		}
		return source.Boundary{Candidate: candidate, TerminalState: state, Issues: []source.Issue{{Code: code, Provider: providerClaude, SessionID: candidate.SessionID, Path: stored.fileName, TerminalState: state}}}, nil
	}
	defer file.Close()
	prefixSize, err := finalizedPrefixSize(file, info.Size())
	if err != nil {
		return source.Boundary{}, err
	}
	hash, lines, err := hashPrefix(ctx, file, prefixSize)
	if err != nil {
		return source.Boundary{}, err
	}
	sourceIdentity := opaqueHandle("source", candidate.SessionID, stored.fileIdentity.Kind, stored.fileIdentity.Volume, stored.fileIdentity.File)
	priorFound := stored.baseline.PriorSource != nil
	var prior memory.SourceRecord
	if priorFound {
		prior = cloneSourceRecord(*stored.baseline.PriorSource)
		if canReuseSourceIdentity(ctx, file, prefixSize, lines, hash, prior) {
			sourceIdentity = prior.SourceIdentity
		}
	}
	boundaryHandle := opaqueHandle("boundary", candidate.Handle, sourceIdentity, hash, strconv.FormatInt(prefixSize, 10))
	boundary := source.Boundary{
		Candidate: candidate, SourceIdentity: sourceIdentity,
		Frozen:   memory.FrozenBoundary{Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: lines, ByteOffset: prefixSize}}, SourceHash: hash},
		Segments: []source.SegmentBoundary{{Ordinal: 1, Size: prefixSize, SourceHash: hash}}, TerminalState: memory.Indexed, Handle: boundaryHandle,
	}
	frozen := frozenSource{boundary: boundary, stored: stored, prefixSize: prefixSize, lineCount: lines, prior: prior, priorFound: priorFound, priorDigest: stored.baseline.ExpectedDigest}
	a.mu.Lock()
	if existing, ok := a.frozen[boundaryHandle]; ok && !sameBoundary(existing.boundary, boundary) {
		a.mu.Unlock()
		return source.Boundary{}, errors.New("Claude frozen boundary handle collision")
	}
	a.frozen[boundaryHandle] = frozen
	key := frozenKey(providerClaude, candidate.SessionID, sourceIdentity)
	a.frozenBySource[key] = appendUnique(a.frozenBySource[key], boundaryHandle)
	boundary.Lease = a.newLeaseLocked("boundary", boundaryHandle)
	a.boundaryLease[boundary.Lease] = boundaryHandle
	a.mu.Unlock()
	return cloneBoundary(boundary), nil
}

func canReuseSourceIdentity(ctx context.Context, file *os.File, size int64, lines int, hash string, prior memory.SourceRecord) bool {
	location := prior.FrozenBoundary.Location.JSONL
	if prior.Provider != providerClaude || prior.SourceIdentity == "" || location == nil || size < location.ByteOffset || lines < location.Line {
		return false
	}
	if size == location.ByteOffset && lines == location.Line {
		return hash == prior.FrozenBoundary.SourceHash
	}
	prefixHash, _, err := hashPrefix(ctx, file, location.ByteOffset)
	return err == nil && prefixHash == prior.FrozenBoundary.SourceHash
}

func (a *adapter) openStored(stored storedCandidate) (*os.File, os.FileInfo, error) {
	root, err := pathguard.Open(a.sessionsRoot)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	identity, err := root.PhysicalIdentity()
	if err != nil || identity != stored.rootIdentity {
		return nil, nil, errors.New("Claude sessions root identity changed")
	}
	child, childInfo, err := root.OpenDirectory(stored.childName)
	if err != nil {
		return nil, nil, err
	}
	childFile, err := child.Open(".")
	if err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	childIdentity, identityErr := pathguard.PhysicalFileIdentity(childFile)
	_ = childFile.Close()
	if identityErr != nil || childIdentity != stored.childIdentity {
		_ = child.Close()
		return nil, nil, errors.New("Claude project directory identity changed")
	}
	file, info, err := root.OpenRegular(filepath.Join(stored.childName, stored.fileName))
	if err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	fileIdentity, identityErr := pathguard.PhysicalFileIdentity(file)
	if identityErr != nil || fileIdentity != stored.fileIdentity {
		_ = file.Close()
		_ = child.Close()
		return nil, nil, errors.New("Claude session file identity changed")
	}
	if err := verifyChild(root, stored.childName, child, childInfo, stored.childIdentity); err != nil {
		_ = file.Close()
		_ = child.Close()
		return nil, nil, err
	}
	if err := child.Close(); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func hashPrefix(ctx context.Context, file *os.File, size int64) (string, int, error) {
	if size < 1 {
		return "", 0, errNoFinalizedRecords
	}
	hash := sha256.New()
	reader := io.NewSectionReader(file, 0, size)
	buffer := make([]byte, 64<<10)
	lines := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		n, err := reader.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
			lines += bytes.Count(buffer[:n], []byte{'\n'})
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), lines, nil
}

func (a *adapter) AbandonCandidate(candidate source.Candidate) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if handle, ok := a.candidateLease[candidate.Lease]; ok && handle == candidate.Handle {
		delete(a.candidateLease, candidate.Lease)
	}
}

func (a *adapter) AbandonBoundary(boundary source.Boundary) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if handle, ok := a.boundaryLease[boundary.Lease]; ok && handle == boundary.Handle {
		delete(a.boundaryLease, boundary.Lease)
	}
}

func (a *adapter) newLeaseLocked(kind, handle string) string {
	a.leaseSequence++
	return opaqueHandle(kind+"-lease", handle, strconv.FormatUint(a.leaseSequence, 10))
}

func opaqueHandle(kind string, parts ...string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, kind)
	for _, part := range parts {
		_, _ = io.WriteString(hash, "\x00")
		_, _ = io.WriteString(hash, part)
	}
	return kind + "-" + hex.EncodeToString(hash.Sum(nil))
}

func frozenKey(provider, sessionID, identity string) string {
	return provider + "\x00" + sessionID + "\x00" + identity
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func cloneCatalogBaseline(value source.CatalogBaselineSnapshot) source.CatalogBaselineSnapshot {
	if value.PriorSource != nil {
		prior := cloneSourceRecord(*value.PriorSource)
		value.PriorSource = &prior
	}
	return value
}

func cloneSourceRecord(value memory.SourceRecord) memory.SourceRecord {
	value.ProjectIDs = append([]string(nil), value.ProjectIDs...)
	value.Usage.Models = append([]accounting.ModelUsage(nil), value.Usage.Models...)
	return value
}

func cloneBoundary(value source.Boundary) source.Boundary {
	value.Candidate.CatalogBaseline = cloneCatalogBaseline(value.Candidate.CatalogBaseline)
	value.Segments = append([]source.SegmentBoundary(nil), value.Segments...)
	value.Issues = append([]source.Issue(nil), value.Issues...)
	return value
}

func sameCandidate(left, right source.Candidate) bool {
	return left.Provider == right.Provider && left.SessionID == right.SessionID && left.StartedAt == right.StartedAt && left.InitialCWD == right.InitialCWD && left.Handle == right.Handle && reflect.DeepEqual(left.CatalogBaseline, right.CatalogBaseline)
}

func sameBoundary(left, right source.Boundary) bool {
	return left.Candidate.Provider == right.Candidate.Provider && left.Candidate.SessionID == right.Candidate.SessionID && left.SourceIdentity == right.SourceIdentity && left.Handle == right.Handle && reflect.DeepEqual(left.Frozen, right.Frozen) && reflect.DeepEqual(left.Segments, right.Segments)
}

type transcriptLine struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	CWD       string          `json:"cwd"`
	UUID      string          `json:"uuid"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   *claudeUsage    `json:"usage"`
}

type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

type claudePart struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Data      string          `json:"data"`
	FileName  string          `json:"file_name"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

func readBoundedLine(reader *bufio.Reader, maximum int) ([]byte, error) {
	line := make([]byte, 0, 64<<10)
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > maximum-len(line) {
			return nil, errors.New("Claude record exceeds supported size")
		}
		line = append(line, fragment...)
		if err == nil || err == io.EOF {
			return line, err
		}
		if err != bufio.ErrBufferFull {
			return nil, err
		}
	}
}

func addSafe(left, right int64) (int64, error) {
	if left < 0 || right < 0 || right > math.MaxInt64-left {
		return 0, errors.New("Claude token count is invalid")
	}
	return left + right, nil
}

func boundedText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	end := maximum - len("…")
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "…"
}

var _ source.Adapter = (*adapter)(nil)
var _ source.VisibleReader = (*adapter)(nil)
var _ source.LeaseLifecycle = (*adapter)(nil)
