package atomicfile

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const backupSuffix = ".session-reviewer-backup"

// Checked writes snapshot an existing SessionReviewer document before publish.
// This matches syncdoc.MaxDocumentBytes, the application-wide document limit.
const maxRollbackSnapshotBytes = 4 << 20

var ErrRootRollbackRecoveryRequired = errors.New("atomic rollback recovery requires an expected old hash")

func BackupPath(path string) string {
	return path + backupSuffix
}

func Write(path string, data []byte, perm fs.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return WriteRoot(root, filepath.Base(path), data, perm)
}

func WriteRoot(root *os.Root, path string, data []byte, perm fs.FileMode) (retErr error) {
	if root == nil {
		return fmt.Errorf("atomic file root is required")
	}
	return writeRootWithParentOpener(root, path, data, perm, root.OpenRoot)
}

// WriteRootFile atomically writes one leaf below an already pinned immediate
// parent. Callers that must hold a directory identity across validation and
// publication use this instead of re-opening the parent by path.
func WriteRootFile(parent *os.Root, leaf string, data []byte, perm fs.FileMode) error {
	return WriteRootFileChecked(parent, leaf, data, perm, nil)
}

// WriteRootFileChecked applies checkpoint before temporary creation, after
// temporary durability but before publication, and after durable publication.
// A failed post-publication checkpoint removes only a still-identical file
// whose content matches this write.
func WriteRootFileChecked(parent *os.Root, leaf string, data []byte, perm fs.FileMode, checkpoint func() error) error {
	if parent == nil {
		return fmt.Errorf("atomic file root is required")
	}
	if !strictRootLeaf(leaf) {
		return fmt.Errorf("atomic file leaf is invalid")
	}
	return writeRootAtParentCheckedWithOps(parent, leaf, data, perm, checkpoint, defaultDurabilityOps())
}

func strictRootLeaf(leaf string) bool {
	return leaf != "" && leaf != "." && leaf != ".." &&
		!strings.ContainsAny(leaf, `/\:`) && !filepath.IsAbs(leaf) &&
		filepath.VolumeName(leaf) == "" && filepath.Base(leaf) == leaf
}

func writeRootWithParentOpener(root *os.Root, path string, data []byte, perm fs.FileMode, openParent func(string) (*os.Root, error)) error {
	if root == nil {
		return fmt.Errorf("atomic file root is required")
	}
	parent, err := openParent(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open destination parent: %w", err)
	}
	defer parent.Close()
	return writeRootAtParent(parent, filepath.Base(path), data, perm)
}

func writeRootAtParent(parent *os.Root, name string, data []byte, perm fs.FileMode) (retErr error) {
	return writeRootAtParentWithOps(parent, name, data, perm, defaultDurabilityOps())
}

type durabilityOps struct {
	syncTemporary         func(*os.File) error
	sanitizeTemporary     func(*os.File) error
	publish               func(*os.Root, string, string) error
	syncPublication       func(*os.Root, string) error
	linkRollback          func(*os.Root, string, string) error
	writeRecoverySnapshot func(*os.File, []byte) error
}

func defaultDurabilityOps() durabilityOps {
	return durabilityOps{
		syncTemporary:         (*os.File).Sync,
		sanitizeTemporary:     sanitizeRootTemporary,
		publish:               replaceRootFile,
		syncPublication:       syncRootPublication,
		linkRollback:          (*os.Root).Link,
		writeRecoverySnapshot: writeFullRootSnapshot,
	}
}

func writeRootAtParentWithOps(parent *os.Root, name string, data []byte, perm fs.FileMode, ops durabilityOps) (retErr error) {
	return writeRootAtParentCheckedWithOps(parent, name, data, perm, nil, ops)
}

