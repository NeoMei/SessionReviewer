package reviewjob

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

var (
	ErrStaleRevision     = errors.New("stale review job revision")
	ErrRevisionExhausted = errors.New("review job revision is exhausted")
	ErrActiveJob         = errors.New("project already has an active review job")
)

const (
	maxJobRecordBytes        = 4 << 20
	maxJobIdentityBytes      = 16 << 10
	maxJobRepairBytes        = 8 << 20
	maxProjectPointerBytes   = 16 << 10
	maxJobDirectoryEntries   = 4_096
	maxProjectPointerEntries = 4_096
	storeLockName            = "store.lock"
)

type Store struct {
	Root                string
	RejectActiveProject bool

	beforePointerWrite    func() error
	afterIdentityWrite    func() error
	afterJobRead          func() error
	afterJobIdentityRead  func() error
	afterJobDirectoryScan func() error
	writeRoot             func(*os.Root, string, []byte, fs.FileMode) error
}

type storedJob struct {
	Revision int `json:"revision"`
	Job      Job `json:"job"`
}

type projectPointer struct {
	SchemaVersion   int                     `json:"schema_version"`
	ProjectID       string                  `json:"project_id"`
	ProjectIdentity pathguard.IdentityToken `json:"project_identity"`
	JobID           string                  `json:"job_id"`
}

type jobIdentity struct {
	SchemaVersion   int                     `json:"schema_version"`
	JobID           string                  `json:"job_id"`
	ProjectID       string                  `json:"project_id"`
	ProjectIdentity pathguard.IdentityToken `json:"project_identity"`
}

type jobStatePair struct {
	id           string
	identityName string
	identityInfo os.FileInfo
	primaryName  string
	primaryInfo  os.FileInfo
	backupName   string
	backupInfo   os.FileInfo
}

type storeLayout struct {
	data         *pathguard.Directory
	review       *os.Root
	jobs         *os.Root
	projects     *os.Root
	locks        *os.Root
	projectLocks *os.Root
	work         *os.Root
	missing      bool
}

