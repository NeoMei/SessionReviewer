package annotation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/project"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

const (
	annotationHeadMaxBytes   = int64(16 << 10)
	annotationRecordMaxBytes = int64(64 << 20)
	annotationMaxRevision    = uint64(1<<53 - 1)
)

var (
	ErrStoreNotFound             = errors.New("annotation store not found")
	ErrStoreReadOnly             = errors.New("annotation store is read-only")
	ErrCandidateRevisionConflict = errors.New("candidate revision conflict")
)

type StoredState struct {
	Revision uint64
	Digest   string
	Record   StoreRecord
}

type CandidateRevisionConflict struct {
	CurrentRevision uint64
	CurrentDigest   string
}

func (conflict *CandidateRevisionConflict) Error() string {
	return fmt.Sprintf("candidate revision conflict at revision %d digest %s", conflict.CurrentRevision, conflict.CurrentDigest)
}

func (conflict *CandidateRevisionConflict) Is(target error) bool {
	return target == ErrCandidateRevisionConflict
}

type annotationStoreHead struct {
	SchemaVersion int    `json:"schema_version" required:"true"`
	ProjectID     string `json:"project_id" required:"true"`
	Revision      uint64 `json:"revision" required:"true"`
	Digest        string `json:"digest" required:"true"`
}

type annotationStoreTestHooks struct {
	beforeImmutableWrite func() error
	afterImmutableWrite  func() error
	beforeHeadWrite      func() error
}

type FileStore struct {
	mu        sync.Mutex
	dataRoot  string
	projectID string
	path      string
	root      *pathguard.Directory
	readOnly  bool
	closed    bool
	testHooks annotationStoreTestHooks
}

func OpenStore(dataRoot, projectID string) (*FileStore, error) {
	return openStore(dataRoot, projectID, false)
}

func OpenStoreReadOnly(dataRoot, projectID string) (*FileStore, error) {
	return openStore(dataRoot, projectID, true)
}

func openStore(dataRoot, projectID string, readOnly bool) (*FileStore, error) {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot || !validID(projectID) {
		return nil, errors.New("annotation store requires a clean absolute data root and valid project ID")
	}
	data, err := pathguard.Open(dataRoot)
	if err != nil {
		return nil, errors.New("open annotation data root")
	}
	if err := validatePrivateDirectory(data.Info()); err != nil {
		_ = data.Close()
		return nil, errors.New("annotation data root is not private")
	}
	relative := filepath.ToSlash(filepath.Join("projects", projectID, "annotations"))
	full := filepath.Join(data.Path, filepath.FromSlash(relative))
	if err := validateExistingPrivateTree(data, relative+"/revisions"); err != nil {
		_ = data.Close()
		return nil, err
	}
	if readOnly {
		root, openErr := pathguard.Open(full)
		if openErr != nil {
			_ = data.Close()
			if errors.Is(openErr, os.ErrNotExist) || strings.Contains(openErr.Error(), "does not exist") {
				return &FileStore{dataRoot: dataRoot, projectID: projectID, path: full, readOnly: true}, nil
			}
			return nil, errors.New("open read-only annotation namespace")
		}
		_ = data.Close()
		if err := validatePrivateAncestors(root); err != nil {
			_ = root.Close()
			return nil, err
		}
		return &FileStore{dataRoot: dataRoot, projectID: projectID, path: full, root: root, readOnly: true}, nil
	}
	if err := data.EnsureDirectory(relative+"/revisions", 0o700); err != nil {
		_ = data.Close()
		return nil, errors.New("create annotation namespace")
	}
	root, err := pathguard.Open(full)
	_ = data.Close()
	if err != nil {
		return nil, errors.New("pin annotation namespace")
	}
	if err := validatePrivateDirectory(root.Info()); err != nil {
		_ = root.Close()
		return nil, err
	}
	return &FileStore{dataRoot: dataRoot, projectID: projectID, path: full, root: root}, nil
}

func validateExistingPrivateTree(base *pathguard.Directory, relative string) error {
	current, err := base.Root.OpenRoot(".")
	if err != nil {
		return errors.New("pin annotation data root")
	}
	defer func() { _ = current.Close() }()
	for _, component := range strings.Split(filepath.FromSlash(relative), string(filepath.Separator)) {
		before, err := current.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || validatePrivateDirectory(before) != nil {
			return errors.New("existing annotation namespace is redirected or not private")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			return errors.New("open existing annotation namespace")
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(before, opened) {
			_ = next.Close()
			return errors.New("existing annotation namespace changed while opening")
		}
		_ = current.Close()
		current = next
	}
	return nil
}

func (store *FileStore) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	if store.root == nil {
		return nil
	}
	return store.root.Close()
}