func writeRootAtParentCheckedWithOps(parent *os.Root, name string, data []byte, perm fs.FileMode, checkpoint func() error, ops durabilityOps) (retErr error) {
	parentGuard, err := captureRootParentCleanupGuard(parent)
	if err != nil {
		return err
	}
	rollback := rootRollback{}
	if checkpoint != nil {
		rollback, err = captureRootRollback(parent, name)
		if err != nil {
			return err
		}
		defer func() { retErr = errors.Join(retErr, rollback.close()) }()
		if err := reconcileRootRollbackAlias(parent, name, parentGuard); err != nil {
			return err
		}
	}
	if checkpoint != nil {
		if err := checkpoint(); err != nil {
			return err
		}
	}
	tmp, tmpName, err := createRootTemp(parent)
	if err != nil {
		return err
	}
	createdInfo, err := tmp.Stat()
	if err != nil || !createdInfo.Mode().IsRegular() || isAtomicRedirect(createdInfo) {
		_ = tmp.Close()
		return errors.New("cannot verify created atomic temporary file")
	}
	prepublication := true
	sanitizeOnReturn := true
	defer func() {
		if retErr != nil && sanitizeOnReturn {
			retErr = errors.Join(retErr, parentGuard.run(parent, func() error {
				return sanitizeAndRemoveRootTemporary(parent, tmp, tmpName, createdInfo, ops.sanitizeTemporary)
			}))
		}
		retErr = errors.Join(retErr, tmp.Close())
	}()
	if err = tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = ops.syncTemporary(tmp); err != nil {
		return err
	}
	temporaryInfo, err := tmp.Stat()
	if err != nil || !os.SameFile(createdInfo, temporaryInfo) || !temporaryInfo.Mode().IsRegular() || isAtomicRedirect(temporaryInfo) {
		return errors.New("cannot verify atomic temporary file")
	}
	temporaryNameInfo, err := parent.Lstat(tmpName)
	if err != nil || !os.SameFile(temporaryInfo, temporaryNameInfo) || !temporaryNameInfo.Mode().IsRegular() || isAtomicRedirect(temporaryNameInfo) {
		return errors.New("atomic temporary file changed before publication")
	}
	if checkpoint != nil {
		prepared, prepareErr := prepareRootRollback(parent, name, rollback, ops)
		if prepareErr != nil {
			return prepareErr
		}
		rollback = prepared
	}
	rollbackFinalized := !rollback.active
	defer func() {
		if retErr != nil && prepublication && !rollbackFinalized {
			retErr = errors.Join(retErr, parentGuard.run(parent, func() error {
				if err := ensureRootRollbackContent(parent, name, &rollback, true, ops); err != nil {
					return err
				}
				return removeRootRollback(parent, &rollback)
			}))
		}
	}()
	if checkpoint != nil {
		if err = checkpoint(); err != nil {
			return err
		}
	}
	if rollback.active {
		if err := ensureRootRollbackContent(parent, name, &rollback, true, ops); err != nil {
			return err
		}
	}
	if err = ops.publish(parent, tmpName, name); err != nil {
		partial, inspectErr := parent.Lstat(name)
		var partialRollbackErr error
		if inspectErr == nil && os.SameFile(temporaryInfo, partial) && partial.Mode().IsRegular() && !isAtomicRedirect(partial) {
			prepublication = false
			partialRollbackErr = parentGuard.run(parent, func() error {
				return rollbackRootPublication(parent, name, partial, data, &rollback, ops)
			})
			rollbackFinalized = partialRollbackErr == nil
		} else if inspectErr != nil && !errors.Is(inspectErr, os.ErrNotExist) {
			partialRollbackErr = errors.New("cannot inspect destination after failed atomic publication")
		}
		return errors.Join(err, partialRollbackErr)
	}
	prepublication = false
	sanitizeOnReturn = false
	publishedInfo, err := parent.Lstat(name)
	if err != nil || !os.SameFile(temporaryInfo, publishedInfo) || !publishedInfo.Mode().IsRegular() || isAtomicRedirect(publishedInfo) {
		sanitizeOnReturn = true
		return errors.New("atomic publication identity changed")
	}
	if err = ops.syncPublication(parent, name); err != nil {
		return fmt.Errorf("sync published file metadata: %w", err)
	}
	if checkpoint != nil {
		checkpointErr := checkpoint()
		ownershipErr := verifyPublishedRootFileOwned(parent, name, publishedInfo, data)
		if checkpointErr != nil || ownershipErr != nil {
			rollbackErr := parentGuard.run(parent, func() error {
				return rollbackRootPublication(parent, name, publishedInfo, data, &rollback, ops)
			})
			rollbackFinalized = rollbackErr == nil
			sanitizeOnReturn = rollbackErr != nil
			return errors.Join(checkpointErr, ownershipErr, rollbackErr)
		}
	}
	if rollback.active {
		if err := parentGuard.run(parent, func() error { return removeRootRollback(parent, &rollback) }); err != nil {
			return err
		}
		rollbackFinalized = true
	}
	return nil
}

