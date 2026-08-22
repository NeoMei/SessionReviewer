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

	"github.com/neomei/SessionReviewer/internal/platform"
)

var ErrStop = errors.New("stop stream")

const (
	maxCurrentSessionAge = 24 * time.Hour
	futureClockSkew      = 5 * time.Minute
)

type Candidate struct {
	ID        string
	Path      string
	CWD       string
	Source    string
	StartedAt time.Time
	ModTime   time.Time
}

type ResolveOptions struct {
	SessionID       string
	CWD             string
	GOOS            string
	Now             time.Time
	AmbiguityWindow time.Duration
}

func Discover(root string) ([]Candidate, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if isSymlinkOrReparse(rootInfo) {
		return nil, fmt.Errorf("sessions path %q is a symlink or reparse point", root)
	}

	var candidates []Candidate
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
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

		candidate, found, err := discoverCandidate(path)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		candidate.ModTime = info.ModTime()
		candidates = append(candidates, candidate)
		return nil
	})
	return candidates, err
}

func discoverCandidate(path string) (Candidate, bool, error) {
	var candidate Candidate
	var metadataLine int
	summary, err := Stream(path, DecodeOptions{MaxRecordBytes: 64 << 20}, func(record Record) error {
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
		return Candidate{}, false, err
	}
	return candidate, candidate.ID != "", nil
}

func Resolve(candidates []Candidate, opts ResolveOptions) (Candidate, error) {
	if opts.AmbiguityWindow < 0 {
		return Candidate{}, fmt.Errorf("ambiguity window must not be negative")
	}
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
	if opts.Now.IsZero() {
		return Candidate{}, fmt.Errorf("current time is required to resolve the current session")
	}

	normalizedCWD := platform.NormalizePath(opts.GOOS, opts.CWD)
	var cwdMatches []Candidate
	for _, candidate := range candidates {
		if platform.NormalizePath(opts.GOOS, candidate.CWD) == normalizedCWD {
			cwdMatches = append(cwdMatches, candidate)
		}
	}
	if len(cwdMatches) == 0 {
		return Candidate{}, fmt.Errorf("no session matches working directory %q", opts.CWD)
	}

	oldestAllowed := opts.Now.Add(-maxCurrentSessionAge)
	latestAllowed := opts.Now.Add(futureClockSkew)
	var matches []Candidate
	var rejected []string
	for _, candidate := range cwdMatches {
		switch {
		case candidate.ModTime.Before(oldestAllowed):
			rejected = append(rejected, fmt.Sprintf("session %q is stale: modification time exceeds the 24h current-session age limit", candidate.ID))
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
