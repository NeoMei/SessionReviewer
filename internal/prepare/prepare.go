package prepare

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/session"
)

type Options struct {
	Mode              string
	SessionsRoot      string
	SessionID         string
	CWD               string
	DataDir           string
	Output            string
	GOOS              string
	FromStart         bool
	Now               time.Time
	AmbiguityWindow   time.Duration
	Limits            evidence.Limits
	MaxRecordBytes    int
	beforeOpenSession func() error
	afterOpenSession  func() error
	afterOpenDataDir  func() error
	afterLoadConfig   func() error
}

var (
	ErrCursorSourceDrift     = errors.New("accepted cursor no longer matches session source")
	ErrSessionNotFound       = errors.New("selected session was not found")
	ErrSessionAmbiguous      = errors.New("current session is ambiguous")
	ErrProjectNotInitialized = errors.New("selected project is not initialized")
	ErrUnsafeOutput          = errors.New("evidence output path is unsafe")
)

func Run(opts Options) (evidence.Packet, error) {
	if err := validateOptions(&opts); err != nil {
		return evidence.Packet{}, err
	}
	dataDir, err := pathguard.Open(opts.DataDir)
	if err != nil {
		return evidence.Packet{}, fmt.Errorf("invalid data directory: %w", err)
	}
	defer dataDir.Close()
	output, err := prepareOutputTarget(opts.Output, opts.SessionsRoot, dataDir)
	if err != nil {
		return evidence.Packet{}, fmt.Errorf("%w: %w", ErrUnsafeOutput, err)
	}
	defer output.close()
	if opts.afterOpenDataDir != nil {
		if err := opts.afterOpenDataDir(); err != nil {
			return evidence.Packet{}, fmt.Errorf("prepare data root: %w", err)
		}
	}
	discovery, err := session.Discover(opts.SessionsRoot, opts.SessionID)
	if err != nil {
		return evidence.Packet{}, fmt.Errorf("discover sessions: %w", err)
	}
	pathsEqual := func(first, second string) bool {
		same, _ := sameProjectDirectory(opts.GOOS, first, second)
		return same
	}
	chosen, err := session.ResolveDiscovery(discovery, session.ResolveOptions{
		SessionID: opts.SessionID, CWD: opts.CWD, GOOS: opts.GOOS,
		Now: opts.Now, AmbiguityWindow: opts.AmbiguityWindow, PathsEqual: pathsEqual,
	})
	if err != nil {
		if opts.SessionID != "" && !discoveryContainsSessionID(discovery, opts.SessionID) {
			return evidence.Packet{}, fmt.Errorf("%w: %w", ErrSessionNotFound, err)
		}
		if opts.SessionID == "" && errors.Is(err, session.ErrSessionAmbiguous) {
			return evidence.Packet{}, fmt.Errorf("%w: %w", ErrSessionAmbiguous, err)
		}
		return evidence.Packet{}, err
	}
	sameProject, err := sameProjectDirectory(opts.GOOS, chosen.CWD, opts.CWD)
	if err != nil {
		return evidence.Packet{}, fmt.Errorf("validate selected session project: %w", err)
	}
	if !sameProject {
		return evidence.Packet{}, fmt.Errorf("selected session belongs to a different project")
	}
	if err := validateIdentifier(chosen.ID, "session id"); err != nil {
		return evidence.Packet{}, err
	}
	cfg, err := config.LoadRoot(dataDir.Root, "config.toml")
	if err != nil {
		return evidence.Packet{}, fmt.Errorf("load configuration: %w", err)
	}
	if opts.afterLoadConfig != nil {
		if err := opts.afterLoadConfig(); err != nil {
			return evidence.Packet{}, fmt.Errorf("prepare configuration snapshot: %w", err)
		}
	}
	mapping, err := findConfiguredProject(cfg, opts.GOOS, chosen.CWD)
	if err != nil {
		return evidence.Packet{}, err
	}
	if mapping.ID == "" {
		return evidence.Packet{}, ErrProjectNotInitialized
	}
	if err := validateIdentifier(mapping.ID, "project id"); err != nil {
		return evidence.Packet{}, err
	}
	if opts.beforeOpenSession != nil {
		if err := opts.beforeOpenSession(); err != nil {
			return evidence.Packet{}, fmt.Errorf("prepare session open: %w", err)
		}
	}
	sessionFile, err := session.OpenCandidate(opts.SessionsRoot, chosen)
	if err != nil {
		return evidence.Packet{}, fmt.Errorf("open selected session: %w", err)
	}
	defer sessionFile.Close()
	if opts.afterOpenSession != nil {
		if err := opts.afterOpenSession(); err != nil {
			return evidence.Packet{}, fmt.Errorf("prepare session stream: %w", err)
		}
	}

	from := 1
	var stored cursor.Cursor
	if !opts.FromStart {
		stored, err = readCursorRoot(dataDir.Root, mapping.ID, chosen.ID)
		if err != nil {
			return evidence.Packet{}, err
		}
		if stored.LastLine == int(^uint(0)>>1) {
			return evidence.Packet{}, fmt.Errorf("cursor cannot be incremented")
		}
		from = stored.LastLine + 1
	}
	x, err := evidence.NewWithProjectID(mapping.ID, chosen.ID, chosen.CWD, from, redact.Default(), opts.Limits)
	if err != nil {
		return evidence.Packet{}, err
	}
	if err := x.SetExpectedCursor(evidence.CursorBoundary{Line: stored.LastLine, SourceHash: stored.LastHash}); err != nil {
		return evidence.Packet{}, fmt.Errorf("bind evidence packet to accepted cursor: %w", err)
	}
	cursorValidated := stored.LastLine == 0
	usage := accounting.NewAccumulator(chosen.StartedAt)
	visit := func(record session.Record) error {
		previousUsage := x.Packet().SessionUsage
		observe := func() error {
			if err := usage.Observe(record); err != nil {
				return err
			}
			return x.SetSessionUsage(usage.Snapshot())
		}
		if !opts.FromStart && record.Line < stored.LastLine {
			return observe()
		}
		if !opts.FromStart && record.Line == stored.LastLine {
			if record.SourceHash != stored.LastHash {
				return ErrCursorSourceDrift
			}
			cursorValidated = true
			return observe()
		}
		if err := observe(); err != nil {
			return err
		}
		if err := x.Add(record); err != nil {
			_ = x.SetSessionUsage(previousUsage)
			return err
		}
		return nil
	}
	summary, streamErr := session.StreamFile(sessionFile, session.DecodeOptions{FromLine: 1, MaxRecordBytes: opts.MaxRecordBytes}, visit)
	if errors.Is(streamErr, ErrCursorSourceDrift) {
		return evidence.Packet{}, fmt.Errorf("%w: accepted cursor hash does not match the selected source", ErrCursorSourceDrift)
	}
	if streamErr != nil && !errors.Is(streamErr, evidence.ErrPacketFull) {
		return evidence.Packet{}, fmt.Errorf("extract session evidence from %q: %w", chosen.Path, streamErr)
	}
	if !cursorValidated {
		return evidence.Packet{}, fmt.Errorf("%w: accepted cursor line is absent from the selected source", ErrCursorSourceDrift)
	}
	packet := x.Packet()
	if summary.MalformedLines > 0 {
		if err := x.AddWarning(fmt.Sprintf("malformed_jsonl_lines:%d", summary.MalformedLines)); err != nil {
			return evidence.Packet{}, fmt.Errorf("bound evidence warnings: %w", err)
		}
		packet = x.Packet()
	}
	b, err := json.Marshal(packet)
	if err != nil {
		return evidence.Packet{}, fmt.Errorf("encode evidence packet: %w", err)
	}
	if err := output.write(append(b, '\n')); err != nil {
		return evidence.Packet{}, fmt.Errorf("%w: write evidence packet: %w", ErrUnsafeOutput, err)
	}
	return packet, nil
}

