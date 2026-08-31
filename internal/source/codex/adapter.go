// Package codex implements deterministic access to Codex JSONL session
// sources. Observation decoding is added separately from source freezing.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/session"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

const (
	providerCodex         = "codex"
	metadataOnlySelection = "\x00session-reviewer-metadata-only"
	maxReadRecordBytes    = 64 << 20
)

var (
	adapterIdentityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	lowercaseSHA256        = regexp.MustCompile(`^[0-9a-f]{64}$`)
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
	discovery      uint64
	candidates     map[string]session.Candidate
	frozen         map[string]frozenSource
	frozenBySource map[string][]string
}

type frozenSource struct {
	boundary  source.Boundary
	candidate session.Candidate
	segments  []frozenSegment
}

type frozenSegment struct {
	size         int64
	hash         string
	identity     pathguard.IdentityToken
	needsNewline bool
}

func New(options AdapterOptions) (source.Adapter, error) {
	if !filepath.IsAbs(options.SessionsRoot) || filepath.Clean(options.SessionsRoot) != options.SessionsRoot {
		return nil, errors.New("Codex sessions root must be an absolute clean path")
	}
	sessionsRoot, err := pathguard.Open(options.SessionsRoot)
	if err != nil {
		return nil, fmt.Errorf("authenticate Codex sessions root: %w", err)
	}
	if err := sessionsRoot.Close(); err != nil {
		return nil, fmt.Errorf("close authenticated Codex sessions root: %w", err)
	}
	if len(options.Bindings) == 0 {
		return nil, errors.New("at least one authenticated project binding is required")
	}
	seenProjects := make(map[string]struct{}, len(options.Bindings))
	for _, binding := range options.Bindings {
		if !adapterIdentityPattern.MatchString(binding.ProjectID) || !binding.RootIdentity.Valid() ||
			!filepath.IsAbs(binding.CanonicalRoot) || filepath.Clean(binding.CanonicalRoot) != binding.CanonicalRoot {
			return nil, errors.New("invalid authenticated project binding")
		}
		if _, duplicate := seenProjects[binding.ProjectID]; duplicate {
			return nil, fmt.Errorf("duplicate authenticated project binding %q", binding.ProjectID)
		}
		seenProjects[binding.ProjectID] = struct{}{}
		root, err := pathguard.Open(binding.CanonicalRoot)
		if err != nil {
			return nil, fmt.Errorf("authenticate project binding %q: %w", binding.ProjectID, err)
		}
		identity, identityErr := root.PhysicalIdentity()
		closeErr := root.Close()
		if identityErr != nil || closeErr != nil || identity != binding.RootIdentity {
			return nil, errors.Join(fmt.Errorf("project binding %q identity changed", binding.ProjectID), identityErr, closeErr)
		}
	}
	if options.Catalog == nil {
		return nil, errors.New("source catalog is required")
	}
	if options.Redactor == nil {
		return nil, errors.New("redactor is required")
	}
	if !adapterIdentityPattern.MatchString(options.AdapterVersion) {
		return nil, errors.New("invalid Codex adapter version")
	}
	supersedes := append([]string(nil), options.SupersedesAdapterVersions...)
	sort.Strings(supersedes)
	for index, version := range supersedes {
		if !adapterIdentityPattern.MatchString(version) || version == options.AdapterVersion || (index > 0 && supersedes[index-1] == version) {
			return nil, errors.New("invalid superseded Codex adapter version")
		}
	}
	return &adapter{
		sessionsRoot:   options.SessionsRoot,
		bindings:       append([]projectidentity.Binding(nil), options.Bindings...),
		catalog:        options.Catalog,
		redactor:       *options.Redactor,
		version:        options.AdapterVersion,
		supersedes:     supersedes,
		candidates:     make(map[string]session.Candidate),
		frozen:         make(map[string]frozenSource),
		frozenBySource: make(map[string][]string),
	}, nil
}

