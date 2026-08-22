package cursor

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
)

// LoadReadOnlyRoot reads cursor state below an already pinned machine data
// root. It opens only existing directories and files and never creates or
// repairs cursor state.
func LoadReadOnlyRoot(dataRoot *os.Root, projectID, sessionID string) (result Cursor, retErr error) {
	if dataRoot == nil {
		return Cursor{}, fmt.Errorf("data root is required")
	}
	if err := validateSessionID(projectID); err != nil {
		return Cursor{}, fmt.Errorf("invalid project id")
	}
	if err := validateSessionID(sessionID); err != nil {
		return Cursor{}, err
	}

	projects, found, err := openExistingDirectory(dataRoot, "projects", "projects directory")
	if err != nil || !found {
		return Cursor{}, err
	}
	defer func() { retErr = errors.Join(retErr, projects.Close()) }()
	project, found, err := openExistingDirectory(projects, projectID, "project data directory")
	if err != nil || !found {
		return Cursor{}, err
	}
	defer func() { retErr = errors.Join(retErr, project.Close()) }()
	cursors, found, err := openExistingDirectory(project, "cursors", "cursor directory")
	if err != nil || !found {
		return Cursor{}, err
	}
	defer func() { retErr = errors.Join(retErr, cursors.Close()) }()

	cursorName := sessionID + ".json"
	backupName := atomicfile.BackupPath(cursorName)
	lockName := "." + strings.ToLower(sessionID) + ".lock"
	if err := rejectCaseCollision(cursors, cursorName, backupName, lockName); err != nil {
		return Cursor{}, err
	}
	root := &storeRoot{cursors: cursors, cursorName: cursorName, backupName: backupName, lockName: lockName}
	return root.loadReadOnlyLocked(sessionID)
}

func openExistingDirectory(parent *os.Root, name, label string) (*os.Root, bool, error) {
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect %s: %w", label, err)
	}
	if isSymlinkOrReparse(info) || !info.IsDir() {
		return nil, true, fmt.Errorf("%s is a symlink, reparse point, or non-directory", label)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, true, fmt.Errorf("open %s: %w", label, err)
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = child.Close()
		return nil, true, fmt.Errorf("%s changed while opening", label)
	}
	return child, true, nil
}