func reconcileRootRollbackAlias(parent *os.Root, destination string, guard rootParentCleanupGuard) error {
	backupName := BackupPath(destination)
	backup, err := parent.Lstat(backupName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !secureAtomicRecoveryFile(backup) {
		return ErrRootRollbackRecoveryRequired
	}
	current, err := parent.Lstat(destination)
	if err != nil || !secureAtomicRecoveryFile(current) || !os.SameFile(backup, current) {
		return ErrRootRollbackRecoveryRequired
	}
	return guard.run(parent, func() error {
		before, err := parent.Lstat(backupName)
		if err != nil || !secureAtomicRecoveryFile(before) || !os.SameFile(backup, before) {
			return errors.New("atomic rollback alias changed before cleanup")
		}
		if err := parent.Remove(backupName); err != nil {
			return fmt.Errorf("cannot remove atomic rollback alias: %w", err)
		}
		if err := syncRootDirectoryEntry(parent, backupName); err != nil {
			return errors.New("cannot sync atomic rollback alias cleanup")
		}
		return nil
	})
}

func secureAtomicRecoveryFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && !isAtomicRedirect(info) &&
		safeAtomicWriterMode(info.Mode())
}

// RecoverRootFileRollback restores an authenticated rollback backup left by an
// interrupted checked write. expectedOldHash is the lowercase hexadecimal
// SHA-256 of the old content, supplied by the higher-level transaction record.
func RecoverRootFileRollback(parent *os.Root, leaf, expectedOldHash string) (retErr error) {
	if parent == nil {
		return errors.New("atomic recovery root is required")
	}
	if !strictRootLeaf(leaf) || !validRootRollbackHash(expectedOldHash) {
		return errors.New("atomic recovery input is invalid")
	}
	guard, err := captureRootParentCleanupGuard(parent)
	if err != nil {
		return err
	}
	expectedBytes, _ := hex.DecodeString(expectedOldHash)
	authenticatedDestination, destinationAuthenticated := authenticatedRootFile(parent, leaf, expectedBytes)
	backupName := BackupPath(leaf)
	backupInfo, err := parent.Lstat(backupName)
	if errors.Is(err, os.ErrNotExist) {
		if !destinationAuthenticated {
			return ErrRootRollbackRecoveryRequired
		}
		return guard.run(parent, func() error {
			if _, ok := authenticatedRootFile(parent, leaf, expectedBytes); !ok {
				return ErrRootRollbackRecoveryRequired
			}
			return syncRootDirectoryEntry(parent, backupName)
		})
	}
	if err != nil || !secureAtomicRecoveryFile(backupInfo) {
		return ErrRootRollbackRecoveryRequired
	}
	if destinationAuthenticated {
		return guard.run(parent, func() error {
			currentDestination, ok := authenticatedRootFile(parent, leaf, expectedBytes)
			if !ok || !os.SameFile(authenticatedDestination, currentDestination) {
				return ErrRootRollbackRecoveryRequired
			}
			currentBackup, err := parent.Lstat(backupName)
			if err != nil || !os.SameFile(backupInfo, currentBackup) || !secureAtomicRecoveryFile(currentBackup) {
				return ErrRootRollbackRecoveryRequired
			}
			if err := parent.Remove(backupName); err != nil {
				return errors.New("cannot remove converged atomic rollback backup")
			}
			if err := syncRootDirectoryEntry(parent, backupName); err != nil {
				return errors.New("cannot sync converged atomic rollback cleanup")
			}
			return nil
		})
	}
	backupFile, err := parent.Open(backupName)
	if err != nil {
		return ErrRootRollbackRecoveryRequired
	}
	defer func() { retErr = errors.Join(retErr, backupFile.Close()) }()
	openedInfo, err := backupFile.Stat()
	if err != nil || !os.SameFile(backupInfo, openedInfo) || !secureAtomicRecoveryFile(openedInfo) {
		return ErrRootRollbackRecoveryRequired
	}
	snapshot, err := readStableRollbackSnapshot(backupFile, openedInfo)
	if err != nil {
		return ErrRootRollbackRecoveryRequired
	}
	actualHash := sha256.Sum256(snapshot)
	if !bytes.Equal(actualHash[:], expectedBytes) {
		return ErrRootRollbackRecoveryRequired
	}
	afterRead, err := parent.Lstat(backupName)
	if err != nil || !os.SameFile(backupInfo, afterRead) || !secureAtomicRecoveryFile(afterRead) {
		return ErrRootRollbackRecoveryRequired
	}
	rollback := rootRollback{
		active:   true,
		name:     backupName,
		original: backupInfo,
		file:     backupFile,
		snapshot: snapshot,
		hash:     actualHash,
	}

	current, err := parent.Lstat(leaf)
	if err == nil && secureAtomicRecoveryFile(current) && os.SameFile(backupInfo, current) {
		return guard.run(parent, func() error {
			if err := ensureRootRollbackContent(parent, leaf, &rollback, true, defaultDurabilityOps()); err != nil {
				return err
			}
			return removeRootRollback(parent, &rollback)
		})
	}
	var zeroDestination os.FileInfo
	if err == nil {
		if !secureAtomicRecoveryFile(current) || !stableEmptyRootFile(parent, leaf, current) {
			return ErrRootRollbackRecoveryRequired
		}
		zeroDestination = current
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrRootRollbackRecoveryRequired
	}

	return guard.run(parent, func() error {
		if err := ensureRootRollbackContent(parent, "", &rollback, false, defaultDurabilityOps()); err != nil {
			return err
		}
		if zeroDestination != nil {
			current, err := parent.Lstat(leaf)
			if err != nil || !os.SameFile(zeroDestination, current) || !stableEmptyRootFile(parent, leaf, current) {
				return ErrRootRollbackRecoveryRequired
			}
			if err := parent.Remove(leaf); err != nil {
				return errors.New("cannot remove authenticated empty recovery destination")
			}
		} else if _, err := parent.Lstat(leaf); !errors.Is(err, os.ErrNotExist) {
			return ErrRootRollbackRecoveryRequired
		}
		if err := parent.Link(backupName, leaf); err != nil {
			return errors.New("cannot publish authenticated atomic recovery")
		}
		restored, err := parent.Lstat(leaf)
		if err != nil || !os.SameFile(backupInfo, restored) || !secureAtomicRecoveryFile(restored) {
			return errors.New("authenticated atomic recovery identity changed")
		}
		if err := ensureRootRollbackContent(parent, leaf, &rollback, true, defaultDurabilityOps()); err != nil {
			return err
		}
		if err := syncRootDirectoryEntry(parent, leaf); err != nil {
			return errors.New("cannot sync authenticated atomic recovery")
		}
		return removeRootRollback(parent, &rollback)
	})
}

