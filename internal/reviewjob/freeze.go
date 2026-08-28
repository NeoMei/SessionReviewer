package reviewjob

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/prepare"
	"github.com/neomei/SessionReviewer/internal/session"
)

// FreezeOptions binds discovery to one already authenticated configured
// project. ProjectIdentity prevents a mapping-path replacement between
// preflight and click-time discovery.
type FreezeOptions struct {
	SessionsRoot    string
	DataRoot        string
	ProjectID       string
	ProjectIdentity pathguard.IdentityToken
	MaxRecordBytes  int
}

// FreezePending returns only logical sessions with accepted cursors strictly
// below their click-time upper boundary. Candidate paths are used only while
// pinned handles are open and are never copied into FrozenSession.
func FreezePending(opts FreezeOptions) ([]FrozenSession, error) {
	if strings.TrimSpace(opts.SessionsRoot) == "" || strings.TrimSpace(opts.DataRoot) == "" {
		return nil, errors.New("sessions and data roots are required")
	}
	if !validID(opts.ProjectID) || !opts.ProjectIdentity.Valid() {
		return nil, errors.New("valid project ID and physical identity are required")
	}
	if opts.MaxRecordBytes < 0 {
		return nil, errors.New("max record bytes must not be negative")
	}

	data, err := pathguard.Open(opts.DataRoot)
	if err != nil {
		return nil, fmt.Errorf("open data root: %w", err)
	}
	defer data.Close()
	cfg, err := config.LoadRoot(data.Root, "config.toml")
	if err != nil {
		return nil, fmt.Errorf("load configuration: %w", err)
	}
	mapping, found := cfg.ProjectByID(opts.ProjectID)
	if !found {
		return nil, errors.New("configured project ID was not found")
	}
	project, err := pathguard.Open(mapping.Root)
	if err != nil {
		return nil, fmt.Errorf("open configured project root: %w", err)
	}
	defer project.Close()
	projectIdentity, err := project.PhysicalIdentity()
	if err != nil {
		return nil, fmt.Errorf("identify configured project root: %w", err)
	}
	if projectIdentity != opts.ProjectIdentity {
		return nil, errors.New("configured project physical identity changed")
	}

	discovery, err := session.Discover(opts.SessionsRoot, "")
	if err != nil {
		return nil, fmt.Errorf("discover sessions: %w", err)
	}
	associations, err := classifyAssociations(cfg)
	if err != nil {
		return nil, err
	}
	for _, issue := range discovery.Issues {
		associatedProject, classified := associations[issue.SessionID]
		if classified && associatedProject != opts.ProjectID {
			continue
		}
		return nil, fmt.Errorf("session discovery issue could belong to configured project: %w", issue.Err)
	}

	bySession := make(map[string][]session.Candidate)
	for _, candidate := range discovery.Candidates {
		if !validID(candidate.ID) {
			return nil, errors.New("discovered session ID is invalid")
		}
		associatedProject, classified := associations[candidate.ID]
		candidateProject, openErr := pathguard.Open(candidate.CWD)
		if openErr != nil {
			if classified && associatedProject != opts.ProjectID {
				continue
			}
			return nil, fmt.Errorf("authenticate session project directory: %w", openErr)
		}
		candidateIdentity, identityErr := candidateProject.PhysicalIdentity()
		closeErr := candidateProject.Close()
		if identityErr != nil {
			return nil, fmt.Errorf("identify session project directory: %w", identityErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close session project directory: %w", closeErr)
		}
		if candidateIdentity != projectIdentity {
			if classified && associatedProject == opts.ProjectID {
				return nil, errors.New("session association conflicts with its physical project directory")
			}
			continue
		}
		if classified && associatedProject != opts.ProjectID {
			return nil, errors.New("session association conflicts with its physical project directory")
		}
		bySession[candidate.ID] = append(bySession[candidate.ID], candidate)
	}

	ids := make([]string, 0, len(bySession))
	for id := range bySession {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	pending := make([]FrozenSession, 0, len(ids))
	for _, sessionID := range ids {
		chosen, err := session.Resolve(bySession[sessionID], session.ResolveOptions{
			SessionID:  sessionID,
			PathsEqual: func(string, string) bool { return true },
		})
		if err != nil {
			return nil, fmt.Errorf("resolve logical session %q: %w", sessionID, err)
		}
		if chosen.StartedAt.IsZero() {
			return nil, fmt.Errorf("session %q has no canonical start time", sessionID)
		}
		files, err := session.OpenCandidates(opts.SessionsRoot, chosen)
		if err != nil {
			return nil, fmt.Errorf("open session %q: %w", sessionID, err)
		}
		upper, streamErr := lastBoundary(files, opts.MaxRecordBytes)
		closeErr := closeSessionFiles(files)
		if streamErr != nil {
			return nil, fmt.Errorf("stream session %q: %w", sessionID, streamErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close session %q: %w", sessionID, closeErr)
		}
		if upper.Line == 0 {
			return nil, fmt.Errorf("session %q has no valid source boundary", sessionID)
		}

		current, err := cursor.LoadReadOnlyRoot(data.Root, opts.ProjectID, sessionID)
		if err != nil {
			return nil, fmt.Errorf("load accepted cursor for session %q: %w", sessionID, err)
		}
		if current.LastLine > upper.Line || (current.LastLine == upper.Line && current.LastHash != upper.SourceHash) {
			return nil, fmt.Errorf("session %q: %w", sessionID, prepare.ErrCursorSourceDrift)
		}
		if current.LastLine < upper.Line {
			pending = append(pending, FrozenSession{
				SessionID: sessionID,
				StartedAt: chosen.StartedAt.UTC(),
				Upper:     upper,
			})
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].StartedAt.Equal(pending[j].StartedAt) {
			return pending[i].SessionID < pending[j].SessionID
		}
		return pending[i].StartedAt.Before(pending[j].StartedAt)
	})
	if err := validateFrozenSessions(pending); err != nil {
		return nil, err
	}
	return pending, nil
}

func classifyAssociations(cfg config.Config) (map[string]string, error) {
	projects := make(map[string]struct{}, len(cfg.Projects))
	for _, project := range cfg.Projects {
		projects[project.ID] = struct{}{}
	}
	result := make(map[string]string, len(cfg.SessionAssociations))
	for _, association := range cfg.SessionAssociations {
		if !validID(association.SessionID) || !validID(association.ProjectID) {
			return nil, errors.New("session association is invalid")
		}
		if _, found := projects[association.ProjectID]; !found {
			return nil, errors.New("session association names an unknown project")
		}
		if existing, found := result[association.SessionID]; found && existing != association.ProjectID {
			return nil, errors.New("session association is ambiguous")
		}
		result[association.SessionID] = association.ProjectID
	}
	return result, nil
}

func lastBoundary(files []*os.File, maxRecordBytes int) (evidence.CursorBoundary, error) {
	var upper evidence.CursorBoundary
	summary, err := session.StreamFiles(files, session.DecodeOptions{FromLine: 1, MaxRecordBytes: maxRecordBytes}, func(record session.Record) error {
		upper = evidence.CursorBoundary{Line: record.Line, SourceHash: record.SourceHash}
		return nil
	})
	if err != nil {
		return evidence.CursorBoundary{}, err
	}
	if summary.MalformedLines != 0 {
		return evidence.CursorBoundary{}, errors.New("session contains malformed JSONL records")
	}
	return upper, nil
}

func closeSessionFiles(files []*os.File) error {
	var result error
	for _, file := range files {
		result = errors.Join(result, file.Close())
	}
	return result
}
