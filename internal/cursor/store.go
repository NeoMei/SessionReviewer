package cursor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
)

var ErrStale = errors.New("stale cursor")

var (
	safeID   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	validSHA = regexp.MustCompile(`^[A-Fa-f0-9]{64}$`)
)

const (
	lockPollInterval = 10 * time.Millisecond
	lockTimeout      = 2 * time.Second
	maxCursorBytes   = 64 << 10
)

type Cursor struct {
	SessionID string    `json:"session_id"`
	LastLine  int       `json:"last_line"`
	LastHash  string    `json:"last_hash"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store struct {
	Root string
}

type storePaths struct {
	root       string
	dir        string
	cursor     string
	lock       string
	rootInfo   os.FileInfo
	dirInfo    os.FileInfo
	dirMissing bool
}

type cursorLock struct {
	path string
	file *os.File
}

func (s Store) Load(sessionID string) (Cursor, error) {
	paths, err := s.inspectPaths(sessionID, false)
	if err != nil {
		return Cursor{}, err
	}
	if paths.dirMissing {
		return Cursor{}, nil
	}

	current, _, err := readCursor(paths.cursor, sessionID)
	if err != nil {
		return Cursor{}, err
	}
	if err := paths.verifyDirectories(); err != nil {
		return Cursor{}, err
	}
	return current, nil
}

func (s Store) Commit(sessionID string, expected, next Cursor) (retErr error) {
	if err := validateCursor(expected, sessionID, true); err != nil {
		return fmt.Errorf("invalid expected cursor: %w", err)
	}

	paths, err := s.inspectPaths(sessionID, true)
	if err != nil {
		return err
	}
	lock, err := acquireCursorLock(paths.lock)
	if err != nil {
		return err
	}
	defer func() {
		retErr = errors.Join(retErr, lock.release())
	}()

	if err := paths.verifyDirectories(); err != nil {
		return err
	}
	if err := rejectCaseCollision(paths.dir, filepath.Base(paths.cursor), filepath.Base(paths.lock)); err != nil {
		return err
	}
	current, currentInfo, err := readCursor(paths.cursor, sessionID)
	if err != nil {
		return err
	}
	if !cursorEqual(current, expected) {
		return ErrStale
	}
	if err := validateCursor(next, sessionID, false); err != nil {
		return fmt.Errorf("invalid next cursor: %w", err)
	}
	if next.LastLine < current.LastLine {
		return fmt.Errorf("invalid next cursor: last line decreases")
	}
	if next.LastLine == current.LastLine && current.LastLine > 0 && next.LastHash != current.LastHash {
		return fmt.Errorf("invalid next cursor: hash changes at the same line")
	}
	if !current.UpdatedAt.IsZero() && (next.UpdatedAt.IsZero() || next.UpdatedAt.Before(current.UpdatedAt)) {
		return fmt.Errorf("invalid next cursor: timestamp decreases")
	}

	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cursor state: %w", err)
	}
	if err := paths.verifyTarget(currentInfo); err != nil {
		return err
	}
	if err := atomicfile.Write(paths.cursor, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("persist cursor state: %w", err)
	}
	if err := paths.verifyWrittenTarget(); err != nil {
		return err
	}
	return nil
}

func (s Store) inspectPaths(sessionID string, createDir bool) (storePaths, error) {
	if err := validateSessionID(sessionID); err != nil {
		return storePaths{}, err
	}
	rootInfo, err := validateDirectory(s.Root, "data root")
	if err != nil {
		return storePaths{}, err
	}

	paths := storePaths{
		root:     s.Root,
		dir:      filepath.Join(s.Root, "cursors"),
		cursor:   filepath.Join(s.Root, "cursors", sessionID+".json"),
		lock:     filepath.Join(s.Root, "cursors", "."+strings.ToLower(sessionID)+".lock"),
		rootInfo: rootInfo,
	}
	dirInfo, err := validateDirectory(paths.dir, "cursor directory")
	if errors.Is(err, os.ErrNotExist) && !createDir {
		paths.dirMissing = true
		return paths, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(paths.dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return storePaths{}, fmt.Errorf("create cursor directory: %w", err)
		}
		dirInfo, err = validateDirectory(paths.dir, "cursor directory")
	}
	if err != nil {
		return storePaths{}, err
	}
	if createDir {
		if err := os.Chmod(paths.dir, 0o700); err != nil {
			return storePaths{}, fmt.Errorf("protect cursor directory: %w", err)
		}
	}
	paths.dirInfo = dirInfo
	if err := paths.verifyDirectories(); err != nil {
		return storePaths{}, err
	}
	if err := rejectCaseCollision(paths.dir, filepath.Base(paths.cursor), filepath.Base(paths.lock)); err != nil {
		return storePaths{}, err
	}
	if err := validateOptionalRegular(paths.cursor, "cursor file"); err != nil {
		return storePaths{}, err
	}
	if err := validateOptionalRegular(paths.lock, "cursor lock"); err != nil {
		return storePaths{}, err
	}
	return paths, nil
}

func validateSessionID(sessionID string) error {
	if sessionID == "" || sessionID == "." || sessionID == ".." || !safeID.MatchString(sessionID) {
		return fmt.Errorf("invalid session id")
	}
	return nil
}

func validateCursor(cursor Cursor, sessionID string, allowMissing bool) error {
	if cursor == (Cursor{}) && allowMissing {
		return nil
	}
	if cursor.SessionID != sessionID {
		return fmt.Errorf("session id does not match")
	}
	if cursor.LastLine < 0 {
		return fmt.Errorf("last line is negative")
	}
	if cursor.LastLine == 0 && cursor.LastHash != "" {
		return fmt.Errorf("zero line has a hash")
	}
	if cursor.LastLine > 0 && !validSHA.MatchString(cursor.LastHash) {
		return fmt.Errorf("last hash is not a SHA-256 digest")
	}
	return nil
}

func cursorEqual(first, second Cursor) bool {
	return first.SessionID == second.SessionID &&
		first.LastLine == second.LastLine &&
		first.LastHash == second.LastHash &&
		first.UpdatedAt.Equal(second.UpdatedAt)
}

func validateDirectory(path, label string) (os.FileInfo, error) {
	if path == "" {
		return nil, fmt.Errorf("%s is required", label)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink or reparse point", label)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", label)
	}
	if redirected, err := pathIsRedirected(path); err != nil {
		return nil, fmt.Errorf("inspect %s: %w", label, err)
	} else if redirected {
		return nil, fmt.Errorf("%s is a symlink or reparse point", label)
	}
	return info, nil
}

func validateOptionalRegular(path, label string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink or reparse point", label)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", label)
	}
	if redirected, err := pathIsRedirected(path); err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	} else if redirected {
		return fmt.Errorf("%s is a symlink or reparse point", label)
	}
	return nil
}

func pathIsRedirected(path string) (bool, error) {
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, err
	}
	physicalParent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	expected := filepath.Join(physicalParent, filepath.Base(path))
	return !samePath(physical, expected), nil
}

func samePath(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(first, second)
	}
	return first == second
}

func rejectCaseCollision(dir string, allowed ...string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("inspect cursor directory: %w", err)
	}
	for _, entry := range entries {
		for _, name := range allowed {
			if strings.EqualFold(entry.Name(), name) && entry.Name() != name {
				return fmt.Errorf("cursor session id collides by case")
			}
		}
	}
	return nil
}

func readCursor(path, sessionID string) (Cursor, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Cursor{}, nil, nil
	}
	if err != nil {
		return Cursor{}, nil, fmt.Errorf("inspect cursor file: %w", err)
	}
	if err := validateOptionalRegular(path, "cursor file"); err != nil {
		return Cursor{}, nil, err
	}
	if before.Size() > maxCursorBytes {
		return Cursor{}, nil, fmt.Errorf("cursor state is corrupt")
	}

	file, err := os.Open(path)
	if err != nil {
		return Cursor{}, nil, fmt.Errorf("open cursor state: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return Cursor{}, nil, fmt.Errorf("inspect open cursor state: %w", err)
	}
	if !os.SameFile(before, opened) {
		return Cursor{}, nil, fmt.Errorf("cursor file changed while opening")
	}

	decoder := json.NewDecoder(io.LimitReader(file, maxCursorBytes+1))
	decoder.DisallowUnknownFields()
	var current Cursor
	if err := decoder.Decode(&current); err != nil {
		return Cursor{}, nil, fmt.Errorf("cursor state is corrupt")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Cursor{}, nil, fmt.Errorf("cursor state is corrupt")
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, after) {
		return Cursor{}, nil, fmt.Errorf("cursor file changed while reading")
	}
	if err := validateCursor(current, sessionID, false); err != nil {
		return Cursor{}, nil, fmt.Errorf("cursor state is invalid: %w", err)
	}
	return current, opened, nil
}

func (paths storePaths) verifyDirectories() error {
	rootInfo, err := validateDirectory(paths.root, "data root")
	if err != nil {
		return err
	}
	if !os.SameFile(paths.rootInfo, rootInfo) {
		return fmt.Errorf("data root changed during cursor operation")
	}
	if paths.dirMissing {
		return nil
	}
	dirInfo, err := validateDirectory(paths.dir, "cursor directory")
	if err != nil {
		return err
	}
	if !os.SameFile(paths.dirInfo, dirInfo) {
		return fmt.Errorf("cursor directory changed during cursor operation")
	}
	return nil
}

func (paths storePaths) verifyTarget(previous os.FileInfo) error {
	if err := paths.verifyDirectories(); err != nil {
		return err
	}
	current, err := os.Lstat(paths.cursor)
	if previous == nil && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cursor file changed before commit")
	}
	if previous == nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(previous, current) {
		return fmt.Errorf("cursor file changed before commit")
	}
	return nil
}

func (paths storePaths) verifyWrittenTarget() error {
	if err := paths.verifyDirectories(); err != nil {
		return err
	}
	return validateOptionalRegular(paths.cursor, "cursor file")
}

func acquireCursorLock(path string) (*cursorLock, error) {
	deadline := time.Now().Add(lockTimeout)
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, err := fmt.Fprintf(file, "pid=%d\ncreated=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("initialize cursor lock: %w", err)
			}
			if err := file.Sync(); err != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("sync cursor lock: %w", err)
			}
			return &cursorLock{path: path, file: file}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire cursor lock: %w", err)
		}
		if err := validateOptionalRegular(path, "cursor lock"); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("cursor lock remains held; refusing automatic stale or crashed lock recovery")
		}
		time.Sleep(lockPollInterval)
	}
}

func (lock *cursorLock) release() error {
	owned, err := lock.file.Stat()
	if err != nil {
		_ = lock.file.Close()
		return fmt.Errorf("inspect owned cursor lock: %w", err)
	}
	current, err := os.Lstat(lock.path)
	if err != nil {
		_ = lock.file.Close()
		return fmt.Errorf("inspect cursor lock before release: %w", err)
	}
	if current.Mode()&os.ModeSymlink != 0 || !os.SameFile(owned, current) {
		_ = lock.file.Close()
		return fmt.Errorf("cursor lock ownership changed; refusing to remove it")
	}
	if err := lock.file.Close(); err != nil {
		return fmt.Errorf("close cursor lock: %w", err)
	}
	if err := os.Remove(lock.path); err != nil {
		return fmt.Errorf("remove cursor lock: %w", err)
	}
	return nil
}
