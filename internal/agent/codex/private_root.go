package codex

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

var errPrivateRootIdentityChanged = errors.New("private working root identity changed")

// PrepareWorkingDirectory converts one existing empty physical directory into
// the owner-only boundary required by GenerateProposal. The caller owns the
// directory lifecycle; this function only pins its identity while applying and
// validating the platform-specific protection (mode bits on Unix, a protected
// DACL on Windows).
func PrepareWorkingDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("private working root must be a canonical absolute path")
	}
	requested, err := os.Lstat(path)
	if err != nil || !requested.IsDir() || requested.Mode()&os.ModeSymlink != 0 {
		return errors.New("private working root is not a physical directory")
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(physical) {
		return errors.New("private working root cannot be resolved")
	}
	physical = filepath.Clean(physical)
	anchor, err := os.Open(physical)
	if err != nil {
		return err
	}
	defer anchor.Close()
	opened, err := anchor.Stat()
	visible, visibleErr := os.Stat(physical)
	if err != nil || visibleErr != nil || !os.SameFile(requested, opened) || !os.SameFile(opened, visible) {
		return errPrivateRootIdentityChanged
	}
	entries, readErr := anchor.ReadDir(1)
	if len(entries) != 0 || !errors.Is(readErr, io.EOF) {
		return errors.New("private working root must be empty")
	}
	if err := anchor.Chmod(0o700); err != nil {
		return err
	}
	if err := protectPrivateDirectory(physical, anchor); err != nil {
		return err
	}
	protected, err := anchor.Stat()
	visible, visibleErr = os.Stat(physical)
	if err != nil || visibleErr != nil || !os.SameFile(opened, protected) || !os.SameFile(protected, visible) {
		return errPrivateRootIdentityChanged
	}
	return validatePrivateDirectory(physical, anchor, protected)
}

// privateRoot pins a physical directory with os.Root. Every descendant
// operation is root-relative, so renaming or replacing an ancestor cannot
// redirect job creation, writes, or cleanup into an attacker-selected tree.
type privateRoot struct {
	path      string
	root      *os.Root
	anchor    *os.File
	info      os.FileInfo
	closeOnce sync.Once
	closeErr  error
}

type privateDirectory struct {
	parent       *privateRoot
	name         string
	root         *os.Root
	anchor       *os.File
	info         os.FileInfo
	physicalPath string
	commandPath  string
	cleanupOnce  sync.Once
	cleanupErr   error
}

func openPrivateRoot(path string) (*privateRoot, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("private working root must be absolute")
	}
	clean := filepath.Clean(path)
	requestedInfo, err := os.Lstat(clean)
	if err != nil || !requestedInfo.IsDir() || requestedInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("private working root is not a physical directory")
	}
	physical, err := filepath.EvalSymlinks(clean)
	if err != nil || !filepath.IsAbs(physical) {
		return nil, errors.New("private working root cannot be resolved")
	}
	physical = filepath.Clean(physical)
	root, err := os.OpenRoot(physical)
	if err != nil {
		return nil, err
	}
	anchor, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	info, err := anchor.Stat()
	pathInfo, pathErr := os.Stat(physical)
	if err != nil || pathErr != nil || !os.SameFile(requestedInfo, info) || !os.SameFile(info, pathInfo) {
		_ = anchor.Close()
		_ = root.Close()
		return nil, errPrivateRootIdentityChanged
	}
	if err := validatePrivateDirectory(physical, anchor, info); err != nil {
		_ = anchor.Close()
		_ = root.Close()
		return nil, err
	}
	entries, readErr := anchor.ReadDir(1)
	if len(entries) != 0 || !errors.Is(readErr, io.EOF) {
		_ = anchor.Close()
		_ = root.Close()
		return nil, errors.New("private working root must be empty")
	}
	return &privateRoot{path: physical, root: root, anchor: anchor, info: info}, nil
}

