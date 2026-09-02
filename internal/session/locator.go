package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
)

var (
	ErrStop             = errors.New("stop stream")
	ErrSessionAmbiguous = errors.New("current session is ambiguous")
	ErrSessionConflict  = errors.New("session identity has conflicting candidates")
	ErrDiscoveryBudget  = errors.New("session discovery budget exceeded")
)

const (
	futureClockSkew    = 5 * time.Minute
	maxSessionSegments = 256
)

type DiscoveryLimits struct {
	MaxEntries    int
	MaxCandidates int
	MaxBytes      int64
}

var defaultDiscoveryLimits = DiscoveryLimits{
	MaxEntries:    1_000_000,
	MaxCandidates: 100_000,
	MaxBytes:      64 << 30,
}

type Candidate struct {
	ID        string
	Path      string
	CWD       string
	StartedAt time.Time
	ModTime   time.Time
	fileInfo  os.FileInfo
	rootInfo  os.FileInfo
	relative  string
	segments  []Candidate
}

type DiscoveryIssue struct {
	Path      string
	SessionID string
	CWD       string
	Err       error
}

type Discovery struct {
	Candidates []Candidate
	Issues     []DiscoveryIssue
}

type ResolveOptions struct {
	SessionID       string
	CWD             string
	GOOS            string
	Now             time.Time
	AmbiguityWindow time.Duration
	PathsEqual      func(string, string) bool
}

func Discover(root, selectedSessionID string) (Discovery, error) {
	return DiscoverWithLimits(root, selectedSessionID, defaultDiscoveryLimits)
}

func DiscoverWithLimits(root, selectedSessionID string, limits DiscoveryLimits) (Discovery, error) {
	if limits.MaxEntries < 1 || limits.MaxCandidates < 1 || limits.MaxBytes < 1 {
		return Discovery{}, fmt.Errorf("%w: limits must be positive", ErrDiscoveryBudget)
	}
	directory, err := pathguard.Open(root)
	if err != nil {
		return Discovery{}, fmt.Errorf("sessions path %q is redirected or invalid: %w", root, err)
	}
	defer directory.Close()

	var discovery Discovery
	entries, candidates := 0, 0
	var bytes int64
	err = fs.WalkDir(directory.Root.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > limits.MaxEntries {
			return fmt.Errorf("%w: too many filesystem entries", ErrDiscoveryBudget)
		}
		path := filepath.Join(directory.Path, filepath.FromSlash(relative))
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if isSymlinkOrReparse(info) {
			return fmt.Errorf("sessions path %q is a symlink or reparse point", path)
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
			return nil
		}
		candidates++
		if candidates > limits.MaxCandidates {
			return fmt.Errorf("%w: too many JSONL candidates", ErrDiscoveryBudget)
		}
		if info.Size() < 0 || info.Size() > limits.MaxBytes-bytes {
			return fmt.Errorf("%w: aggregate JSONL bytes exceed the limit", ErrDiscoveryBudget)
		}
		bytes += info.Size()

		file, identity, err := directory.OpenRegular(relative)
		if err != nil {
			return fmt.Errorf("open session candidate %q: %w", path, err)
		}
		candidate, found, issue := discoverCandidateFile(file, path, selectedSessionID)
		closeErr := file.Close()
		if closeErr != nil {
			return closeErr
		}
		if issue != nil {
			discovery.Issues = append(discovery.Issues, *issue)
			return nil
		}
		if !found {
			return nil
		}
		candidate.Path = path
		candidate.ModTime = identity.ModTime()
		candidate.fileInfo = identity
		candidate.rootInfo = directory.Info()
		candidate.relative = relative
		discovery.Candidates = append(discovery.Candidates, candidate)
		return nil
	})
	if err != nil {
		return Discovery{}, err
	}
	return discovery, nil
}