func (s Store) Create(job Job) (revision int, retErr error) {
	if err := Validate(job); err != nil {
		return 0, fmt.Errorf("invalid review job: %w", err)
	}
	if err := validateStoreID(job.ID, "job"); err != nil {
		return 0, err
	}
	if err := validateStoreID(job.ProjectID, "project"); err != nil {
		return 0, err
	}
	layout, err := s.openLayout(true)
	if err != nil {
		return 0, err
	}
	defer func() { retErr = errors.Join(retErr, layout.finish()) }()
	lock, err := acquireStoreFileLock(layout.locks, storeLockName, 2*time.Second)
	if err != nil {
		return 0, fmt.Errorf("lock review job store: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, lock.release()) }()
	if s.RejectActiveProject {
		activeJob, activeRevision, found, err := s.activeProjectJob(layout, job.ProjectID)
		if err != nil {
			return 0, err
		}
		if found {
			return activeRevision, fmt.Errorf("%w: %s", ErrActiveJob, activeJob.ID)
		}
	}

	if err := rejectCaseCollision(layout.jobs, jobIdentityName(job.ID), job.ID+".json", job.ID+".json.bak"); err != nil {
		return 0, err
	}
	createdIdentity, err := s.ensureJobIdentity(layout.jobs, job)
	if err != nil {
		return 0, err
	}
	if createdIdentity && s.afterIdentityWrite != nil {
		if err := s.afterIdentityWrite(); err != nil {
			return 0, fmt.Errorf("publish review job identity: %w", err)
		}
	}
	if _, _, found, err := s.loadFromJobs(layout.jobs, job.ID); err != nil {
		return 0, err
	} else if found {
		return 0, errors.New("review job already exists")
	}
	work, err := ensurePrivateDirectory(layout.work, job.ID)
	if err != nil {
		return 0, fmt.Errorf("create review job work directory: %w", err)
	}
	if err := work.Close(); err != nil {
		return 0, fmt.Errorf("close review job work directory: %w", err)
	}

	record := storedJob{Revision: 1, Job: job}
	encoded, err := marshalCanonical(record, maxJobRecordBytes)
	if err != nil {
		return 0, err
	}
	if err := s.writer()(layout.jobs, job.ID+".json", encoded, 0o600); err != nil {
		return 0, fmt.Errorf("persist review job: %w", err)
	}
	written, writtenRevision, found, err := s.loadFromJobs(layout.jobs, job.ID)
	if err != nil || !found || writtenRevision != 1 || !reflect.DeepEqual(written, job) {
		return 0, errors.New("review job failed canonical post-write verification")
	}

	if s.beforePointerWrite != nil {
		if err := s.beforePointerWrite(); err != nil {
			return 0, fmt.Errorf("publish project pointer: %w", err)
		}
	}
	if err := s.publishPointer(layout.projects, projectPointerFor(job)); err != nil {
		return 0, err
	}
	return 1, nil
}

func (s Store) activeProjectJob(layout *storeLayout, projectID string) (Job, int, bool, error) {
	if layout == nil || layout.missing {
		return Job{}, 0, false, nil
	}
	if err := rejectCaseCollision(layout.projects, projectID+".json"); err != nil {
		return Job{}, 0, false, err
	}
	pointer, found, err := readProjectPointer(layout.projects, projectID)
	if err != nil {
		return Job{}, 0, false, err
	}
	if found {
		job, revision, loaded, err := s.loadAuthenticatedPointer(layout.jobs, pointer, projectID)
		if err != nil || !loaded {
			return Job{}, 0, false, err
		}
		if active(job.State) {
			return job, revision, true, nil
		}
		return Job{}, 0, false, nil
	}
	job, revision, loaded, err := s.latestJobByEnumeration(layout.jobs, projectID)
	if err != nil || !loaded || !active(job.State) {
		return Job{}, 0, false, err
	}
	return job, revision, true, nil
}

func (s Store) Load(jobID string) (job Job, revision int, found bool, retErr error) {
	if err := validateStoreID(jobID, "job"); err != nil {
		return Job{}, 0, false, err
	}
	layout, err := s.openLayout(false)
	if err != nil || layout == nil {
		return Job{}, 0, false, err
	}
	defer func() { retErr = errors.Join(retErr, layout.finish()) }()
	if layout.missing {
		return Job{}, 0, false, nil
	}
	return s.loadFromJobs(layout.jobs, jobID)
}

func (s Store) Update(jobID string, expectedRevision int, mutate func(*Job) error) (job Job, revision int, retErr error) {
	if err := validateStoreID(jobID, "job"); err != nil {
		return Job{}, 0, err
	}
	if expectedRevision < 1 || expectedRevision > maxSafeInteger || mutate == nil {
		return Job{}, 0, errors.New("valid expected revision and mutation are required")
	}
	layout, err := s.openLayout(false)
	if err != nil {
		return Job{}, 0, err
	}
	if layout == nil || layout.missing {
		if layout != nil {
			_ = layout.close()
		}
		return Job{}, 0, os.ErrNotExist
	}
	defer func() { retErr = errors.Join(retErr, layout.finish()) }()
	return s.updateLayout(layout, jobID, expectedRevision, mutate)
}

// updateLayout performs one CAS against an already physically pinned store
// layout. Review workers use this path for their full leased lifetime so a
// replacement Data directory can never receive an authoritative mutation.
func (s Store) updateLayout(layout *storeLayout, jobID string, expectedRevision int, mutate func(*Job) error) (job Job, revision int, retErr error) {
	return s.updateLayoutMode(layout, jobID, expectedRevision, mutate, true)
}

func (s Store) updatePinnedLayout(layout *storeLayout, jobID string, expectedRevision int, mutate func(*Job) error) (job Job, revision int, retErr error) {
	return s.updateLayoutMode(layout, jobID, expectedRevision, mutate, false)
}

func (s Store) updateLayoutMode(layout *storeLayout, jobID string, expectedRevision int, mutate func(*Job) error, requireNamespace bool) (job Job, revision int, retErr error) {
	if layout == nil || layout.missing {
		return Job{}, 0, os.ErrNotExist
	}
	verify := layout.verifyPinned
	if requireNamespace {
		verify = layout.verify
	}
	if err := verify(); err != nil {
		return Job{}, 0, err
	}
	lock, err := acquireStoreFileLock(layout.locks, storeLockName, 2*time.Second)
	if err != nil {
		return Job{}, 0, fmt.Errorf("lock review job store: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, lock.release()) }()
	if err := verify(); err != nil {
		return Job{}, 0, err
	}

	current, currentRevision, found, err := s.loadFromJobs(layout.jobs, jobID)
	if err != nil {
		return Job{}, 0, err
	}
	if !found {
		return Job{}, 0, os.ErrNotExist
	}
	if currentRevision != expectedRevision {
		return Job{}, currentRevision, ErrStaleRevision
	}
	if currentRevision == maxSafeInteger {
		return Job{}, currentRevision, ErrRevisionExhausted
	}
	backup := current
	backup.FrozenSessions = append([]FrozenSession(nil), current.FrozenSessions...)
	backup.ReviewAccounting = cloneReviewAccounting(current.ReviewAccounting)
	backup.PayloadPublications = append([]PayloadPublication(nil), current.PayloadPublications...)
	next := current
	next.FrozenSessions = append([]FrozenSession(nil), current.FrozenSessions...)
	next.ReviewAccounting = cloneReviewAccounting(current.ReviewAccounting)
	next.PayloadPublications = append([]PayloadPublication(nil), current.PayloadPublications...)
	if err := mutate(&next); err != nil {
		return Job{}, currentRevision, err
	}
	if next.ID != current.ID || next.ProjectID != current.ProjectID || next.ProjectIdentity != current.ProjectIdentity {
		return Job{}, currentRevision, errors.New("review job stable identity cannot change")
	}
	if err := Validate(next); err != nil {
		return Job{}, currentRevision, fmt.Errorf("invalid updated review job: %w", err)
	}
	if err := validateReviewAccountingTransition(current.ReviewAccounting, next.ReviewAccounting); err != nil {
		return Job{}, currentRevision, fmt.Errorf("invalid review accounting update: %w", err)
	}
	if err := verify(); err != nil {
		return Job{}, currentRevision, err
	}
	if current.LaunchTokenDigest != "" && next.LaunchTokenDigest == "" {
		// Once an ownership or cancellation CAS consumes launch authority, no
		// authenticated recovery record may resurrect the one-use token.
		backup = next
	}
	backupEncoded, err := marshalCanonical(storedJob{Revision: currentRevision, Job: backup}, maxJobRecordBytes)
	if err != nil {
		return Job{}, currentRevision, err
	}
	if err := s.writer()(layout.jobs, jobID+".json.bak", backupEncoded, 0o600); err != nil {
		return Job{}, currentRevision, fmt.Errorf("persist review job recovery backup: %w", err)
	}
	nextRevision := currentRevision + 1
	encoded, err := marshalCanonical(storedJob{Revision: nextRevision, Job: next}, maxJobRecordBytes)
	if err != nil {
		return Job{}, currentRevision, err
	}
	if err := s.writer()(layout.jobs, jobID+".json", encoded, 0o600); err != nil {
		return Job{}, currentRevision, fmt.Errorf("persist review job update: %w", err)
	}
	if err := verify(); err != nil {
		return Job{}, currentRevision, err
	}
	written, writtenRevision, found, err := s.loadFromJobs(layout.jobs, jobID)
	if err != nil || !found || writtenRevision != nextRevision || !reflect.DeepEqual(written, next) {
		return Job{}, currentRevision, errors.New("review job update failed canonical post-write verification")
	}
	return written, writtenRevision, nil
}

func (s Store) LatestForProject(projectID string) (job Job, revision int, found bool, retErr error) {
	if err := validateStoreID(projectID, "project"); err != nil {
		return Job{}, 0, false, err
	}
	layout, err := s.openLayout(false)
	if err != nil || layout == nil {
		return Job{}, 0, false, err
	}
	defer func() { retErr = errors.Join(retErr, layout.finish()) }()
	if layout.missing {
		return Job{}, 0, false, nil
	}
	if err := rejectCaseCollision(layout.projects, projectID+".json"); err != nil {
		return Job{}, 0, false, err
	}
	pointer, pointerFound, err := readProjectPointer(layout.projects, projectID)
	if err != nil {
		return Job{}, 0, false, err
	}
	if pointerFound {
		return s.loadAuthenticatedPointer(layout.jobs, pointer, projectID)
	}

	lock, err := acquireStoreFileLock(layout.locks, storeLockName, 2*time.Second)
	if err != nil {
		return Job{}, 0, false, fmt.Errorf("lock review job store: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, lock.release()) }()
	pointer, pointerFound, err = readProjectPointer(layout.projects, projectID)
	if err != nil {
		return Job{}, 0, false, err
	}
	if pointerFound {
		return s.loadAuthenticatedPointer(layout.jobs, pointer, projectID)
	}
	job, revision, found, err = s.latestJobByEnumeration(layout.jobs, projectID)
	if err != nil || !found {
		return job, revision, found, err
	}
	if err := s.publishPointer(layout.projects, projectPointerFor(job)); err != nil {
		return Job{}, 0, false, err
	}
	return job, revision, true, nil
}

func (s Store) loadAuthenticatedPointer(jobs *os.Root, pointer projectPointer, projectID string) (Job, int, bool, error) {
	if pointer.ProjectID != projectID {
		return Job{}, 0, false, errors.New("project pointer names another project")
	}
	job, revision, found, err := s.loadFromJobs(jobs, pointer.JobID)
	if err != nil || !found {
		if err != nil {
			return Job{}, 0, false, err
		}
		return Job{}, 0, false, errors.New("project pointer names a missing job")
	}
	if job.ProjectID != projectID || job.ProjectIdentity != pointer.ProjectIdentity {
		return Job{}, 0, false, errors.New("project pointer is not authenticated by its job")
	}
	return job, revision, true, nil
}

func (s Store) latestJobByEnumeration(jobs *os.Root, projectID string) (Job, int, bool, error) {
	pairs, err := s.snapshotJobDirectory(jobs)
	if err != nil {
		return Job{}, 0, false, fmt.Errorf("enumerate review jobs: %w", err)
	}
	var selected Job
	selectedRevision := 0
	for _, pair := range pairs {
		if pair.identityInfo == nil {
			return Job{}, 0, false, errors.New("review job state lacks immutable identity authority")
		}
		authority, err := s.readJobIdentityInfo(jobs, pair.identityName, pair.id, pair.identityInfo)
		if err != nil {
			return Job{}, 0, false, err
		}
		if pair.primaryInfo == nil && pair.backupInfo == nil {
			continue
		}
		var record storedJob
		var primaryErr error
		if pair.primaryInfo != nil {
			record, primaryErr = s.readStoredJobInfo(jobs, pair.primaryName, pair.id, pair.primaryInfo)
			if primaryErr == nil {
				primaryErr = authenticateStoredJob(record, authority)
			}
		}
		if pair.primaryInfo == nil || primaryErr != nil {
			if pair.backupInfo == nil {
				return Job{}, 0, false, errors.New("review job and recovery backup are corrupt")
			}
			record, err = s.readStoredJobInfo(jobs, pair.backupName, pair.id, pair.backupInfo)
			if err != nil {
				return Job{}, 0, false, errors.New("review job and recovery backup are corrupt")
			}
			if err := authenticateStoredJob(record, authority); err != nil {
				return Job{}, 0, false, err
			}
		}
		candidate, candidateRevision := record.Job, record.Revision
		if candidate.ID != pair.id {
			return Job{}, 0, false, errors.New("review job filename does not authenticate its record")
		}
		if candidate.ProjectID != projectID {
			continue
		}
		if selectedRevision == 0 || candidate.CreatedAt.After(selected.CreatedAt) || (candidate.CreatedAt.Equal(selected.CreatedAt) && candidate.ID > selected.ID) {
			selected, selectedRevision = candidate, candidateRevision
		}
	}
	return selected, selectedRevision, selectedRevision != 0, nil
}

func (s Store) snapshotJobDirectory(jobs *os.Root) ([]jobStatePair, error) {
	entries, err := readBoundedEntries(jobs, maxJobDirectoryEntries)
	if err != nil {
		return nil, err
	}
	if s.afterJobDirectoryScan != nil {
		if err := s.afterJobDirectoryScan(); err != nil {
			return nil, err
		}
	}
	seenNames := make(map[string]string, len(entries))
	pairsByID := make(map[string]jobStatePair, len(entries))
	var aggregate int64
	for _, entry := range entries {
		name := entry.Name()
		folded := strings.ToLower(name)
		if previous, found := seenNames[folded]; found && previous != name {
			return nil, errors.New("review job filenames collide case-insensitively")
		}
		seenNames[folded] = name
		base := name
		kind := "primary"
		if strings.HasSuffix(base, ".identity.json") {
			base = strings.TrimSuffix(base, ".identity.json") + ".json"
			kind = "identity"
		} else if strings.HasSuffix(base, ".json.bak") {
			base = strings.TrimSuffix(base, ".bak")
			kind = "backup"
		}
		if !strings.HasSuffix(base, ".json") {
			return nil, errors.New("invalid review job filename")
		}
		id := strings.TrimSuffix(base, ".json")
		if err := validateStoreID(id, "job"); err != nil {
			return nil, errors.New("invalid review job filename")
		}
		info, found, err := regularPrivateEntry(jobs, name)
		maxBytes := int64(maxJobRecordBytes)
		if kind == "identity" {
			maxBytes = maxJobIdentityBytes
		}
		if err != nil || !found || info.Size() > maxBytes {
			return nil, errors.New("review job repair entry is invalid, redirected, or oversized")
		}
		if info.Size() > maxJobRepairBytes-aggregate {
			return nil, errors.New("review job repair exceeds aggregate record-byte budget")
		}
		aggregate += info.Size()
		pair := pairsByID[id]
		pair.id = id
		switch kind {
		case "identity":
			pair.identityName, pair.identityInfo = name, info
		case "backup":
			pair.backupName, pair.backupInfo = name, info
		default:
			pair.primaryName, pair.primaryInfo = name, info
		}
		pairsByID[id] = pair
	}
	result := make([]jobStatePair, 0, len(pairsByID))
	for _, pair := range pairsByID {
		result = append(result, pair)
	}
	sort.Slice(result, func(first, second int) bool { return result[first].id < result[second].id })
	return result, nil
}

func (s Store) loadFromJobs(jobs *os.Root, jobID string) (Job, int, bool, error) {
	if err := rejectCaseCollision(jobs, jobIdentityName(jobID), jobID+".json", jobID+".json.bak"); err != nil {
		return Job{}, 0, false, err
	}
	primaryName, backupName := jobID+".json", jobID+".json.bak"
	authority, authorityFound, authorityErr := s.readJobIdentity(jobs, jobID)
	if authorityErr != nil {
		return Job{}, 0, false, authorityErr
	}
	if _, _, err := regularPrivateEntry(jobs, backupName); err != nil {
		return Job{}, 0, false, err
	}
	primary, primaryFound, primaryErr := s.readStoredJob(jobs, primaryName, jobID)
	if primaryFound && primaryErr == nil {
		if !authorityFound {
			primaryErr = errors.New("review job state lacks immutable identity authority")
		} else {
			primaryErr = authenticateStoredJob(primary, authority)
		}
	}
	if primaryFound && primaryErr == nil {
		return primary.Job, primary.Revision, true, nil
	}
	backup, backupFound, backupErr := s.readStoredJob(jobs, backupName, jobID)
	if backupFound && backupErr == nil {
		if !authorityFound {
			return Job{}, 0, false, errors.New("review job recovery backup lacks immutable identity authority")
		}
		if err := authenticateStoredJob(backup, authority); err != nil {
			return Job{}, 0, false, err
		}
		return backup.Job, backup.Revision, true, nil
	}
	if !primaryFound && !backupFound {
		return Job{}, 0, false, nil
	}
	return Job{}, 0, false, errors.New("review job and recovery backup are corrupt")
}

func (s Store) ensureJobIdentity(jobs *os.Root, job Job) (bool, error) {
	expected := jobIdentityFor(job)
	current, found, err := s.readJobIdentity(jobs, job.ID)
	if err != nil {
		return false, err
	}
	if found {
		if current != expected {
			return false, errors.New("immutable review job identity does not match create request")
		}
		return false, nil
	}
	for _, name := range []string{job.ID + ".json", job.ID + ".json.bak"} {
		if _, mutableFound, err := regularPrivateEntry(jobs, name); err != nil {
			return false, err
		} else if mutableFound {
			return false, errors.New("review job state exists without immutable identity authority")
		}
	}
	body, err := marshalCanonical(expected, maxJobIdentityBytes)
	if err != nil {
		return false, err
	}
	name := jobIdentityName(job.ID)
	err = atomicfile.WriteRootFileCreateIfAbsent(jobs, name, body, 0o600, nil)
	created := err == nil
	if err != nil && !errors.Is(err, os.ErrExist) {
		return false, fmt.Errorf("persist immutable review job identity: %w", err)
	}
	written, found, readErr := s.readJobIdentity(jobs, job.ID)
	if readErr != nil || !found || written != expected {
		return false, errors.New("immutable review job identity failed canonical post-write verification")
	}
	return created, nil
}

func (s Store) readJobIdentity(root *os.Root, jobID string) (jobIdentity, bool, error) {
	name := jobIdentityName(jobID)
	info, found, err := regularPrivateEntry(root, name)
	if err != nil || !found {
		return jobIdentity{}, found, err
	}
	authority, err := s.readJobIdentityInfo(root, name, jobID, info)
	return authority, true, err
}

func (s Store) readJobIdentityInfo(root *os.Root, name, jobID string, info os.FileInfo) (jobIdentity, error) {
	body, err := readStableCanonical(root, name, info, maxJobIdentityBytes, s.afterJobIdentityRead)
	if err != nil {
		return jobIdentity{}, err
	}
	var authority jobIdentity
	if err := decodeStrictCanonical(body, &authority); err != nil {
		return jobIdentity{}, errors.New("immutable review job identity is corrupt")
	}
	if authority.SchemaVersion != PublicStatusSchemaVersion || authority.JobID != jobID || validateStoreID(authority.JobID, "job") != nil || validateStoreID(authority.ProjectID, "project") != nil || !authority.ProjectIdentity.Valid() {
		return jobIdentity{}, errors.New("immutable review job identity is invalid")
	}
	return authority, nil
}

func authenticateStoredJob(record storedJob, authority jobIdentity) error {
	if record.Job.ID != authority.JobID || record.Job.ProjectID != authority.ProjectID || record.Job.ProjectIdentity != authority.ProjectIdentity {
		return errors.New("review job state does not match immutable identity authority")
	}
	return nil
}

func jobIdentityFor(job Job) jobIdentity {
	return jobIdentity{SchemaVersion: PublicStatusSchemaVersion, JobID: job.ID, ProjectID: job.ProjectID, ProjectIdentity: job.ProjectIdentity}
}

func jobIdentityName(jobID string) string { return jobID + ".identity.json" }

func (s Store) readStoredJob(root *os.Root, name, jobID string) (storedJob, bool, error) {
	info, found, err := regularPrivateEntry(root, name)
	if err != nil || !found {
		return storedJob{}, found, err
	}
	record, err := s.readStoredJobInfo(root, name, jobID, info)
	return record, true, err
}

func (s Store) readStoredJobInfo(root *os.Root, name, jobID string, info os.FileInfo) (storedJob, error) {
	body, err := readStableCanonical(root, name, info, maxJobRecordBytes, s.afterJobRead)
	if err != nil {
		return storedJob{}, err
	}
	var record storedJob
	if currentErr := decodeStrictCanonical(body, &record); currentErr != nil {
		var legacy legacyStoredJobV1
		if legacyErr := decodeStrictCanonical(body, &legacy); legacyErr != nil {
			return storedJob{}, errors.New("review job state is corrupt")
		}
		record, err = migrateLegacyStoredJobV1(legacy)
		if err != nil {
			return storedJob{}, fmt.Errorf("legacy review job state is invalid: %w", err)
		}
	}
	if record.Revision < 1 || record.Revision > maxSafeInteger || record.Job.ID != jobID {
		return storedJob{}, errors.New("review job state identity or revision is invalid")
	}
	if err := Validate(record.Job); err != nil {
		return storedJob{}, fmt.Errorf("review job state is invalid: %w", err)
	}
	return record, nil
}

func (s Store) publishPointer(root *os.Root, pointer projectPointer) error {
	if err := rejectCaseCollision(root, pointer.ProjectID+".json"); err != nil {
		return err
	}
	encoded, err := marshalCanonical(pointer, maxProjectPointerBytes)
	if err != nil {
		return err
	}
	if err := s.writer()(root, pointer.ProjectID+".json", encoded, 0o600); err != nil {
		return fmt.Errorf("persist project review pointer: %w", err)
	}
	written, found, err := readProjectPointer(root, pointer.ProjectID)
	if err != nil || !found || written != pointer {
		return errors.New("project review pointer failed canonical post-write verification")
	}
	return nil
}

func readProjectPointer(root *os.Root, projectID string) (projectPointer, bool, error) {
	info, found, err := regularPrivateEntry(root, projectID+".json")
	if err != nil || !found {
		return projectPointer{}, found, err
	}
	body, err := readStableCanonical(root, projectID+".json", info, maxProjectPointerBytes, nil)
	if err != nil {
		return projectPointer{}, true, err
	}
	var pointer projectPointer
	if err := decodeStrictCanonical(body, &pointer); err != nil || pointer.SchemaVersion != PublicStatusSchemaVersion || pointer.ProjectID != projectID || !pointer.ProjectIdentity.Valid() || validateStoreID(pointer.JobID, "job") != nil {
		return projectPointer{}, true, errors.New("project review pointer is corrupt")
	}
	return pointer, true, nil
}

func projectPointerFor(job Job) projectPointer {
	return projectPointer{SchemaVersion: PublicStatusSchemaVersion, ProjectID: job.ProjectID, ProjectIdentity: job.ProjectIdentity, JobID: job.ID}
}

func (s Store) openLayout(create bool) (*storeLayout, error) {
	data, err := pathguard.Open(s.Root)
	if err != nil {
		return nil, fmt.Errorf("open review job data root: %w", err)
	}
	layout := &storeLayout{data: data}
	review, found, err := openPrivateDirectory(data.Root, "review-jobs", create)
	if err != nil {
		_ = layout.close()
		return nil, err
	}
	if !found {
		layout.missing = true
		return layout, nil
	}
	layout.review = review
	for _, item := range []struct {
		name string
		dst  **os.Root
	}{
		{"jobs", &layout.jobs}, {"projects", &layout.projects}, {"locks", &layout.locks}, {"work", &layout.work},
	} {
		child, _, err := openPrivateDirectory(layout.review, item.name, create)
		if err != nil {
			_ = layout.close()
			return nil, err
		}
		if child == nil {
			_ = layout.close()
			return nil, fmt.Errorf("review job %s directory is missing", item.name)
		}
		*item.dst = child
	}
	projectLocks, _, err := openPrivateDirectory(layout.locks, "projects", create)
	if err != nil || projectLocks == nil {
		_ = layout.close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("review job project lock directory is missing")
	}
	layout.projectLocks = projectLocks
	return layout, nil
}

func openPrivateDirectory(parent *os.Root, name string, create bool) (*os.Root, bool, error) {
	before, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && !create {
		return nil, false, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		if err := atomicfile.EnsureRootDir(parent, name, 0o700); err != nil {
			return nil, false, fmt.Errorf("create private directory %s: %w", name, err)
		}
		before, err = parent.Lstat(name)
	}
	if err != nil || before == nil || !before.IsDir() || isRedirect(before) || !privateMode(before, 0o700) {
		return nil, true, fmt.Errorf("private directory %s is redirected, invalid, or has weak permissions", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, true, fmt.Errorf("open private directory %s: %w", name, err)
	}
	opened, err := child.Stat(".")
	after, afterErr := parent.Lstat(name)
	if err != nil || afterErr != nil || !os.SameFile(before, opened) || !os.SameFile(before, after) || !privateMode(opened, 0o700) || isRedirect(after) {
		_ = child.Close()
		return nil, true, fmt.Errorf("private directory %s changed while opening", name)
	}
	return child, true, nil
}

func ensurePrivateDirectory(parent *os.Root, name string) (*os.Root, error) {
	child, _, err := openPrivateDirectory(parent, name, true)
	return child, err
}

func (layout *storeLayout) close() error {
	if layout == nil {
		return nil
	}
	var err error
	for _, root := range []*os.Root{layout.projectLocks, layout.work, layout.locks, layout.projects, layout.jobs, layout.review} {
		if root != nil {
			err = errors.Join(err, root.Close())
		}
	}
	if layout.data != nil {
		err = errors.Join(err, layout.data.Close())
	}
	return err
}

func (layout *storeLayout) finish() error {
	if layout == nil {
		return nil
	}
	return errors.Join(layout.verify(), layout.close())
}

func (layout *storeLayout) verify() error {
	if layout == nil || layout.data == nil {
		return errors.New("review job store layout is not pinned")
	}
	reopened, err := pathguard.Open(layout.data.Path)
	if err != nil {
		return errors.New("review job data root changed during operation")
	}
	reopenedInfo := reopened.Info()
	closeErr := reopened.Close()
	if closeErr != nil || reopenedInfo == nil || !os.SameFile(layout.data.Info(), reopenedInfo) {
		return errors.New("review job data root changed during operation")
	}
	return layout.verifyPinned()
}

func (layout *storeLayout) verifyPinned() error {
	if layout == nil || layout.data == nil || layout.data.Root == nil {
		return errors.New("review job store layout is not pinned")
	}
	pinnedData, err := layout.data.Root.Stat(".")
	if err != nil || pinnedData == nil || !os.SameFile(layout.data.Info(), pinnedData) {
		return errors.New("review job pinned data root changed during operation")
	}
	if layout.missing {
		return nil
	}
	checks := []struct {
		parent *os.Root
		name   string
		child  *os.Root
	}{
		{layout.data.Root, "review-jobs", layout.review},
		{layout.review, "jobs", layout.jobs},
		{layout.review, "projects", layout.projects},
		{layout.review, "locks", layout.locks},
		{layout.review, "work", layout.work},
		{layout.locks, "projects", layout.projectLocks},
	}
	for _, check := range checks {
		named, nameErr := check.parent.Lstat(check.name)
		pinned, pinErr := check.child.Stat(".")
		if nameErr != nil || pinErr != nil || !os.SameFile(named, pinned) || !named.IsDir() || isRedirect(named) || !privateMode(named, 0o700) || !privateMode(pinned, 0o700) {
			return errors.New("review job store namespace changed during operation")
		}
	}
	return nil
}

func (s Store) writer() func(*os.Root, string, []byte, fs.FileMode) error {
	if s.writeRoot != nil {
		return s.writeRoot
	}
	return atomicfile.WriteRoot
}

func regularPrivateEntry(root *os.Root, name string) (os.FileInfo, bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if isRedirect(info) || !info.Mode().IsRegular() || !privateMode(info, 0o600) {
		return nil, true, errors.New("private store entry is redirected, invalid, or has weak permissions")
	}
	return info, true, nil
}

func readStableCanonical(root *os.Root, name string, before os.FileInfo, max int64, afterFirstRead func() error) ([]byte, error) {
	if before == nil || before.Size() > max {
		return nil, errors.New("private store entry exceeds size limit")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameFileMetadata(before, opened) {
		return nil, errors.New("private store entry changed while opening")
	}
	first, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil || int64(len(first)) > max || !utf8.Valid(first) {
		return nil, errors.New("private store entry is corrupt or oversized")
	}
	if afterFirstRead != nil {
		if err := afterFirstRead(); err != nil {
			return nil, err
		}
	}
	middle, err := file.Stat()
	if err != nil || !sameFileMetadata(opened, middle) {
		return nil, errors.New("private store entry changed while reading")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	second, err := io.ReadAll(io.LimitReader(file, max+1))
	afterOpen, statErr := file.Stat()
	afterName, nameErr := root.Lstat(name)
	if err != nil || statErr != nil || nameErr != nil || int64(len(second)) > max || !bytes.Equal(first, second) || !sameFileMetadata(opened, afterOpen) || !sameFileMetadata(opened, afterName) || isRedirect(afterName) {
		return nil, errors.New("private store entry changed while reading")
	}
	return first, nil
}

func sameFileMetadata(first, second os.FileInfo) bool {
	return first != nil && second != nil && os.SameFile(first, second) && first.Size() == second.Size() && first.Mode() == second.Mode() && first.ModTime().Equal(second.ModTime())
}

func marshalCanonical(value any, max int) ([]byte, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, errors.New("cannot encode private store state")
	}
	body = append(body, '\n')
	if len(body) > max {
		return nil, errors.New("private store state exceeds size limit")
	}
	return body, nil
}

func decodeStrictCanonical(body []byte, destination any) error {
	if err := rejectDuplicateJSONFields(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON content")
	}
	canonical, err := json.MarshalIndent(destination, "", "  ")
	if err != nil {
		return err
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(body, canonical) {
		return errors.New("JSON is not canonical")
	}
	return nil
}

func rejectDuplicateJSONFields(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON content")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return errors.New("invalid JSON object")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate JSON field")
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func rejectCaseCollision(root *os.Root, allowed ...string) error {
	entries, err := readBoundedEntries(root, maxProjectPointerEntries)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		for _, name := range allowed {
			if strings.EqualFold(entry.Name(), name) && entry.Name() != name {
				return errors.New("private store names collide case-insensitively")
			}
		}
	}
	return nil
}

func readBoundedEntries(root *os.Root, limit int) ([]fs.DirEntry, error) {
	if root == nil || limit <= 0 {
		return nil, errors.New("invalid private store directory budget")
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(limit + 1)
	closeErr := directory.Close()
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errors.New("private store directory exceeds entry limit")
	}
	return entries, nil
}

func validateStoreID(value, label string) error {
	if !validID(value) || windowsReservedName(value) || (label == "job" && strings.HasSuffix(value, ".identity")) {
		return fmt.Errorf("invalid %s ID", label)
	}
	return nil
}

func windowsReservedName(name string) bool {
	base := strings.ToUpper(strings.Split(strings.TrimRight(name, " ."), ".")[0])
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}

func privateMode(info os.FileInfo, want fs.FileMode) bool {
	return runtime.GOOS == "windows" || info.Mode().Perm() == want
}

func isRedirect(info os.FileInfo) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() {
		return false
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	attributes := value.FieldByName("FileAttributes")
	return attributes.IsValid() && attributes.CanUint() && attributes.Uint()&0x400 != 0
}
