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

var ErrStop = errors.New("stop stream")

const futureClockSkew = 5 * time.Minute

type Candidate struct {
	ID        string
	Path      string
	CWD       string
	Source    string
	StartedAt time.Time
	ModTime   time.Time
	fileInfo  os.FileInfo
	rootInfo  os.FileInfo
	relative  string
}

type ResolveOptions struct {
	SessionID       string
	CWD             string
	GOOS            string
	Now             time.Time
	AmbiguityWindow time.Duration
	PathsEqual      func(string, string) bool
}

func Discover(root string) ([]Candidate, error) {
	directory, err := pathguard.Open(root)
	if err != nil {
		return nil, fmt.Errorf("sessions path %q is redirected or invalid: %w", root, err)
	}
	defer directory.Close()

	var candidates []Candidate
	err = fs.WalkDir(directory.Root.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
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

		file, identity, err := directory.OpenRegular(relative)
		if err != nil {
			return fmt.Errorf("open session candidate %q: %w", path, err)
		}
		candidate, found, discoverErr := discoverCandidateFile(file, path)
		closeErr := file.Close()
		if discoverErr != nil {
			return discoverErr
		}
		if closeErr != nil {
			return closeErr
		}
		if !found {
			return nil
		}
		candidate.Path = path
		candidate.ModTime = identity.ModTime()
		candidate.fileInfo = identity
		candidate.rootInfo = directory.Info()
		candidate.relative = relative
		candidates = append(candidates, candidate)
		return nil
	})
	return candidates, err
}

func discoverCandidateFile(file *os.File, path string) (Candidate, bool, error) {
	var candidate Candidate
	var metadataLine int
	summary, err := StreamFile(file, DecodeOptions{MaxRecordBytes: 64 << 20}, func(record Record) error {
		if record.Type != "session_meta" {
			return nil
		}
		metadataLine = record.Line

		var meta struct {
			ID     string `json:"id"`
			CWD    string `json:"cwd"`
			Source string `json:"source"`
		}
		if err := json.Unmarshal(record.Payload, &meta); err != nil {
			return fmt.Errorf("decode session metadata in %q at line %d: %w", path, record.Line, err)
		}
		if strings.TrimSpace(meta.ID) == "" {
			return fmt.Errorf("decode session metadata in %q at line %d: session id is missing or blank", path, record.Line)
		}

		candidate = Candidate{
			ID:     meta.ID,
			Path:   path,
			CWD:    meta.CWD,
			Source: meta.Source,
		}
		if record.Timestamp != "" {
			startedAt, err := time.Parse(time.RFC3339Nano, record.Timestamp)
			if err != nil {
				return fmt.Errorf("decode session timestamp in %q at line %d: %w", path, record.Line, err)
			}
			candidate.StartedAt = startedAt
		}
		return ErrStop
	})
	if summary.MalformedLines > 0 {
		if metadataLine > 0 {
			return Candidate{}, false, fmt.Errorf("discover session metadata in %q: %d malformed JSONL record(s) before metadata at line %d", path, summary.MalformedLines, metadataLine)
		}
		return Candidate{}, false, fmt.Errorf("discover session metadata in %q: %d malformed JSONL record(s)", path, summary.MalformedLines)
	}
	if err != nil && !errors.Is(err, ErrStop) {
		return Candidate{}, false, fmt.Errorf("stream session %q: %w", path, err)
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
		if len(matches) > 1 {
			paths := make([]string, 0, len(matches))
			for _, match := range matches {
				paths = append(paths, match.Path)
			}
			return Candidate{}, fmt.Errorf("duplicate session id %q found at paths: %s", opts.SessionID, strings.Join(paths, ", "))
		}
		return matches[0], nil
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
			return Candidate{}, fmt.Errorf("ambiguous current session: %s and %s", matches[0].ID, matches[1].ID)
		}
	}
	return matches[0], nil
}