func (a *adapter) Discover(ctx context.Context) (source.Discovery, error) {
	if err := ctx.Err(); err != nil {
		return source.Discovery{}, err
	}
	// A deliberately impossible selected ID makes the existing discovery stop
	// after metadata in each candidate. Malformed later records remain decoder
	// diagnostics instead of hiding an otherwise identifiable Session.
	raw, err := session.Discover(a.sessionsRoot, metadataOnlySelection)
	if err != nil {
		return source.Discovery{}, err
	}
	if err := ctx.Err(); err != nil {
		return source.Discovery{}, err
	}

	grouped := make(map[string]struct{}, len(raw.Candidates))
	for _, candidate := range raw.Candidates {
		grouped[candidate.ID] = struct{}{}
	}
	ids := make([]string, 0, len(grouped))
	for id := range grouped {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.discovery++
	result := source.Discovery{}
	for _, issue := range raw.Issues {
		state := memory.Unreadable
		code := "unreadable_segment"
		if errors.Is(issue.Err, os.ErrNotExist) {
			state = memory.Missing
			code = "missing_segment"
		}
		result.Issues = append(result.Issues, source.Issue{
			Code: code, Provider: providerCodex, SessionID: issue.SessionID,
			Path: issue.Path, TerminalState: state,
		})
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return source.Discovery{}, err
		}
		resolved, resolveErr := session.Resolve(raw.Candidates, session.ResolveOptions{SessionID: id, GOOS: runtime.GOOS})
		if resolveErr != nil {
			state, code := memory.Unreadable, "unreadable_segment"
			if errors.Is(resolveErr, session.ErrSessionConflict) {
				state, code = memory.Ambiguous, "duplicate_segment"
			} else if errors.Is(resolveErr, os.ErrNotExist) {
				state, code = memory.Missing, "missing_segment"
			}
			result.Issues = append(result.Issues, source.Issue{
				Code: code, Provider: providerCodex, SessionID: id, TerminalState: state,
			})
			continue
		}
		startedAt := candidateStartedAt(resolved)
		handle := opaqueHandle("candidate", strconv.FormatUint(a.discovery, 10), id, resolved.Path, startedAt)
		candidate := source.Candidate{
			Provider: providerCodex, SessionID: id, StartedAt: startedAt,
			InitialCWD: resolved.CWD, Handle: handle,
		}
		a.candidates[handle] = resolved
		result.Candidates = append(result.Candidates, candidate)
	}
	sort.Slice(result.Issues, func(i, j int) bool {
		if result.Issues[i].SessionID != result.Issues[j].SessionID {
			return result.Issues[i].SessionID < result.Issues[j].SessionID
		}
		return result.Issues[i].Path < result.Issues[j].Path
	})
	return result, nil
}

func (a *adapter) Freeze(ctx context.Context, candidate source.Candidate) (source.Boundary, error) {
	if err := ctx.Err(); err != nil {
		return source.Boundary{}, err
	}
	a.mu.RLock()
	resolved, found := a.candidates[candidate.Handle]
	a.mu.RUnlock()
	if !found || candidate.Provider != providerCodex || candidate.SessionID != resolved.ID || candidate.InitialCWD != resolved.CWD ||
		candidate.StartedAt != candidateStartedAt(resolved) {
		return source.Boundary{}, errors.New("candidate was not returned by this Codex adapter")
	}
	files, err := session.OpenCandidates(a.sessionsRoot, resolved)
	if err != nil {
		state, code := memory.Unreadable, "unreadable_segment"
		if errors.Is(err, os.ErrNotExist) {
			state, code = memory.Missing, "missing_segment"
		}
		return source.Boundary{
			Candidate: candidate, TerminalState: state,
			Issues: []source.Issue{{Code: code, Provider: providerCodex, SessionID: candidate.SessionID, Path: resolved.Path, TerminalState: state}},
		}, nil
	}
	defer closeFiles(files)
	if err := ctx.Err(); err != nil {
		return source.Boundary{}, err
	}

	segments, logicalHash, logicalSize, lines, firstIdentity, err := freezeFiles(ctx, files)
	if err != nil {
		return source.Boundary{}, err
	}
	sourceIdentity := sourceIdentity(candidate.SessionID, firstIdentity)
	segmentBoundaries := make([]source.SegmentBoundary, len(segments))
	for index, segment := range segments {
		segmentBoundaries[index] = source.SegmentBoundary{Ordinal: index + 1, Size: segment.size, SourceHash: segment.hash}
	}
	boundaryHandle := opaqueHandle("boundary", candidate.Handle, sourceIdentity, logicalHash, strconv.FormatInt(logicalSize, 10))
	boundary := source.Boundary{
		Candidate: candidate, SourceIdentity: sourceIdentity,
		Frozen: memory.FrozenBoundary{
			Location:   memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: lines, ByteOffset: logicalSize}},
			SourceHash: logicalHash,
		},
		Segments: segmentBoundaries, TerminalState: memory.Indexed, Handle: boundaryHandle,
	}
	frozen := frozenSource{boundary: cloneBoundary(boundary), candidate: resolved, segments: segments}
	key := frozenSourceKey(providerCodex, candidate.SessionID, sourceIdentity)
	a.mu.Lock()
	a.frozen[boundaryHandle] = frozen
	a.frozenBySource[key] = appendUnique(a.frozenBySource[key], boundaryHandle)
	a.mu.Unlock()
	return cloneBoundary(boundary), nil
}