func discoverCandidateFile(file *os.File, path, selectedSessionID string) (Candidate, bool, *DiscoveryIssue) {
	var candidate Candidate
	var rootSessionID string
	var metadataLine int
	var metadataErr error
	summary, err := StreamFile(file, DecodeOptions{MaxRecordBytes: 64 << 20}, func(record Record) error {
		if record.Type != "session_meta" {
			return nil
		}

		var meta struct {
			ID        string `json:"id"`
			SessionID string `json:"session_id"`
			CWD       string `json:"cwd"`
		}
		if err := json.Unmarshal(record.Payload, &meta); err != nil {
			if metadataErr == nil {
				metadataErr = fmt.Errorf("decode session metadata in %q at line %d: %w", path, record.Line, err)
			}
			return nil
		}
		if strings.TrimSpace(meta.ID) == "" {
			if metadataErr == nil {
				metadataErr = fmt.Errorf("decode session metadata in %q at line %d: session id is missing or blank", path, record.Line)
			}
			return nil
		}

		if metadataLine == 0 {
			metadataLine = record.Line
			rootSessionID = meta.SessionID
			candidate = Candidate{
				ID:   meta.ID,
				Path: path,
				CWD:  meta.CWD,
			}
			if record.Timestamp != "" {
				startedAt, err := time.Parse(time.RFC3339Nano, record.Timestamp)
				if err != nil {
					if metadataErr == nil {
						metadataErr = fmt.Errorf("decode session timestamp in %q at line %d: %w", path, record.Line, err)
					}
				} else {
					candidate.StartedAt = startedAt
				}
			}
			if selectedSessionID != "" && candidate.ID != selectedSessionID {
				return ErrStop
			}
			return nil
		}
		inheritedMetadata := rootSessionID != "" && meta.SessionID == rootSessionID && meta.CWD == candidate.CWD
		if meta.ID != candidate.ID && !inheritedMetadata && metadataErr == nil {
			metadataErr = fmt.Errorf("decode session metadata in %q at line %d: conflicting session id", path, record.Line)
		}
		return nil
	})
	issue := func(issueErr error) (Candidate, bool, *DiscoveryIssue) {
		return Candidate{}, false, &DiscoveryIssue{Path: path, SessionID: candidate.ID, CWD: candidate.CWD, Err: issueErr}
	}
	issueErr := metadataErr
	if summary.MalformedLines > 0 {
		var malformedErr error
		if metadataLine > 0 {
			malformedErr = fmt.Errorf("discover session metadata in %q: %d malformed JSONL record(s) in candidate with metadata at line %d", path, summary.MalformedLines, metadataLine)
		} else {
			malformedErr = fmt.Errorf("discover session metadata in %q: %d malformed JSONL record(s)", path, summary.MalformedLines)
		}
		issueErr = errors.Join(issueErr, malformedErr)
	}
	if err != nil && !errors.Is(err, ErrStop) {
		issueErr = errors.Join(issueErr, fmt.Errorf("stream session %q: %w", path, err))
	}
	if issueErr != nil {
		return issue(issueErr)
	}
	return candidate, candidate.ID != "", nil
}

// OpenCandidate opens exactly the regular file observed during discovery. The
// returned handle remains bound to those bytes even if the pathname changes.
func OpenCandidate(root string, candidate Candidate) (*os.File, error) {
	if candidate.fileInfo == nil || candidate.rootInfo == nil || candidate.relative == "" {
		return nil, fmt.Errorf("session candidate has no discovery identity")
	}
	directory, err := pathguard.Open(root)
	if err != nil {
		return nil, fmt.Errorf("open sessions root: %w", err)
	}
	defer directory.Close()
	if !os.SameFile(directory.Info(), candidate.rootInfo) {
		return nil, fmt.Errorf("sessions root changed after discovery")
	}
	file, identity, err := directory.OpenRegular(candidate.relative)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(identity, candidate.fileInfo) {
		_ = file.Close()
		return nil, fmt.Errorf("session file changed after discovery")
	}
	return file, nil
}