func (store *FileStore) Load(ctx context.Context) (StoredState, error) {
	if store == nil || ctx == nil {
		return StoredState{}, errors.New("annotation store and context are required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.usable(); err != nil {
		return StoredState{}, err
	}
	return store.loadLocked(ctx)
}

func (store *FileStore) CompareAndSwap(ctx context.Context, expectedRevision uint64, expectedDigest string, next StoreRecord) (result StoredState, retErr error) {
	if store == nil || ctx == nil {
		return StoredState{}, errors.New("annotation store and context are required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.readOnly {
		return StoredState{}, ErrStoreReadOnly
	}
	if err := store.usable(); err != nil {
		return StoredState{}, err
	}
	if expectedRevision > annotationMaxRevision || (expectedRevision == 0) != (expectedDigest == "") || expectedDigest != "" && !digestRE.MatchString(expectedDigest) {
		return StoredState{}, errors.New("invalid annotation store CAS preimage")
	}
	if err := context.Cause(ctx); err != nil {
		return StoredState{}, err
	}
	lock, err := project.AcquireProjectLock(store.root.Root, "store.lock", 10*time.Second)
	if err != nil {
		return StoredState{}, err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	current, loadErr := store.loadLocked(ctx)
	missing := errors.Is(loadErr, ErrStoreNotFound)
	if loadErr != nil && !missing {
		return StoredState{}, loadErr
	}
	if missing {
		current = StoredState{}
	}
	if current.Revision != expectedRevision || current.Digest != expectedDigest {
		return StoredState{}, &CandidateRevisionConflict{CurrentRevision: current.Revision, CurrentDigest: current.Digest}
	}
	if next.ProjectID != store.projectID {
		return StoredState{}, errors.New("annotation record belongs to another project")
	}
	body, err := Render(next)
	if err != nil {
		return StoredState{}, err
	}
	canonicalNext, err := Parse(body)
	if err != nil {
		return StoredState{}, err
	}
	if err := validateStoreTransition(current.Record, canonicalNext, !missing); err != nil {
		return StoredState{}, err
	}
	digest := annotationRecordDigest(body)
	if !missing && digest == current.Digest {
		return current, nil
	}
	if current.Revision == annotationMaxRevision {
		return StoredState{}, errors.New("annotation store revision overflow")
	}
	if hook := store.testHooks.beforeImmutableWrite; hook != nil {
		if err := hook(); err != nil {
			return StoredState{}, err
		}
	}
	revisions, info, err := store.root.OpenDirectory("revisions")
	if err != nil || validatePrivateDirectory(info) != nil {
		if revisions != nil {
			_ = revisions.Close()
		}
		return StoredState{}, errors.New("annotation revisions namespace is unavailable")
	}
	leaf := strings.TrimPrefix(digest, "sha256:") + ".json"
	writeErr := atomicfile.WriteRootFileCreateIfAbsent(revisions, leaf, body, 0o600, func() error { return context.Cause(ctx) })
	if errors.Is(writeErr, os.ErrExist) {
		entryErr := requirePrivateEntry(revisions, leaf, false)
		existing, found, readErr := store.root.ReadRegularOptional(filepath.ToSlash(filepath.Join("revisions", leaf)), annotationRecordMaxBytes)
		if entryErr != nil || readErr != nil || !found || !bytes.Equal(existing, body) {
			_ = revisions.Close()
			return StoredState{}, errors.New("immutable annotation revision conflicts with existing bytes")
		}
		writeErr = nil
	}
	closeErr := revisions.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return StoredState{}, err
	}
	if hook := store.testHooks.afterImmutableWrite; hook != nil {
		if err := hook(); err != nil {
			return StoredState{}, err
		}
	}
	if err := store.usable(); err != nil {
		return StoredState{}, err
	}
	if err := context.Cause(ctx); err != nil {
		return StoredState{}, err
	}
	rechecked, recheckErr := store.loadLocked(ctx)
	recheckMissing := errors.Is(recheckErr, ErrStoreNotFound)
	if recheckErr != nil && !recheckMissing {
		return StoredState{}, recheckErr
	}
	if recheckMissing {
		rechecked = StoredState{}
	}
	if rechecked.Revision != expectedRevision || rechecked.Digest != expectedDigest {
		return StoredState{}, &CandidateRevisionConflict{CurrentRevision: rechecked.Revision, CurrentDigest: rechecked.Digest}
	}
	if hook := store.testHooks.beforeHeadWrite; hook != nil {
		if err := hook(); err != nil {
			return StoredState{}, err
		}
	}
	if err := context.Cause(ctx); err != nil {
		return StoredState{}, err
	}
	head := annotationStoreHead{SchemaVersion: 1, ProjectID: store.projectID, Revision: current.Revision + 1, Digest: digest}
	headBytes, err := strictjson.Encode(head)
	if err != nil {
		return StoredState{}, err
	}
	if err := atomicfile.WriteRootFile(store.root.Root, "head.json", headBytes, 0o600); err != nil {
		return StoredState{}, err
	}
	if err := store.usable(); err != nil {
		return StoredState{}, err
	}
	return StoredState{Revision: head.Revision, Digest: digest, Record: canonicalNext}, nil
}

func (store *FileStore) usable() error {
	if store.closed {
		return errors.New("annotation store is closed")
	}
	if store.root == nil {
		return ErrStoreNotFound
	}
	opened, err := pathguard.Open(store.path)
	if err != nil {
		return errors.New("annotation namespace identity changed")
	}
	defer opened.Close()
	if store.root.Info() == nil || opened.Info() == nil || !os.SameFile(store.root.Info(), opened.Info()) {
		return errors.New("annotation namespace identity changed")
	}
	return validatePrivateDirectory(store.root.Info())
}

func (store *FileStore) loadLocked(ctx context.Context) (StoredState, error) {
	if err := context.Cause(ctx); err != nil {
		return StoredState{}, err
	}
	headBytes, found, err := store.root.ReadRegularOptional("head.json", annotationHeadMaxBytes)
	if err != nil {
		return StoredState{}, errors.New("read annotation head")
	}
	if !found {
		return StoredState{}, ErrStoreNotFound
	}
	if err := requirePrivateEntry(store.root.Root, "head.json", false); err != nil {
		return StoredState{}, err
	}
	var head annotationStoreHead
	if err := strictjson.Decode(headBytes, &head); err != nil || head.SchemaVersion != 1 || head.ProjectID != store.projectID || head.Revision == 0 || head.Revision > annotationMaxRevision || !digestRE.MatchString(head.Digest) {
		return StoredState{}, errors.New("annotation head is invalid")
	}
	leaf := strings.TrimPrefix(head.Digest, "sha256:") + ".json"
	recordBytes, found, err := store.root.ReadRegularOptional(filepath.ToSlash(filepath.Join("revisions", leaf)), annotationRecordMaxBytes)
	if err != nil || !found {
		return StoredState{}, errors.New("annotation revision is unavailable")
	}
	if err := requirePrivateEntry(store.root.Root, "revisions", true); err != nil {
		return StoredState{}, err
	}
	revisions, _, err := store.root.OpenDirectory("revisions")
	if err != nil {
		return StoredState{}, errors.New("open annotation revisions")
	}
	entryErr := requirePrivateEntry(revisions, leaf, false)
	_ = revisions.Close()
	if entryErr != nil {
		return StoredState{}, entryErr
	}
	if annotationRecordDigest(recordBytes) != head.Digest {
		return StoredState{}, errors.New("annotation record digest mismatch")
	}
	record, err := Parse(recordBytes)
	if err != nil || record.ProjectID != store.projectID {
		return StoredState{}, errors.New("annotation record is invalid")
	}
	if err := validateStoreRecordSemantics(record); err != nil {
		return StoredState{}, errors.New("annotation record is semantically invalid")
	}
	canonical, err := Render(record)
	if err != nil || !bytes.Equal(canonical, recordBytes) {
		return StoredState{}, errors.New("annotation record is not canonical")
	}
	return StoredState{Revision: head.Revision, Digest: head.Digest, Record: record}, nil
}

func annotationRecordDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func requirePrivateEntry(root *os.Root, leaf string, directory bool) error {
	info, err := root.Lstat(leaf)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || directory != info.IsDir() {
		return errors.New("annotation namespace entry is unsafe")
	}
	if runtime.GOOS != "windows" {
		want := os.FileMode(0o600)
		if directory {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			return errors.New("annotation namespace entry is not private")
		}
	}
	return nil
}

func validatePrivateDirectory(info os.FileInfo) error {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return errors.New("annotation namespace is not private")
	}
	return nil
}

func validatePrivateAncestors(root *pathguard.Directory) error {
	if root == nil || len(root.Ancestors) < 4 {
		return errors.New("annotation namespace ancestry is unavailable")
	}
	// The last four identities are dataRoot/projects/projectID/annotations.
	// System ancestors are outside the private application namespace.
	for _, info := range root.Ancestors[len(root.Ancestors)-4:] {
		if err := validatePrivateDirectory(info); err != nil {
			return errors.New("annotation namespace ancestor is not private")
		}
	}
	return nil
}
