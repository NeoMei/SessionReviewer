package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/platform"
)

var ErrStop = errors.New("stop stream")

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
	var candidates []Candidate
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
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
		info, err := entry.Info()
		if err != nil {
			return err
		}
		candidate.ModTime = info.ModTime()
		candidates = append(candidates, candidate)
		return nil
	})
	return candidates, err
}

func discoverCandidate(path string) (Candidate, bool, error) {
	var candidate Candidate
	_, err := Stream(path, DecodeOptions{MaxRecordBytes: 64 << 20}, func(record Record) error {
		if record.Type != "session_meta" {
			return nil
		}

		var meta struct {
			ID     string `json:"id"`
			CWD    string `json:"cwd"`
			Source string `json:"source"`
		}
		if err := json.Unmarshal(record.Payload, &meta); err != nil {
			return fmt.Errorf("decode session metadata in %q: %w", path, err)
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
				return fmt.Errorf("decode session timestamp in %q: %w", path, err)
			}
			candidate.StartedAt = startedAt
		}
		return ErrStop
	})
	if err != nil && !errors.Is(err, ErrStop) {
		return Candidate{}, false, err
	}
	return candidate, candidate.ID != "", nil
}

func Resolve(candidates []Candidate, opts ResolveOptions) (Candidate, error) {
	if opts.SessionID != "" {
		for _, candidate := range candidates {
			if candidate.ID == opts.SessionID {
				return candidate, nil
			}
		}
		return Candidate{}, fmt.Errorf("session %q not found", opts.SessionID)
	}

	normalizedCWD := platform.NormalizePath(opts.GOOS, opts.CWD)
	var matches []Candidate
	for _, candidate := range candidates {
		if platform.NormalizePath(opts.GOOS, candidate.CWD) == normalizedCWD {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return Candidate{}, fmt.Errorf("no session matches working directory %q", opts.CWD)
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ModTime.After(matches[j].ModTime)
	})
	if len(matches) > 1 && matches[0].ModTime.Sub(matches[1].ModTime) < opts.AmbiguityWindow {
		return Candidate{}, fmt.Errorf("ambiguous current session: %s and %s", matches[0].ID, matches[1].ID)
	}
	return matches[0], nil
}