// OpenCandidates opens every ordered physical segment represented by a
// resolved session candidate. A normal one-file session returns one handle.
func OpenCandidates(root string, candidate Candidate) ([]*os.File, error) {
	segments := candidate.segments
	if len(segments) == 0 {
		segments = []Candidate{candidate}
	}
	files := make([]*os.File, 0, len(segments))
	for _, segment := range segments {
		file, err := OpenCandidate(root, segment)
		if err != nil {
			for _, opened := range files {
				_ = opened.Close()
			}
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func Resolve(candidates []Candidate, opts ResolveOptions) (Candidate, error) {
	if opts.SessionID != "" {
		var matches []Candidate
		for _, candidate := range candidates {
			if candidate.ID == opts.SessionID {
				matches = append(matches, candidate)
			}
		}
		if len(matches) == 0 {
			return Candidate{}, fmt.Errorf("session %q not found", opts.SessionID)
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		return resolveSegments(matches, opts)
	}
	if opts.AmbiguityWindow < 0 {
		return Candidate{}, fmt.Errorf("ambiguity window must not be negative")
	}
	if opts.Now.IsZero() {
		return Candidate{}, fmt.Errorf("current time is required to resolve the current session")
	}

	normalizedCWD := platform.NormalizePath(opts.GOOS, opts.CWD)
	var cwdMatches []Candidate
	for _, candidate := range candidates {
		matches := platform.NormalizePath(opts.GOOS, candidate.CWD) == normalizedCWD
		if opts.PathsEqual != nil {
			matches = opts.PathsEqual(candidate.CWD, opts.CWD)
		}
		if matches {
			cwdMatches = append(cwdMatches, candidate)
		}
	}
	if len(cwdMatches) == 0 {
		return Candidate{}, fmt.Errorf("no session matches working directory %q", opts.CWD)
	}

	latestAllowed := opts.Now.Add(futureClockSkew)
	var matches []Candidate
	var rejected []string
	for _, candidate := range cwdMatches {
		switch {
		case candidate.ModTime.After(latestAllowed):
			rejected = append(rejected, fmt.Sprintf("session %q has a future modification time beyond the 5m clock-skew allowance", candidate.ID))
		case !candidate.StartedAt.IsZero() && candidate.StartedAt.After(latestAllowed):
			rejected = append(rejected, fmt.Sprintf("session %q has a future start time beyond the 5m clock-skew allowance", candidate.ID))
		default:
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return Candidate{}, fmt.Errorf("no current session matches working directory %q: %s", opts.CWD, strings.Join(rejected, "; "))
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ModTime.After(matches[j].ModTime)
	})
	if len(matches) > 1 {
		lead := matches[0].ModTime.Sub(matches[1].ModTime)
		if lead == 0 || lead < opts.AmbiguityWindow {
			return Candidate{}, fmt.Errorf("%w: %s and %s", ErrSessionAmbiguous, matches[0].ID, matches[1].ID)
		}
	}
	return matches[0], nil
}

func resolveSegments(matches []Candidate, opts ResolveOptions) (Candidate, error) {
	if len(matches) > maxSessionSegments {
		return Candidate{}, fmt.Errorf("%w: session %q has too many physical segments", ErrDiscoveryBudget, opts.SessionID)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].StartedAt.Equal(matches[j].StartedAt) {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].StartedAt.Before(matches[j].StartedAt)
	})
	paths := make([]string, 0, len(matches))
	for _, match := range matches {
		paths = append(paths, match.Path)
	}
	for index, match := range matches {
		if match.StartedAt.IsZero() || (index > 0 && !match.StartedAt.After(matches[index-1].StartedAt)) {
			return Candidate{}, fmt.Errorf("%w: duplicate session id %q found at paths: %s", ErrSessionConflict, opts.SessionID, strings.Join(paths, ", "))
		}
		if index == 0 {
			continue
		}
		sameProject := platform.NormalizePath(opts.GOOS, matches[0].CWD) == platform.NormalizePath(opts.GOOS, match.CWD)
		if opts.PathsEqual != nil {
			sameProject = opts.PathsEqual(matches[0].CWD, match.CWD)
		}
		if !sameProject {
			return Candidate{}, fmt.Errorf("%w: duplicate session id %q spans different projects", ErrSessionConflict, opts.SessionID)
		}
	}
	composite := matches[0]
	composite.segments = append([]Candidate(nil), matches...)
	for _, match := range matches[1:] {
		if match.ModTime.After(composite.ModTime) {
			composite.ModTime = match.ModTime
		}
	}
	return composite, nil
}

func ResolveDiscovery(discovery Discovery, opts ResolveOptions) (Candidate, error) {
	if opts.SessionID == "" && len(discovery.Issues) > 0 {
		return Candidate{}, fmt.Errorf("current-session discovery contains corrupt candidates; select a session explicitly")
	}

	matches := 0
	for _, candidate := range discovery.Candidates {
		if candidate.ID == opts.SessionID {
			matches++
		}
	}
	selectedIssues := 0
	for _, issue := range discovery.Issues {
		if issue.SessionID == opts.SessionID {
			selectedIssues++
		}
	}
	if opts.SessionID != "" && selectedIssues > 0 && matches+selectedIssues > 1 {
		return Candidate{}, fmt.Errorf("%w: duplicate session id %q includes a corrupt candidate", ErrSessionConflict, opts.SessionID)
	}
	if selectedIssues == 1 {
		return Candidate{}, fmt.Errorf("selected session candidate is corrupt")
	}
	if selectedIssues > 1 {
		return Candidate{}, fmt.Errorf("%w: duplicate session id %q includes corrupt candidates", ErrSessionConflict, opts.SessionID)
	}
	return Resolve(discovery.Candidates, opts)
}