func (a *adapter) Read(ctx context.Context, ref memory.SourceRef, limit int64) ([]byte, error) {
	if err := source.ValidateReadLimit(limit); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.Provider != providerCodex || ref.SessionID == "" || ref.SourceIdentity == "" || !lowercaseSHA256.MatchString(ref.SourceHash) ||
		ref.Location.Kind != memory.SourceLocationJSONL || ref.Location.JSONL == nil || ref.Location.JSONL.Line < 1 || ref.Location.JSONL.ByteOffset < 0 {
		return nil, errors.New("invalid Codex source reference")
	}
	key := frozenSourceKey(ref.Provider, ref.SessionID, ref.SourceIdentity)
	a.mu.RLock()
	handles := append([]string(nil), a.frozenBySource[key]...)
	possible := make([]frozenSource, 0, len(handles))
	for _, handle := range handles {
		frozen := a.frozen[handle]
		location := frozen.boundary.Frozen.Location.JSONL
		if location != nil && ref.Location.JSONL.Line <= location.Line && ref.Location.JSONL.ByteOffset < location.ByteOffset {
			possible = append(possible, frozen)
		}
	}
	a.mu.RUnlock()
	if len(possible) == 0 {
		return nil, errors.New("source reference is outside every frozen Codex boundary")
	}
	var failures []error
	for _, frozen := range possible {
		span, err := a.readFrozen(ctx, frozen, ref, limit)
		if err == nil {
			return span, nil
		}
		failures = append(failures, err)
	}
	return nil, errors.Join(append([]error{errors.New("no frozen Codex boundary matched the exact source hash")}, failures...)...)
}

func (a *adapter) openExactFrozen(ctx context.Context, frozen frozenSource) ([]*os.File, error) {
	files, err := session.OpenCandidates(a.sessionsRoot, frozen.candidate)
	if err != nil {
		return nil, err
	}
	if err := a.verifyExactFrozen(ctx, files, frozen); err != nil {
		closeFiles(files)
		return nil, err
	}
	return files, nil
}

func (a *adapter) verifyExactFrozen(ctx context.Context, files []*os.File, frozen frozenSource) error {
	if len(files) != len(frozen.segments) {
		return errors.New("frozen Codex segment count changed")
	}
	overall := sha256.New()
	for index, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		segment := frozen.segments[index]
		info, err := file.Stat()
		if err != nil || info.Size() != segment.size {
			return errors.Join(errors.New("frozen Codex segment size changed"), err)
		}
		identity, err := pathguard.PhysicalFileIdentity(file)
		if err != nil || identity != segment.identity {
			return errors.Join(errors.New("frozen Codex segment identity changed"), err)
		}
		segmentHash := sha256.New()
		if _, err := io.Copy(io.MultiWriter(overall, segmentHash), io.NewSectionReader(file, 0, segment.size)); err != nil {
			return err
		}
		if hex.EncodeToString(segmentHash.Sum(nil)) != segment.hash {
			return errors.New("frozen Codex segment hash changed")
		}
		if segment.needsNewline {
			_, _ = overall.Write([]byte{'\n'})
		}
	}
	if hex.EncodeToString(overall.Sum(nil)) != frozen.boundary.Frozen.SourceHash {
		return errors.New("frozen Codex logical source hash changed")
	}
	return nil
}

