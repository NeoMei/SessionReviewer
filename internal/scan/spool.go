package scan

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

const (
	observationSpoolNamespace = "scan-spool"
	maxObservationSpoolBytes  = int64(64 << 20)
	maxObservationSpoolLine   = 2 << 20
)

type observationSpoolStats struct {
	ResidentSources int
}

type observationSpools struct {
	project  *pathguard.Directory
	run      *pathguard.Directory
	runLeaf  string
	maxBytes int64
	observer func(observationSpoolStats)

	mu       sync.Mutex
	resident int
	spools   map[string]*observationSpool
	closed   bool
}

type observationSpool struct {
	owner      *observationSpools
	provider   string
	sessionID  string
	leaf       string
	file       *os.File
	count      int
	bytes      int64
	sealed     bool
	appendErr  error
	closeOnce  sync.Once
	closeError error
}

func openObservationSpools(ctx context.Context, dataRoot, projectID string, observer func(observationSpoolStats)) (*observationSpools, error) {
	if ctx == nil {
		return nil, errors.New("observation spool context is required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if !scanIDPattern.MatchString(projectID) {
		return nil, errors.New("invalid observation spool project identity")
	}
	data, err := pathguard.Open(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("open observation spool data root: %w", err)
	}
	defer data.Close()
	relative := filepath.ToSlash(filepath.Join(observationSpoolNamespace, projectID))
	if err := data.EnsureDirectoryChecked(relative, 0o700, func() error { return context.Cause(ctx) }); err != nil {
		return nil, fmt.Errorf("create observation spool namespace: %w", err)
	}
	projectPath := filepath.Join(data.Path, filepath.FromSlash(relative))
	project, err := pathguard.Open(projectPath)
	if err != nil {
		return nil, fmt.Errorf("pin observation spool namespace: %w", err)
	}
	closeProject := true
	defer func() {
		if closeProject {
			_ = project.Close()
		}
	}()
	if err := cleanupStaleObservationRuns(ctx, project); err != nil {
		return nil, err
	}
	runLeaf, err := createObservationRun(ctx, project)
	if err != nil {
		return nil, err
	}
	run, err := pathguard.Open(filepath.Join(project.Path, runLeaf))
	if err != nil {
		_ = project.Root.Remove(runLeaf)
		return nil, fmt.Errorf("pin observation spool run: %w", err)
	}
	closeProject = false
	return &observationSpools{
		project: project, run: run, runLeaf: runLeaf, maxBytes: maxObservationSpoolBytes,
		observer: observer, spools: make(map[string]*observationSpool),
	}, nil
}

func createObservationRun(ctx context.Context, project *pathguard.Directory) (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		if err := context.Cause(ctx); err != nil {
			return "", err
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("create observation spool identity: %w", err)
		}
		leaf := "run-" + hex.EncodeToString(random[:])
		if err := project.Root.Mkdir(leaf, 0o700); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", fmt.Errorf("create observation spool run: %w", err)
		}
		info, err := project.Root.Lstat(leaf)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = project.Root.Remove(leaf)
			return "", errors.New("observation spool run is not a private directory")
		}
		root, err := project.Root.OpenRoot(leaf)
		if err != nil {
			_ = project.Root.Remove(leaf)
			return "", fmt.Errorf("open observation spool run: %w", err)
		}
		file, chmodErr := root.Open(".")
		if chmodErr == nil {
			chmodErr = file.Chmod(0o700)
			chmodErr = errors.Join(chmodErr, file.Close())
		}
		opened, statErr := root.Stat(".")
		closeErr := root.Close()
		if err := errors.Join(chmodErr, statErr, closeErr); err != nil || !os.SameFile(info, opened) {
			_ = project.Root.Remove(leaf)
			return "", errors.Join(errors.New("protect observation spool run"), err)
		}
		return leaf, nil
	}
	return "", errors.New("observation spool run identity collision")
}

