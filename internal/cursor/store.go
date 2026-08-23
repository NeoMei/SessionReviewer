package cursor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	Root         string
	ExpectedRoot os.FileInfo

	beforeRootValidation func() error
	writeRoot            func(*os.Root, string, []byte, fs.FileMode) error
	ensureRootDir        func(*os.Root, string, fs.FileMode) error
}

type storeRoot struct {
	data       *os.Root
	cursors    *os.Root
	cursorName string
	backupName string
	lockName   string
	dirMissing bool
}

type cursorLock struct {
	file *os.File
}

func (s Store) Load(sessionID string) (result Cursor, retErr error) {
	root, err := s.open(sessionID, false)
	if err != nil {
		return Cursor{}, err
	}
	defer func() { retErr = errors.Join(retErr, root.close()) }()
	if root.dirMissing {
		return Cursor{}, nil
	}
	lock, err := acquireCursorLock(root.cursors, root.lockName)
	if err != nil {
		return Cursor{}, err
	}
	defer func() { retErr = errors.Join(retErr, lock.release()) }()
	return root.loadLocked(sessionID)
}

// LoadReadOnly reads the best valid cursor state under the normal transaction
// lock but never repairs, replaces, or removes cursor state files.
func (s Store) LoadReadOnly(sessionID string) (result Cursor, retErr error) {
	root, err := s.open(sessionID, false)
	if err != nil {
		return Cursor{}, err
	}
	defer func() { retErr = errors.Join(retErr, root.close()) }()
	if root.dirMissing {
		return Cursor{}, nil
	}
	lock, err := acquireCursorLock(root.cursors, root.lockName)
	if err != nil {
		return Cursor{}, err
	}
	defer func() { retErr = errors.Join(retErr, lock.release()) }()
	return root.loadReadOnlyLocked(sessionID)
}

func (s Store) Commit(sessionID string, expected, next Cursor) (retErr error) {
	if err := validateCursor(expected, sessionID, true); err != nil {
		return fmt.Errorf("invalid expected cursor: %w", err)
	}
	expected.LastHash = strings.ToLower(expected.LastHash)
	root, err := s.open(sessionID, true)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, root.close()) }()
	lock, err := acquireCursorLock(root.cursors, root.lockName)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lock.release()) }()

	if err := rejectCaseCollision(root.cursors, root.cursorName, root.backupName, root.lockName); err != nil {
		return err
	}
	current, err := root.loadLocked(sessionID)
	if err != nil {
		return err
	}
	if !cursorEqual(current, expected) {
		return ErrStale
	}
	if err := validateCursor(next, sessionID, false); err != nil {
		return fmt.Errorf("invalid next cursor: %w", err)
	}
	next.LastHash = strings.ToLower(next.LastHash)
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
	writeRoot := s.writeRoot
	if writeRoot == nil {
		writeRoot = atomicfile.WriteRoot
	}
	if err := writeRoot(root.cursors, root.cursorName, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("persist cursor state: %w", err)
	}
	if _, found, err := regularEntry(root.cursors, root.cursorName, "cursor file"); err != nil || !found {
		if err != nil {
			return err
		}
		return fmt.Errorf("cursor file is missing after commit")
	}
	return nil
}