func discoveryContainsSessionID(discovery session.Discovery, sessionID string) bool {
	for _, candidate := range discovery.Candidates {
		if candidate.ID == sessionID {
			return true
		}
	}
	for _, issue := range discovery.Issues {
		if issue.SessionID == sessionID {
			return true
		}
	}
	return false
}

func validateOptions(opts *Options) error {
	if opts.Mode != "review" && opts.Mode != "checkpoint" {
		return fmt.Errorf("invalid prepare mode %q", opts.Mode)
	}
	if opts.Mode == "checkpoint" && opts.FromStart {
		return fmt.Errorf("--from-start is valid only for review")
	}
	if opts.SessionsRoot == "" {
		return fmt.Errorf("sessions root is required")
	}
	if opts.CWD == "" {
		return fmt.Errorf("working directory is required")
	}
	if opts.DataDir == "" {
		return fmt.Errorf("data directory is required")
	}
	if opts.Output == "" {
		return fmt.Errorf("output path is required")
	}
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.Limits == (evidence.Limits{}) {
		opts.Limits = evidence.DefaultLimits()
	}
	if err := opts.Limits.Validate(); err != nil {
		return err
	}
	if opts.MaxRecordBytes < 0 {
		return fmt.Errorf("max record bytes must not be negative")
	}
	if opts.AmbiguityWindow < 0 {
		return fmt.Errorf("ambiguity window must not be negative")
	}
	if opts.SessionID != "" {
		if err := validateIdentifier(opts.SessionID, "session id"); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		label string
		path  *string
	}{
		{label: "sessions root", path: &opts.SessionsRoot},
		{label: "working directory", path: &opts.CWD},
	} {
		absolute, err := filepath.Abs(*field.path)
		if err != nil {
			return fmt.Errorf("invalid %s", field.label)
		}
		directory, err := pathguard.Open(absolute)
		if err != nil {
			return fmt.Errorf("invalid %s: %w", field.label, err)
		}
		_ = directory.Close()
		*field.path = absolute
	}
	absoluteData, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return fmt.Errorf("invalid data directory")
	}
	opts.DataDir = absoluteData
	return nil
}