func freezeFiles(ctx context.Context, files []*os.File) ([]frozenSegment, string, int64, int, pathguard.IdentityToken, error) {
	if len(files) == 0 {
		return nil, "", 0, 0, pathguard.IdentityToken{}, errors.New("Codex source has no physical segments")
	}
	overall := sha256.New()
	segments := make([]frozenSegment, 0, len(files))
	var logicalSize int64
	lines := 0
	var firstIdentity pathguard.IdentityToken
	buffer := make([]byte, 64<<10)
	for index, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, "", 0, 0, pathguard.IdentityToken{}, err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 {
			return nil, "", 0, 0, pathguard.IdentityToken{}, errors.Join(errors.New("inspect frozen Codex segment"), err)
		}
		identity, err := pathguard.PhysicalFileIdentity(file)
		if err != nil {
			return nil, "", 0, 0, pathguard.IdentityToken{}, err
		}
		if index == 0 {
			firstIdentity = identity
		}
		segmentHash := sha256.New()
		reader := io.NewSectionReader(file, 0, info.Size())
		remaining := info.Size()
		for remaining > 0 {
			if err := ctx.Err(); err != nil {
				return nil, "", 0, 0, pathguard.IdentityToken{}, err
			}
			wanted := int64(len(buffer))
			if remaining < wanted {
				wanted = remaining
			}
			count, readErr := io.ReadFull(reader, buffer[:wanted])
			if readErr != nil {
				return nil, "", 0, 0, pathguard.IdentityToken{}, readErr
			}
			chunk := buffer[:count]
			_, _ = overall.Write(chunk)
			_, _ = segmentHash.Write(chunk)
			lines += bytes.Count(chunk, []byte{'\n'})
			remaining -= int64(count)
		}
		needsNewline := false
		if info.Size() > 0 {
			var last [1]byte
			if _, err := file.ReadAt(last[:], info.Size()-1); err != nil {
				return nil, "", 0, 0, pathguard.IdentityToken{}, err
			}
			if last[0] != '\n' {
				needsNewline = true
				_, _ = overall.Write([]byte{'\n'})
				logicalSize++
				lines++
			}
		}
		logicalSize += info.Size()
		segments = append(segments, frozenSegment{
			size: info.Size(), hash: hex.EncodeToString(segmentHash.Sum(nil)), identity: identity, needsNewline: needsNewline,
		})
	}
	return segments, hex.EncodeToString(overall.Sum(nil)), logicalSize, lines, firstIdentity, nil
}

func (a *adapter) readFrozen(ctx context.Context, frozen frozenSource, ref memory.SourceRef, limit int64) ([]byte, error) {
	files, err := session.OpenCandidates(a.sessionsRoot, frozen.candidate)
	if err != nil {
		return nil, err
	}
	defer closeFiles(files)
	if len(files) != len(frozen.segments) {
		return nil, errors.New("frozen Codex segment count changed")
	}
	readers := make([]io.Reader, 0, len(files)*2)
	overall := sha256.New()
	for index, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		segment := frozen.segments[index]
		info, err := file.Stat()
		if err != nil || info.Size() < segment.size {
			return nil, errors.Join(errors.New("frozen Codex segment was truncated"), err)
		}
		identity, err := pathguard.PhysicalFileIdentity(file)
		if err != nil || identity != segment.identity {
			return nil, errors.Join(errors.New("frozen Codex segment identity changed"), err)
		}
		segmentHash := sha256.New()
		if _, err := io.Copy(io.MultiWriter(overall, segmentHash), io.NewSectionReader(file, 0, segment.size)); err != nil {
			return nil, err
		}
		if hex.EncodeToString(segmentHash.Sum(nil)) != segment.hash {
			return nil, errors.New("frozen Codex segment hash changed")
		}
		readers = append(readers, io.NewSectionReader(file, 0, segment.size))
		if segment.needsNewline {
			_, _ = overall.Write([]byte{'\n'})
			readers = append(readers, strings.NewReader("\n"))
		}
	}
	if hex.EncodeToString(overall.Sum(nil)) != frozen.boundary.Frozen.SourceHash {
		return nil, errors.New("frozen Codex logical source hash changed")
	}
	return readExactRecord(ctx, io.MultiReader(readers...), ref, limit)
}