// RemoveRootFileIfHashMatches durably removes one private regular leaf only
// when its stable content matches the caller-authenticated SHA-256. It is used
// to retire a converged migration backup without deleting an unrelated file
// merely because it occupies the recovery name.
func RemoveRootFileIfHashMatches(parent *os.Root, leaf, expectedHash string) (retErr error) {
	if parent == nil || !strictRootLeaf(leaf) || !validRootRollbackHash(expectedHash) {
		return errors.New("atomic cleanup input is invalid")
	}
	guard, err := captureRootParentCleanupGuard(parent)
	if err != nil {
		return err
	}
	before, err := parent.Lstat(leaf)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !secureAtomicRecoveryFile(before) {
		return ErrRootRollbackRecoveryRequired
	}
	file, err := parent.Open(leaf)
	if err != nil {
		return ErrRootRollbackRecoveryRequired
	}
	defer func() { retErr = errors.Join(retErr, file.Close()) }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !secureAtomicRecoveryFile(opened) {
		return ErrRootRollbackRecoveryRequired
	}
	snapshot, err := readStableRollbackSnapshot(file, opened)
	snapshotHash := sha256.Sum256(snapshot)
	if err != nil || hex.EncodeToString(snapshotHash[:]) != expectedHash {
		return ErrRootRollbackRecoveryRequired
	}
	after, err := parent.Lstat(leaf)
	if err != nil || !os.SameFile(before, after) || !secureAtomicRecoveryFile(after) || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		return ErrRootRollbackRecoveryRequired
	}
	expectedBytes, _ := hex.DecodeString(expectedHash)
	return guard.run(parent, func() error {
		current, ok := authenticatedRootFile(parent, leaf, expectedBytes)
		if !ok || !os.SameFile(before, current) {
			return ErrRootRollbackRecoveryRequired
		}
		if err := parent.Remove(leaf); err != nil {
			return errors.New("cannot remove authenticated atomic file")
		}
		if err := syncRootDirectoryEntry(parent, leaf); err != nil {
			return errors.New("cannot sync authenticated atomic file cleanup")
		}
		return nil
	})
}

func authenticatedRootFile(parent *os.Root, leaf string, expectedHash []byte) (os.FileInfo, bool) {
	before, err := parent.Lstat(leaf)
	if err != nil || !secureAtomicRecoveryFile(before) {
		return nil, false
	}
	file, err := parent.Open(leaf)
	if err != nil {
		return nil, false
	}
	opened, statErr := file.Stat()
	content, readErr := readStableRollbackSnapshot(file, opened)
	closeErr := file.Close()
	after, nameErr := parent.Lstat(leaf)
	digest := sha256.Sum256(content)
	if statErr != nil || readErr != nil || closeErr != nil || nameErr != nil ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) || !secureAtomicRecoveryFile(opened) ||
		!secureAtomicRecoveryFile(after) || !bytes.Equal(digest[:], expectedHash) {
		return nil, false
	}
	return after, true
}

func validRootRollbackHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func stableEmptyRootFile(parent *os.Root, leaf string, expected os.FileInfo) bool {
	if !secureAtomicRecoveryFile(expected) || expected.Size() != 0 {
		return false
	}
	file, err := parent.Open(leaf)
	if err != nil {
		return false
	}
	opened, statErr := file.Stat()
	content, readErr := readStableRollbackSnapshot(file, opened)
	closeErr := file.Close()
	after, nameErr := parent.Lstat(leaf)
	return statErr == nil && readErr == nil && closeErr == nil && nameErr == nil && len(content) == 0 &&
		os.SameFile(expected, opened) && os.SameFile(opened, after) && secureAtomicRecoveryFile(opened) && secureAtomicRecoveryFile(after)
}

type rootParentCleanupGuard struct {
	info os.FileInfo
	mode fs.FileMode
	safe bool
}

func captureRootParentCleanupGuard(parent *os.Root) (rootParentCleanupGuard, error) {
	if parent == nil {
		return rootParentCleanupGuard{}, errors.New("atomic cleanup parent is required")
	}
	info, err := parent.Stat(".")
	if err != nil || info == nil || !info.IsDir() || isAtomicRedirect(info) {
		return rootParentCleanupGuard{}, errors.New("atomic cleanup parent is redirected or invalid")
	}
	mode := info.Mode().Perm()
	safe := mode&0o700 == 0o700 && mode&0o077 == 0
	return rootParentCleanupGuard{info: info, mode: mode, safe: safe}, nil
}

func (guard rootParentCleanupGuard) run(parent *os.Root, operation func() error) error {
	firstErr := operation()
	current, statErr := parent.Stat(".")
	if statErr != nil || !os.SameFile(guard.info, current) || !current.IsDir() || isAtomicRedirect(current) {
		if firstErr != nil {
			return errors.Join(firstErr, errors.New("atomic cleanup parent identity changed"))
		}
		return errors.New("atomic cleanup parent identity changed")
	}
	if current.Mode().Perm() == guard.mode {
		return firstErr
	}
	if !guard.safe {
		return errors.Join(firstErr, errors.New("atomic cleanup parent mode changed from an unsafe original mode"))
	}
	if err := parent.Chmod(".", guard.mode); err != nil {
		return errors.Join(firstErr, errors.New("cannot restore private atomic cleanup parent mode"))
	}
	result := firstErr
	if firstErr != nil {
		result = operation()
	}
	after, err := parent.Stat(".")
	if err != nil || !os.SameFile(guard.info, after) || after.Mode().Perm() != guard.mode {
		return errors.Join(result, errors.New("atomic cleanup parent mode was not restored"))
	}
	return result
}

type rootRollback struct {
	active   bool
	name     string
	original os.FileInfo
	file     *os.File
	snapshot []byte
	hash     [sha256.Size]byte
}

func (rollback rootRollback) close() error {
	if rollback.file == nil {
		return nil
	}
	return rollback.file.Close()
}

func readStableRollbackSnapshot(file *os.File, expected os.FileInfo) ([]byte, error) {
	if file == nil || expected == nil || expected.Size() < 0 || expected.Size() > maxRollbackSnapshotBytes {
		return nil, errors.New("atomic rollback snapshot exceeds size limit or is invalid")
	}
	read := func() ([]byte, os.FileInfo, error) {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, nil, err
		}
		content, err := io.ReadAll(io.LimitReader(file, maxRollbackSnapshotBytes+1))
		if err != nil || len(content) > maxRollbackSnapshotBytes {
			return nil, nil, errors.New("cannot read complete atomic rollback snapshot")
		}
		info, err := file.Stat()
		if err != nil {
			return nil, nil, err
		}
		return content, info, nil
	}
	first, middle, err := read()
	if err != nil {
		return nil, errors.New("cannot read stable atomic rollback snapshot")
	}
	second, after, err := read()
	if err != nil || !os.SameFile(expected, middle) || !os.SameFile(middle, after) ||
		middle.Size() != int64(len(first)) || after.Size() != int64(len(second)) || !bytes.Equal(first, second) {
		return nil, errors.New("atomic rollback snapshot changed while reading")
	}
	return first, nil
}

func ensureRootRollbackContent(parent *os.Root, destination string, rollback *rootRollback, requireDestination bool, ops durabilityOps) error {
	if rollback == nil || !rollback.active {
		return nil
	}
	if err := validateRootRollbackIdentity(parent, destination, *rollback, requireDestination); err != nil {
		return err
	}
	current, err := rollback.file.Stat()
	if err == nil && os.SameFile(rollback.original, current) {
		content, readErr := readStableRollbackSnapshot(rollback.file, current)
		if readErr == nil && sha256.Sum256(content) == rollback.hash && current.Mode() == rollback.original.Mode() {
			return nil
		}
	}
	if !requireDestination {
		return errors.New("atomic rollback content changed")
	}
	if err := restoreRootRollbackCopy(parent, destination, rollback.original, *rollback, ops); err != nil {
		return err
	}
	rollback.active = false
	return errors.New("tampered atomic rollback was restored by copy")
}