func (s Store) open(sessionID string, createDir bool) (*storeRoot, error) {
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	if s.beforeRootValidation != nil {
		if err := s.beforeRootValidation(); err != nil {
			return nil, err
		}
	}
	rootInfo, err := validateRootPath(s.Root)
	if err != nil {
		return nil, err
	}
	data, err := os.OpenRoot(s.Root)
	if err != nil {
		return nil, fmt.Errorf("open data root: %w", err)
	}
	openedRootInfo, err := data.Stat(".")
	if err != nil || !os.SameFile(rootInfo, openedRootInfo) {
		_ = data.Close()
		return nil, fmt.Errorf("data root changed while opening")
	}
	if s.ExpectedRoot != nil && !os.SameFile(s.ExpectedRoot, openedRootInfo) {
		_ = data.Close()
		return nil, fmt.Errorf("data root does not match expected root identity")
	}

	root := &storeRoot{
		data:       data,
		cursorName: sessionID + ".json",
		backupName: atomicfile.BackupPath(sessionID + ".json"),
		lockName:   "." + strings.ToLower(sessionID) + ".lock",
	}
	cursorInfo, err := data.Lstat("cursors")
	if errors.Is(err, os.ErrNotExist) && !createDir {
		root.dirMissing = true
		return root, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		ensureRootDir := s.ensureRootDir
		if ensureRootDir == nil {
			ensureRootDir = atomicfile.EnsureRootDir
		}
		if err := ensureRootDir(data, "cursors", 0o700); err != nil {
			_ = root.close()
			return nil, fmt.Errorf("create cursor directory: %w", err)
		}
		cursorInfo, err = data.Lstat("cursors")
	}
	if err != nil {
		_ = root.close()
		return nil, fmt.Errorf("inspect cursor directory: %w", err)
	}
	if isSymlinkOrReparse(cursorInfo) || !cursorInfo.IsDir() {
		_ = root.close()
		return nil, fmt.Errorf("cursor directory is a symlink, reparse point, or non-directory")
	}
	cursors, err := data.OpenRoot("cursors")
	if err != nil {
		_ = root.close()
		return nil, fmt.Errorf("open cursor directory: %w", err)
	}
	root.cursors = cursors
	openedCursorInfo, err := cursors.Stat(".")
	if err != nil || !os.SameFile(cursorInfo, openedCursorInfo) {
		_ = root.close()
		return nil, fmt.Errorf("cursor directory changed while opening")
	}
	if createDir {
		if err := protectCursorDirectory(cursors); err != nil {
			_ = root.close()
			return nil, err
		}
	}
	if err := rejectCaseCollision(cursors, root.cursorName, root.backupName, root.lockName); err != nil {
		_ = root.close()
		return nil, err
	}
	for name, label := range map[string]string{
		root.cursorName: "cursor file",
		root.backupName: "cursor backup",
		root.lockName:   "cursor lock",
	} {
		if _, _, err := regularEntry(cursors, name, label); err != nil {
			_ = root.close()
			return nil, err
		}
	}
	return root, nil
}

func protectCursorDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open cursor directory for protection: %w", err)
	}
	chmodErr := directory.Chmod(0o700)
	closeErr := directory.Close()
	if err := errors.Join(chmodErr, closeErr); err != nil {
		return fmt.Errorf("protect cursor directory: %w", err)
	}
	return nil
}

func (root *storeRoot) close() error {
	if root == nil {
		return nil
	}
	var err error
	if root.cursors != nil {
		err = errors.Join(err, root.cursors.Close())
	}
	if root.data != nil {
		err = errors.Join(err, root.data.Close())
	}
	return err
}

func (root *storeRoot) loadLocked(sessionID string) (Cursor, error) {
	destinationInfo, destinationFound, err := regularEntry(root.cursors, root.cursorName, "cursor file")
	if err != nil {
		return Cursor{}, err
	}
	backupInfo, backupFound, err := regularEntry(root.cursors, root.backupName, "cursor backup")
	if err != nil {
		return Cursor{}, err
	}
	if !destinationFound && !backupFound {
		return Cursor{}, nil
	}

	var destination Cursor
	var destinationErr error
	if destinationFound {
		destination, destinationErr = readCursor(root.cursors, root.cursorName, sessionID, destinationInfo)
	}
	var backup Cursor
	var backupErr error
	if backupFound {
		backup, backupErr = readCursor(root.cursors, root.backupName, sessionID, backupInfo)
	}

	if destinationFound && destinationErr == nil {
		if backupFound {
			if err := atomicfile.RemoveRoot(root.cursors, root.backupName); err != nil {
				return Cursor{}, fmt.Errorf("remove stale cursor backup: %w", err)
			}
		}
		return destination, nil
	}
	if backupFound && backupErr == nil {
		if destinationFound {
			if err := atomicfile.RemoveRoot(root.cursors, root.cursorName); err != nil {
				return Cursor{}, fmt.Errorf("remove corrupt replacement cursor: %w", err)
			}
		}
		if err := atomicfile.RenameRoot(root.cursors, root.backupName, root.cursorName); err != nil {
			return Cursor{}, fmt.Errorf("restore cursor backup: %w", err)
		}
		return backup, nil
	}
	return Cursor{}, fmt.Errorf("cursor state and recovery backup are corrupt")
}

func (root *storeRoot) loadReadOnlyLocked(sessionID string) (Cursor, error) {
	destinationInfo, destinationFound, err := regularEntry(root.cursors, root.cursorName, "cursor file")
	if err != nil {
		return Cursor{}, err
	}
	backupInfo, backupFound, err := regularEntry(root.cursors, root.backupName, "cursor backup")
	if err != nil {
		return Cursor{}, err
	}
	if !destinationFound && !backupFound {
		return Cursor{}, nil
	}
	if destinationFound {
		if destination, err := readCursor(root.cursors, root.cursorName, sessionID, destinationInfo); err == nil {
			return destination, nil
		}
	}
	if backupFound {
		if backup, err := readCursor(root.cursors, root.backupName, sessionID, backupInfo); err == nil {
			return backup, nil
		}
	}
	return Cursor{}, fmt.Errorf("cursor state and recovery backup are corrupt")
}