func cleanupStaleObservationRuns(ctx context.Context, project *pathguard.Directory) error {
	directory, err := project.Root.Open(".")
	if err != nil {
		return fmt.Errorf("open observation spool namespace: %w", err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return fmt.Errorf("read observation spool namespace: %w", err)
	}
	for _, entry := range entries {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if !strings.HasPrefix(entry.Name(), "run-") {
			return fmt.Errorf("unexpected entry in observation spool namespace: %s", entry.Name())
		}
		if err := removeObservationRun(ctx, project, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func removeObservationRun(ctx context.Context, project *pathguard.Directory, leaf string) error {
	before, err := project.Root.Lstat(leaf)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return errors.New("observation spool run was redirected")
	}
	run, err := project.Root.OpenRoot(leaf)
	if err != nil {
		return fmt.Errorf("open stale observation spool: %w", err)
	}
	opened, err := run.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		_ = run.Close()
		return errors.New("observation spool run changed while opening")
	}
	directory, err := run.Open(".")
	if err != nil {
		_ = run.Close()
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		_ = run.Close()
		return err
	}
	for _, entry := range entries {
		if err := context.Cause(ctx); err != nil {
			_ = run.Close()
			return err
		}
		name := entry.Name()
		info, err := run.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !strings.HasPrefix(name, "source-") || !strings.HasSuffix(name, ".jsonl") {
			_ = run.Close()
			return errors.New("observation spool contains an unexpected entry")
		}
		if err := run.Remove(name); err != nil {
			_ = run.Close()
			return fmt.Errorf("remove stale observation spool file: %w", err)
		}
	}
	if err := run.Close(); err != nil {
		return err
	}
	after, err := project.Root.Lstat(leaf)
	if err != nil || !os.SameFile(before, after) || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 {
		return errors.New("observation spool run changed before cleanup")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := project.Root.Remove(leaf); err != nil {
		return fmt.Errorf("remove stale observation spool run: %w", err)
	}
	return nil
}

func (spools *observationSpools) create(ctx context.Context, provider, sessionID string) (*observationSpool, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if spools == nil {
		return nil, errors.New("observation spool set is closed")
	}
	spools.mu.Lock()
	defer spools.mu.Unlock()
	if spools.closed || spools.run == nil {
		return nil, errors.New("observation spool set is closed")
	}
	key := sourceKey(provider, sessionID)
	if _, exists := spools.spools[key]; exists {
		return nil, errors.New("duplicate observation spool source")
	}
	digest := sha256.Sum256([]byte(key))
	leaf := "source-" + hex.EncodeToString(digest[:]) + ".jsonl"
	file, err := spools.run.Root.OpenFile(leaf, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create observation spool file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = spools.run.Root.Remove(leaf)
		return nil, fmt.Errorf("protect observation spool file: %w", err)
	}
	spool := &observationSpool{owner: spools, provider: provider, sessionID: sessionID, leaf: leaf, file: file}
	spools.spools[key] = spool
	return spool, nil
}

func (spool *observationSpool) append(ctx context.Context, value memory.ObservationRevision) error {
	if spool == nil || spool.file == nil || spool.sealed {
		return errors.New("observation spool is not writable")
	}
	if spool.appendErr != nil {
		return spool.appendErr
	}
	if err := context.Cause(ctx); err != nil {
		spool.appendErr = err
		return err
	}
	if err := memory.ValidateObservationRevisionContext(ctx, value); err != nil {
		spool.appendErr = fmt.Errorf("validate spooled observation: %w", err)
		return spool.appendErr
	}
	if value.Key.Provider != spool.provider || value.Key.SessionID != spool.sessionID {
		spool.appendErr = errors.New("spooled observation belongs to a different source")
		return spool.appendErr
	}
	if spool.count >= maxSourceRevisions {
		spool.appendErr = ErrObservationBudget
		return spool.appendErr
	}
	var body bytes.Buffer
	if err := memory.WriteCanonicalJSONContext(ctx, &body, value); err != nil {
		spool.appendErr = fmt.Errorf("encode canonical spooled observation: %w", err)
		return spool.appendErr
	}
	body.WriteByte('\n')
	if int64(body.Len()) > spool.owner.maxBytes-spool.bytes {
		spool.appendErr = ErrObservationBudget
		return spool.appendErr
	}
	if _, err := spool.file.Write(body.Bytes()); err != nil {
		spool.appendErr = fmt.Errorf("write observation spool: %w", err)
		return spool.appendErr
	}
	spool.count++
	spool.bytes += int64(body.Len())
	return nil
}

func (spool *observationSpool) seal(ctx context.Context) error {
	if spool == nil || spool.file == nil {
		return errors.New("observation spool is unavailable")
	}
	if spool.sealed {
		return spool.appendErr
	}
	if spool.appendErr != nil {
		_ = spool.file.Close()
		spool.sealed = true
		return spool.appendErr
	}
	if err := context.Cause(ctx); err != nil {
		_ = spool.file.Close()
		spool.sealed = true
		return err
	}
	err := errors.Join(spool.file.Sync(), spool.file.Chmod(0o600), spool.file.Close())
	spool.sealed = true
	spool.file = nil
	if err != nil {
		return fmt.Errorf("seal observation spool: %w", err)
	}
	return nil
}

func (spool *observationSpool) replay(ctx context.Context, visit func(memory.ObservationRevision) error) error {
	if spool == nil || !spool.sealed || spool.appendErr != nil || visit == nil {
		return errors.New("observation spool is not replayable")
	}
	spool.owner.enterResident()
	defer spool.owner.leaveResident()
	file, before, err := spool.owner.run.OpenRegular(spool.leaf)
	if err != nil {
		return fmt.Errorf("open observation spool replay: %w", err)
	}
	defer file.Close()
	if runtime.GOOS != "windows" && before.Mode().Perm() != 0o600 {
		return errors.New("observation spool file permissions changed")
	}
	scanner := bufio.NewScanner(&contextReader{ctx: ctx, reader: file})
	scanner.Buffer(make([]byte, 64<<10), maxObservationSpoolLine)
	count := 0
	var readBytes int64
	for scanner.Scan() {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		line := bytes.Clone(scanner.Bytes())
		readBytes += int64(len(line) + 1)
		var value memory.ObservationRevision
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("decode spooled observation: %w", err)
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			return errors.New("spooled observation has trailing JSON")
		}
		if err := memory.ValidateObservationRevisionContext(ctx, value); err != nil {
			return fmt.Errorf("validate replayed observation: %w", err)
		}
		if value.Key.Provider != spool.provider || value.Key.SessionID != spool.sessionID {
			return errors.New("replayed observation belongs to a different source")
		}
		var canonical bytes.Buffer
		if err := memory.WriteCanonicalJSONContext(ctx, &canonical, value); err != nil || !bytes.Equal(canonical.Bytes(), line) {
			return errors.Join(errors.New("observation spool is not canonical"), err)
		}
		if err := visit(value); err != nil {
			return err
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan observation spool: %w", err)
	}
	after, err := spool.owner.run.Root.Lstat(spool.leaf)
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode()&os.ModeSymlink != 0 ||
		after.Mode().Perm() != before.Mode().Perm() || after.Size() != spool.bytes || !after.ModTime().Equal(before.ModTime()) {
		return errors.New("observation spool changed during replay")
	}
	if count != spool.count || readBytes != spool.bytes {
		return errors.New("observation spool count or size changed")
	}
	return context.Cause(ctx)
}

func (spools *observationSpools) enterResident() {
	spools.mu.Lock()
	spools.resident++
	stats := observationSpoolStats{ResidentSources: spools.resident}
	observer := spools.observer
	spools.mu.Unlock()
	if observer != nil {
		observer(stats)
	}
}

func (spools *observationSpools) leaveResident() {
	spools.mu.Lock()
	spools.resident--
	stats := observationSpoolStats{ResidentSources: spools.resident}
	observer := spools.observer
	spools.mu.Unlock()
	if observer != nil {
		observer(stats)
	}
}

func (spools *observationSpools) close() error {
	if spools == nil {
		return nil
	}
	spools.mu.Lock()
	if spools.closed {
		spools.mu.Unlock()
		return nil
	}
	spools.closed = true
	spools.mu.Unlock()
	var errs []error
	for _, spool := range spools.spools {
		if spool.file != nil {
			errs = append(errs, spool.file.Close())
			spool.file = nil
		}
	}
	if spools.run != nil {
		for _, spool := range spools.spools {
			if err := spools.run.Root.Remove(spool.leaf); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
		errs = append(errs, spools.run.Close())
	}
	if spools.project != nil {
		if err := spools.project.Root.Remove(spools.runLeaf); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
		errs = append(errs, spools.project.Close())
	}
	return errors.Join(errs...)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := context.Cause(reader.ctx); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