func restoreRootRollbackCopy(parent *os.Root, destination string, expectedDestination os.FileInfo, rollback rootRollback, ops durabilityOps) (retErr error) {
	if !safeAtomicWriterMode(rollback.original.Mode()) {
		return errors.New("atomic rollback mode is unsafe")
	}
	if err := validateRootRollbackIdentity(parent, destination, rollback, false); err != nil {
		return err
	}
	currentDestination, err := parent.Lstat(destination)
	if err != nil || !os.SameFile(expectedDestination, currentDestination) || !currentDestination.Mode().IsRegular() || isAtomicRedirect(currentDestination) {
		return errors.New("atomic rollback destination changed before copy restore")
	}
	tmp, tmpName, err := createRootTemp(parent)
	if err != nil {
		return errors.New("cannot create atomic rollback copy")
	}
	createdInfo, err := tmp.Stat()
	if err != nil || !createdInfo.Mode().IsRegular() || isAtomicRedirect(createdInfo) {
		_ = tmp.Close()
		return errors.New("cannot verify atomic rollback copy")
	}
	published := false
	defer func() {
		if retErr != nil && !published {
			retErr = errors.Join(retErr, sanitizeAndRemoveRootTemporary(parent, tmp, tmpName, createdInfo, ops.sanitizeTemporary))
		}
		retErr = errors.Join(retErr, tmp.Close())
	}()
	if err := tmp.Chmod(rollback.original.Mode().Perm()); err != nil {
		return errors.New("cannot set atomic rollback copy mode")
	}
	writeSnapshot := ops.writeRecoverySnapshot
	if writeSnapshot == nil {
		writeSnapshot = writeFullRootSnapshot
	}
	if err := writeSnapshot(tmp, rollback.snapshot); err != nil {
		return errors.New("cannot write complete atomic rollback copy")
	}
	if err := ops.syncTemporary(tmp); err != nil {
		return errors.New("cannot sync atomic rollback copy")
	}
	temporaryInfo, err := tmp.Stat()
	if err != nil || !os.SameFile(createdInfo, temporaryInfo) || temporaryInfo.Mode().Perm() != rollback.original.Mode().Perm() {
		return errors.New("atomic rollback copy identity or mode changed")
	}
	content, err := readStableRollbackSnapshot(tmp, temporaryInfo)
	if err != nil || sha256.Sum256(content) != rollback.hash {
		return errors.New("atomic rollback copy content failed verification")
	}
	temporaryNameInfo, err := parent.Lstat(tmpName)
	if err != nil || !os.SameFile(temporaryInfo, temporaryNameInfo) || !temporaryNameInfo.Mode().IsRegular() || isAtomicRedirect(temporaryNameInfo) {
		return errors.New("atomic rollback copy name changed")
	}
	if err := validateRootRollbackIdentity(parent, destination, rollback, false); err != nil {
		return err
	}
	currentDestination, err = parent.Lstat(destination)
	if err != nil || !os.SameFile(expectedDestination, currentDestination) || !currentDestination.Mode().IsRegular() || isAtomicRedirect(currentDestination) {
		return errors.New("atomic rollback destination changed before copy publication")
	}
	if err := ops.publish(parent, tmpName, destination); err != nil {
		partial, inspectErr := parent.Lstat(destination)
		if inspectErr == nil && os.SameFile(temporaryInfo, partial) {
			published = true
		} else {
			return errors.New("cannot publish atomic rollback copy")
		}
	}
	published = true
	restored, err := parent.Lstat(destination)
	if err != nil || !os.SameFile(temporaryInfo, restored) || restored.Mode().Perm() != rollback.original.Mode().Perm() || !secureAtomicRecoveryFile(restored) {
		return errors.New("atomic rollback copy publication changed")
	}
	restoredFile, err := parent.Open(destination)
	if err != nil {
		return errors.New("cannot open restored atomic rollback copy")
	}
	restoredOpened, statErr := restoredFile.Stat()
	restoredContent, readErr := readStableRollbackSnapshot(restoredFile, restoredOpened)
	closeErr := restoredFile.Close()
	if statErr != nil || readErr != nil || closeErr != nil || !os.SameFile(restored, restoredOpened) || sha256.Sum256(restoredContent) != rollback.hash {
		return errors.New("restored atomic rollback copy failed verification")
	}
	if err := ops.syncPublication(parent, destination); err != nil {
		return fmt.Errorf("cannot sync restored atomic rollback copy: %w", err)
	}
	if err := removeRootRollbackEntry(parent, rollback); err != nil {
		return err
	}
	return nil
}