func createOwnedPrivateRoot(prefix string) (*privateRoot, error) {
	path, err := os.MkdirTemp("", prefix)
	if err != nil {
		return nil, err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err := prepareOwnedPrivateDirectory(path); err != nil {
		return nil, err
	}
	root, err := openPrivateRoot(path)
	if err != nil {
		return nil, err
	}
	remove = false
	return root, nil
}

func (root *privateRoot) createDirectory(prefix string) (*privateDirectory, error) {
	if root == nil || root.root == nil {
		return nil, errors.New("private working root is closed")
	}
	for attempt := 0; attempt < 8; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		name := prefix + hex.EncodeToString(random[:])
		if err := root.root.Mkdir(name, 0o700); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return nil, err
		}
		childRoot, err := root.root.OpenRoot(name)
		if err != nil {
			_ = root.root.Remove(name)
			return nil, err
		}
		anchor, err := childRoot.Open(".")
		if err != nil {
			_ = childRoot.Close()
			_ = root.root.Remove(name)
			return nil, err
		}
		physicalPath := filepath.Join(root.path, name)
		if err := protectPrivateDirectory(physicalPath, anchor); err != nil {
			_ = anchor.Close()
			_ = childRoot.Close()
			_ = root.root.Remove(name)
			return nil, err
		}
		info, statErr := anchor.Stat()
		parentInfo, parentErr := root.root.Lstat(name)
		if statErr != nil || parentErr != nil || !os.SameFile(info, parentInfo) {
			_ = anchor.Close()
			_ = childRoot.Close()
			_ = root.root.Remove(name)
			return nil, errPrivateRootIdentityChanged
		}
		if err := validatePrivateDirectory(physicalPath, anchor, info); err != nil {
			_ = anchor.Close()
			_ = childRoot.Close()
			_ = root.root.Remove(name)
			return nil, err
		}
		return &privateDirectory{
			parent:       root,
			name:         name,
			root:         childRoot,
			anchor:       anchor,
			info:         info,
			physicalPath: physicalPath,
		}, nil
	}
	return nil, errors.New("could not allocate a private run directory")
}

func (directory *privateDirectory) writePrivateFile(name string, data []byte) error {
	if directory == nil || directory.root == nil || filepath.Base(name) != name || name == "." {
		return errors.New("invalid private file name")
	}
	file, err := directory.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || written != len(data) || syncErr != nil || closeErr != nil {
		return errors.Join(writeErr, syncErr, closeErr, errors.New("write private file"))
	}
	return nil
}

func (directory *privateDirectory) configureCommandDirectory(command *exec.Cmd) error {
	if directory == nil || directory.anchor == nil {
		return errors.New("private run directory is closed")
	}
	path, err := configureCommandDirectory(command, directory.anchor, directory.physicalPath)
	if err != nil {
		return err
	}
	directory.commandPath = path
	return nil
}

func (directory *privateDirectory) recheckForStart() error {
	if directory == nil || directory.parent == nil || directory.anchor == nil {
		return errPrivateRootIdentityChanged
	}
	current, err := directory.anchor.Stat()
	parentInfo, parentErr := directory.parent.root.Lstat(directory.name)
	if err != nil || parentErr != nil || !os.SameFile(directory.info, current) || !os.SameFile(current, parentInfo) {
		return errPrivateRootIdentityChanged
	}
	if err := validatePrivateDirectory(directory.physicalPath, directory.anchor, current); err != nil {
		return err
	}
	path := directory.commandPath
	if path == "" {
		path = directory.physicalPath
	}
	return recheckVisiblePrivateDirectory(path, directory.info)
}

func (directory *privateDirectory) cleanup() error {
	if directory == nil {
		return nil
	}
	directory.cleanupOnce.Do(func() {
		directory.cleanupErr = directory.cleanupUncached()
	})
	return directory.cleanupErr
}

func (directory *privateDirectory) cleanupUncached() error {
	var result error
	if directory.root != nil {
		result = errors.Join(result, removeRootContents(directory.root))
	}
	if directory.anchor != nil {
		result = errors.Join(result, directory.anchor.Close())
		directory.anchor = nil
	}
	if directory.root != nil {
		result = errors.Join(result, directory.root.Close())
		directory.root = nil
	}
	if directory.parent == nil || directory.parent.root == nil {
		return errors.Join(result, errPrivateRootIdentityChanged)
	}
	current, err := directory.parent.root.Lstat(directory.name)
	if err != nil || !os.SameFile(directory.info, current) {
		return errors.Join(result, errPrivateRootIdentityChanged)
	}
	return errors.Join(result, directory.parent.root.Remove(directory.name))
}

func (root *privateRoot) close() error {
	if root == nil {
		return nil
	}
	root.closeOnce.Do(func() {
		if root.anchor != nil {
			root.closeErr = errors.Join(root.closeErr, root.anchor.Close())
			root.anchor = nil
		}
		if root.root != nil {
			root.closeErr = errors.Join(root.closeErr, root.root.Close())
			root.root = nil
		}
	})
	return root.closeErr
}

func (root *privateRoot) cleanupOwned() error {
	if root == nil {
		return nil
	}
	var result error
	if root.root != nil {
		result = errors.Join(result, removeRootContents(root.root))
	}
	path := root.path
	info := root.info
	result = errors.Join(result, root.close())
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return errors.Join(result, errPrivateRootIdentityChanged)
	}
	return errors.Join(result, os.Remove(path))
}

func removeRootContents(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	var result error
	for _, entry := range entries {
		result = errors.Join(result, root.RemoveAll(entry.Name()))
	}
	return errors.Join(readErr, closeErr, result)
}