func readExactRecord(ctx context.Context, reader io.Reader, ref memory.SourceRef, limit int64) ([]byte, error) {
	buffered := bufio.NewReaderSize(reader, 64<<10)
	var offset int64
	for lineNumber := 1; ; lineNumber++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := readBoundedLine(buffered, maxReadRecordBytes)
		if len(line) == 0 && err == io.EOF {
			return nil, errors.New("source reference line is outside frozen boundary")
		}
		start := offset
		offset += int64(len(line))
		if lineNumber == ref.Location.JSONL.Line {
			if start != ref.Location.JSONL.ByteOffset {
				return nil, errors.New("source reference byte offset does not match frozen boundary")
			}
			trimmed := bytes.TrimSpace(line)
			sum := sha256.Sum256(trimmed)
			if hex.EncodeToString(sum[:]) != ref.SourceHash {
				return nil, errors.New("source reference hash does not match frozen record")
			}
			if int64(len(trimmed)) > limit {
				trimmed = trimmed[:limit]
			}
			return bytes.Clone(trimmed), nil
		}
		if err == io.EOF {
			return nil, errors.New("source reference line is outside frozen boundary")
		}
		if err != nil {
			return nil, err
		}
	}
}

func readBoundedLine(reader *bufio.Reader, maximum int) ([]byte, error) {
	line := make([]byte, 0, 64<<10)
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > maximum-len(line) {
			return nil, errors.New("frozen Codex record exceeds the supported size")
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

func sourceIdentity(sessionID string, identity pathguard.IdentityToken) string {
	return opaqueHandle("source", sessionID, identity.Kind, identity.Volume, identity.File)
}

func opaqueHandle(kind string, parts ...string) string {
	sum := sha256.New()
	_, _ = io.WriteString(sum, kind)
	for _, part := range parts {
		_, _ = io.WriteString(sum, "\x00")
		_, _ = io.WriteString(sum, part)
	}
	return kind + "-" + hex.EncodeToString(sum.Sum(nil))
}

func frozenSourceKey(provider, sessionID, identity string) string {
	return provider + "\x00" + sessionID + "\x00" + identity
}

func candidateStartedAt(candidate session.Candidate) string {
	if candidate.StartedAt.IsZero() {
		return ""
	}
	return candidate.StartedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
}

func sameBoundary(value, frozen source.Boundary) bool {
	if value.Candidate != frozen.Candidate || value.SourceIdentity != frozen.SourceIdentity ||
		value.TerminalState != frozen.TerminalState || value.Handle != frozen.Handle ||
		value.Frozen.SourceHash != frozen.Frozen.SourceHash || value.Frozen.Location.Kind != frozen.Frozen.Location.Kind ||
		len(value.Segments) != len(frozen.Segments) || len(value.Issues) != len(frozen.Issues) {
		return false
	}
	valueLocation, frozenLocation := value.Frozen.Location.JSONL, frozen.Frozen.Location.JSONL
	if (valueLocation == nil) != (frozenLocation == nil) || (valueLocation != nil && *valueLocation != *frozenLocation) {
		return false
	}
	for index := range value.Segments {
		if value.Segments[index] != frozen.Segments[index] {
			return false
		}
	}
	for index := range value.Issues {
		if value.Issues[index] != frozen.Issues[index] {
			return false
		}
	}
	return true
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func cloneBoundary(value source.Boundary) source.Boundary {
	value.Segments = append([]source.SegmentBoundary(nil), value.Segments...)
	value.Issues = append([]source.Issue(nil), value.Issues...)
	return value
}

func closeFiles(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}