func writeFullRootSnapshot(file *os.File, content []byte) error {
	remaining := content
	for len(remaining) != 0 {
		written, err := file.Write(remaining)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		remaining = remaining[written:]
	}
	return nil
}

func captureRootRollback(parent *os.Root, destination string) (rootRollback, error) {
	original, err := parent.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return rootRollback{}, nil
	}
	if err != nil || !original.Mode().IsRegular() || isAtomicRedirect(original) || !safeAtomicWriterMode(original.Mode()) {
		return rootRollback{}, errors.New("atomic destination is redirected or not regular")
	}
	file, err := parent.Open(destination)
	if err != nil {
		return rootRollback{}, errors.New("cannot open atomic destination for rollback")
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(original, opened) || !opened.Mode().IsRegular() || isAtomicRedirect(opened) {
		_ = file.Close()
		return rootRollback{}, errors.New("atomic destination changed before rollback")
	}
	snapshot, err := readStableRollbackSnapshot(file, opened)
	if err != nil {
		_ = file.Close()
		return rootRollback{}, err
	}
	return rootRollback{active: true, name: BackupPath(destination), original: original, file: file, snapshot: snapshot, hash: sha256.Sum256(snapshot)}, nil
}

func prepareRootRollback(parent *os.Root, destination string, rollback rootRollback, ops durabilityOps) (rootRollback, error) {
	backup := BackupPath(destination)
	if _, err := parent.Lstat(backup); err == nil {
		return rootRollback{}, errors.New("atomic rollback backup already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return rootRollback{}, errors.New("cannot inspect atomic rollback backup")
	}
	if !rollback.active {
		return rootRollback{}, nil
	}
	current, err := parent.Lstat(destination)
	if err != nil || !os.SameFile(rollback.original, current) || !current.Mode().IsRegular() || isAtomicRedirect(current) {
		return rootRollback{}, errors.New("atomic destination changed before rollback")
	}
	link := ops.linkRollback
	if link == nil {
		link = (*os.Root).Link
	}
	if err := link(parent, destination, backup); err != nil {
		return rootRollback{}, fmt.Errorf("cannot establish atomic rollback hardlink: %w", err)
	}
	if err := ensureRootRollbackContent(parent, destination, &rollback, true, ops); err != nil {
		return rootRollback{}, err
	}
	if err := syncRootDirectoryEntry(parent, backup); err != nil {
		return rootRollback{}, errors.Join(errors.New("cannot sync atomic rollback hardlink"), removeRootRollback(parent, &rollback))
	}
	if err := ensureRootRollbackContent(parent, destination, &rollback, true, ops); err != nil {
		return rootRollback{}, err
	}
	return rollback, nil
}

func validateRootRollbackIdentity(parent *os.Root, destination string, rollback rootRollback, requireDestination bool) error {
	if !rollback.active {
		return nil
	}
	backup, err := parent.Lstat(rollback.name)
	if err != nil || !os.SameFile(rollback.original, backup) || !backup.Mode().IsRegular() || isAtomicRedirect(backup) {
		return errors.New("atomic rollback backup identity changed")
	}
	if requireDestination {
		current, err := parent.Lstat(destination)
		if err != nil || !os.SameFile(rollback.original, current) || !current.Mode().IsRegular() || isAtomicRedirect(current) {
			return errors.New("atomic destination changed after rollback backup")
		}
	}
	return nil
}

func removeRootRollback(parent *os.Root, rollback *rootRollback) error {
	if rollback == nil || !rollback.active {
		return nil
	}
	if err := removeRootRollbackEntry(parent, *rollback); err != nil {
		return err
	}
	rollback.active = false
	return nil
}

func removeRootRollbackEntry(parent *os.Root, rollback rootRollback) error {
	if err := validateRootRollbackIdentity(parent, "", rollback, false); err != nil {
		return err
	}
	if err := parent.Remove(rollback.name); err != nil {
		return errors.New("cannot remove atomic rollback backup")
	}
	if err := syncRootDirectoryEntry(parent, rollback.name); err != nil {
		return errors.New("cannot sync atomic rollback backup removal")
	}
	return nil
}

func rollbackRootPublication(parent *os.Root, destination string, publishedInfo os.FileInfo, desired []byte, rollback *rootRollback, ops durabilityOps) error {
	if rollback != nil && rollback.active {
		if err := validateRootRollbackIdentity(parent, destination, *rollback, false); err != nil {
			return err
		}
		current, err := rollback.file.Stat()
		content, readErr := readStableRollbackSnapshot(rollback.file, current)
		if err != nil || readErr != nil || !os.SameFile(rollback.original, current) || sha256.Sum256(content) != rollback.hash || current.Mode() != rollback.original.Mode() {
			if err := verifyPublishedRootFileOwned(parent, destination, publishedInfo, desired); err != nil {
				return err
			}
			if err := restoreRootRollbackCopy(parent, destination, publishedInfo, *rollback, ops); err != nil {
				return err
			}
			rollback.active = false
			return nil
		}
	}
	if err := removePublishedRootFileIfOwned(parent, destination, publishedInfo, desired); err != nil {
		return err
	}
	if rollback == nil || !rollback.active {
		return nil
	}
	if err := parent.Link(rollback.name, destination); err != nil {
		return errors.New("cannot restore atomic rollback destination")
	}
	restored, err := parent.Lstat(destination)
	if err != nil || !os.SameFile(rollback.original, restored) || !restored.Mode().IsRegular() || isAtomicRedirect(restored) {
		return errors.New("restored atomic rollback identity changed")
	}
	if err := syncRootDirectoryEntry(parent, destination); err != nil {
		return errors.New("cannot sync restored atomic rollback destination")
	}
	return removeRootRollback(parent, rollback)
}

func sanitizeRootTemporary(file *os.File) error {
	truncateErr := file.Truncate(0)
	syncErr := file.Sync()
	return errors.Join(truncateErr, syncErr)
}

func sanitizeAndRemoveRootTemporary(parent *os.Root, file *os.File, name string, createdInfo os.FileInfo, sanitize func(*os.File) error) error {
	if sanitize == nil {
		sanitize = sanitizeRootTemporary
	}
	var sanitizeErr error
	if err := sanitize(file); err != nil {
		sanitizeErr = fmt.Errorf("cannot sanitize failed atomic temporary: %w", err)
	}
	return errors.Join(sanitizeErr, removeRootEntryIfOwned(parent, name, createdInfo))
}

func removeRootEntryIfOwned(parent *os.Root, name string, ownedInfo os.FileInfo) error {
	current, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !os.SameFile(ownedInfo, current) || !current.Mode().IsRegular() || isAtomicRedirect(current) {
		return errors.New("atomic temporary ownership changed; not removed")
	}
	if err := parent.Remove(name); err != nil {
		return errors.New("cannot remove failed atomic temporary")
	}
	return nil
}

func removePublishedRootFileIfOwned(parent *os.Root, name string, publishedInfo os.FileInfo, desired []byte) error {
	if err := verifyPublishedRootFileOwned(parent, name, publishedInfo, desired); err != nil {
		return err
	}
	final, err := parent.Lstat(name)
	if err != nil || !os.SameFile(publishedInfo, final) || !final.Mode().IsRegular() || isAtomicRedirect(final) {
		return errors.New("published file ownership changed before cleanup; not removed")
	}
	if err := parent.Remove(name); err != nil {
		return errors.New("cannot remove failed atomic publication")
	}
	if err := syncRootDirectoryEntry(parent, name); err != nil {
		return errors.New("cannot sync failed atomic publication cleanup")
	}
	return nil
}

func verifyPublishedRootFileOwned(parent *os.Root, name string, publishedInfo os.FileInfo, desired []byte) error {
	before, err := parent.Lstat(name)
	if err != nil || !os.SameFile(publishedInfo, before) || !before.Mode().IsRegular() || isAtomicRedirect(before) {
		return errors.New("published file ownership changed; not removed")
	}
	file, err := parent.Open(name)
	if err != nil {
		return errors.New("cannot verify published file for cleanup")
	}
	opened, statErr := file.Stat()
	content, readErr := io.ReadAll(file)
	afterOpen, afterStatErr := file.Stat()
	closeErr := file.Close()
	afterName, inspectErr := parent.Lstat(name)
	wantHash := sha256.Sum256(desired)
	gotHash := sha256.Sum256(content)
	if statErr != nil || readErr != nil || afterStatErr != nil || closeErr != nil || inspectErr != nil ||
		!os.SameFile(publishedInfo, opened) || !os.SameFile(opened, afterOpen) || !os.SameFile(opened, afterName) ||
		!opened.Mode().IsRegular() || isAtomicRedirect(opened) || gotHash != wantHash {
		return errors.New("published file ownership or content changed; not removed")
	}
	return nil
}

func createRootTemp(root *os.Root) (*os.File, string, error) {
	for range 100 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", fmt.Errorf("generate temporary name: %w", err)
		}
		name := ".session-reviewer-" + hex.EncodeToString(random[:])
		file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("create unique temporary file")
}