func findConfiguredProject(cfg config.Config, goos, cwd string) (config.ProjectMapping, error) {
	if goos != runtime.GOOS {
		normalized := platform.NormalizePath(goos, cwd)
		var match config.ProjectMapping
		matches := 0
		for _, project := range cfg.Projects {
			if platform.NormalizePath(goos, project.Root) != normalized {
				continue
			}
			match = project
			matches++
		}
		if matches > 1 {
			return config.ProjectMapping{}, fmt.Errorf("configured project mapping is ambiguous")
		}
		return match, nil
	}
	current, err := pathguard.Open(cwd)
	if err != nil {
		return config.ProjectMapping{}, fmt.Errorf("validate requested project root: %w", err)
	}
	defer current.Close()
	var match config.ProjectMapping
	matches := 0
	for _, project := range cfg.Projects {
		mapped, err := pathguard.Open(project.Root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return config.ProjectMapping{}, fmt.Errorf("validate configured project mapping: %w", err)
		}
		same := os.SameFile(mapped.Info(), current.Info())
		_ = mapped.Close()
		if !same {
			continue
		}
		match = project
		matches++
	}
	if matches > 1 {
		return config.ProjectMapping{}, fmt.Errorf("configured project mapping is ambiguous")
	}
	return match, nil
}

func sameProjectDirectory(goos, first, second string) (bool, error) {
	if goos != runtime.GOOS {
		return platform.NormalizePath(goos, first) == platform.NormalizePath(goos, second), nil
	}
	return pathguard.SameDirectory(first, second)
}

var safeIdentifier = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func validateIdentifier(value, label string) error {
	if value == "" || value == "." || value == ".." || !safeIdentifier.MatchString(value) {
		return fmt.Errorf("invalid %s", label)
	}
	trimmed := strings.TrimRight(value, " .")
	if dot := strings.IndexByte(trimmed, '.'); dot >= 0 {
		trimmed = trimmed[:dot]
	}
	upper := strings.ToUpper(trimmed)
	if upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" ||
		(len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9') {
		return fmt.Errorf("invalid %s", label)
	}
	return nil
}

func readCursorRoot(dataRoot *os.Root, projectID, sessionID string) (cursor.Cursor, error) {
	return cursor.LoadReadOnlyRoot(dataRoot, projectID, sessionID)
}