func validateSessionID(sessionID string) error {
	if sessionID == "" || sessionID == "." || sessionID == ".." || !safeID.MatchString(sessionID) || windowsReservedName(sessionID) {
		return fmt.Errorf("invalid session id")
	}
	return nil
}

func windowsReservedName(name string) bool {
	name = strings.TrimRight(name, " .")
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		name = name[:dot]
	}
	name = strings.ToUpper(name)
	switch name {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(name) == 4 && (strings.HasPrefix(name, "COM") || strings.HasPrefix(name, "LPT")) && name[3] >= '1' && name[3] <= '9'
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

func validateRootPath(path string) (os.FileInfo, error) {
	if path == "" {
		return nil, fmt.Errorf("data root is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect data root: %w", err)
	}
	if isSymlinkOrReparse(info) || !info.IsDir() {
		return nil, fmt.Errorf("data root is a symlink, reparse point, or non-directory")
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("inspect data root: %w", err)
	}
	physicalParent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("inspect data root parent: %w", err)
	}
	expected := filepath.Join(physicalParent, filepath.Base(path))
	if !samePath(physical, expected) {
		return nil, fmt.Errorf("data root is a symlink or reparse point")
	}
	return info, nil
}

func samePath(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(first, second)
	}
	return first == second
}

func regularEntry(root *os.Root, name, label string) (os.FileInfo, bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect %s: %w", label, err)
	}
	if isSymlinkOrReparse(info) || !info.Mode().IsRegular() {
		return nil, true, fmt.Errorf("%s is a symlink, reparse point, or non-regular file", label)
	}
	return info, true, nil
}

func rejectCaseCollision(root *os.Root, allowed ...string) error {
	entries, err := fs.ReadDir(root.FS(), ".")
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

func readCursor(root *os.Root, name, sessionID string, before os.FileInfo) (Cursor, error) {
	if before.Size() > maxCursorBytes {
		return Cursor{}, fmt.Errorf("cursor state is corrupt")
	}
	file, err := root.Open(name)
	if err != nil {
		return Cursor{}, fmt.Errorf("open cursor state: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return Cursor{}, fmt.Errorf("inspect open cursor state: %w", err)
	}
	if !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		return Cursor{}, fmt.Errorf("cursor file changed while opening")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxCursorBytes+1))
	decoder.DisallowUnknownFields()
	var current Cursor
	if err := decoder.Decode(&current); err != nil {
		return Cursor{}, fmt.Errorf("cursor state is corrupt")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Cursor{}, fmt.Errorf("cursor state is corrupt")
	}
	after, found, err := regularEntry(root, name, "cursor file")
	if err != nil || !found || !os.SameFile(opened, after) {
		return Cursor{}, fmt.Errorf("cursor file changed while reading")
	}
	if err := validateCursor(current, sessionID, false); err != nil {
		return Cursor{}, fmt.Errorf("cursor state is invalid: %w", err)
	}
	current.LastHash = strings.ToLower(current.LastHash)
	return current, nil
}

func acquireCursorLock(root *os.Root, name string) (*cursorLock, error) {
	deadline := time.Now().Add(lockTimeout)
	for {
		file, err := openStableLockFile(root, name)
		if err != nil {
			return nil, err
		}
		locked, err := tryPlatformLock(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("acquire cursor lock: %w", err)
		}
		if locked {
			return &cursorLock{file: file}, nil
		}
		_ = file.Close()
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("cursor transaction remains locked by a live owner")
		}
		time.Sleep(lockPollInterval)
	}
}

func openStableLockFile(root *os.Root, name string) (*os.File, error) {
	for {
		before, found, err := regularEntry(root, name, "cursor lock")
		if err != nil {
			return nil, err
		}
		flags := os.O_RDWR
		if !found {
			flags |= os.O_CREATE | os.O_EXCL
		}
		file, err := root.OpenFile(name, flags, 0o600)
		if errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("open cursor lock: %w", err)
		}
		opened, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("inspect open cursor lock: %w", err)
		}
		after, afterFound, err := regularEntry(root, name, "cursor lock")
		if err != nil || !afterFound || !os.SameFile(opened, after) || (found && !os.SameFile(before, opened)) {
			_ = file.Close()
			if err != nil {
				return nil, err
			}
			continue
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("protect cursor lock: %w", err)
		}
		return file, nil
	}
}

func (lock *cursorLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := unlockPlatformLock(lock.file)
	closeErr := lock.file.Close()
	return errors.Join(unlockErr, closeErr)
}
