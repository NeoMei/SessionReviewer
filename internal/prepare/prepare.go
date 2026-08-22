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
}

func Run(opts Options) (evidence.Packet, error) {
	if err := validateOptions(&opts); err != nil {
		return evidence.Packet{}, err
	}
	output, err := prepareOutputTarget(opts.Output, opts.SessionsRoot, opts.DataDir)
	if err != nil {
		return evidence.Packet{}, err
	}
	defer output.close()
	candidates, err := session.Discover(opts.SessionsRoot)
	if err != nil {
		return evidence.Packet{}, fmt.Errorf("discover sessions: %w", err)
	}
	pathsEqual := func(first, second string) bool {
		same, _ := sameProjectDirectory(opts.GOOS, first, second)
		return same
	}
	chosen, err := session.Resolve(candidates, session.ResolveOptions{
		SessionID: opts.SessionID, CWD: opts.CWD, GOOS: opts.GOOS,
		Now: opts.Now, AmbiguityWindow: opts.AmbiguityWindow, PathsEqual: pathsEqual,
	})
	if err != nil {
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
	cfg, err := config.Load(filepath.Join(opts.DataDir, "config.toml"))
	if err != nil {
		return evidence.Packet{}, fmt.Errorf("load configuration: %w", err)
	}
	mapping, err := findConfiguredProject(cfg, opts.GOOS, chosen.CWD)
	if err != nil {
		return evidence.Packet{}, err
	}
	if mapping.ID == "" {
		return evidence.Packet{}, fmt.Errorf("selected project is not initialized")
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
	if !opts.FromStart {
		stored, err := readCursor(opts.DataDir, mapping.ID, chosen.ID)
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
	summary, streamErr := session.StreamFile(sessionFile, session.DecodeOptions{FromLine: from, MaxRecordBytes: opts.MaxRecordBytes}, x.Add)
	if streamErr != nil && !errors.Is(streamErr, evidence.ErrPacketFull) {
		return evidence.Packet{}, fmt.Errorf("extract session evidence: %w", streamErr)
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
		return evidence.Packet{}, fmt.Errorf("write evidence packet: %w", err)
	}
	return packet, nil
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
		{label: "data directory", path: &opts.DataDir},
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
	return nil
}

func findConfiguredProject(cfg config.Config, goos, cwd string) (config.ProjectMapping, error) {
	var match config.ProjectMapping
	matches := 0
	for _, project := range cfg.Projects {
		same, err := sameProjectDirectory(goos, project.Root, cwd)
		if err != nil {
			return config.ProjectMapping{}, fmt.Errorf("validate configured project mapping: %w", err)
		}
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
	if goos == "windows" || goos != runtime.GOOS {
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

func readCursor(dataDir, projectID, sessionID string) (cursor.Cursor, error) {
	root := filepath.Join(dataDir, "projects", projectID)
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return cursor.Cursor{}, nil
	} else if err != nil {
		return cursor.Cursor{}, err
	}
	return (cursor.Store{Root: root}).LoadReadOnly(sessionID)
}
