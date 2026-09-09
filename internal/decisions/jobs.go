package decisions

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/annotation"
	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

type ExtractionState string

const (
	ExtractionQueued    ExtractionState = "queued"
	ExtractionRunning   ExtractionState = "running"
	ExtractionCompleted ExtractionState = "completed"
	ExtractionFailed    ExtractionState = "failed"
	ExtractionCancelled ExtractionState = "cancelled"
)

var (
	ErrExtractionJobRevisionConflict = errors.New("extraction job revision conflict")
	extractionID                     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

type ExtractionJob struct {
	SchemaVersion     int             `json:"schema_version" required:"true"`
	JobID             string          `json:"job_id" required:"true"`
	ProjectID         string          `json:"project_id" required:"true"`
	GenerationID      string          `json:"generation_id" required:"true"`
	State             ExtractionState `json:"state" required:"true"`
	Revision          int             `json:"revision" required:"true"`
	PID               int             `json:"pid" required:"true"`
	DependencyDigests []string        `json:"dependency_digests" required:"true"`
	CandidateCount    int             `json:"candidate_count" required:"true"`
	ErrorCode         string          `json:"error_code" required:"true"`
	CreatedAt         string          `json:"created_at" required:"true"`
	UpdatedAt         string          `json:"updated_at" required:"true"`
}

type ExtractionJobStore struct {
	dataRoot string
	root     string
}

func OpenExtractionJobStore(dataRoot string) (*ExtractionJobStore, error) {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return nil, errors.New("extraction jobs require a clean absolute data root")
	}
	root := filepath.Join(dataRoot, "decision-extraction-jobs")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(root, 0o700); err != nil {
			return nil, err
		}
	}
	return &ExtractionJobStore{dataRoot: dataRoot, root: root}, nil
}

func (s *ExtractionJobStore) Create(job ExtractionJob) error {
	return s.withLock(func() error { return s.createUnlocked(job) })
}

func (s *ExtractionJobStore) createUnlocked(job ExtractionJob) error {
	if err := validateExtractionJob(job); err != nil {
		return err
	}
	path := s.path(job.JobID)
	if _, err := os.Lstat(path); err == nil {
		return ErrExtractionJobRevisionConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.write(job)
}

func (s *ExtractionJobStore) Load(jobID string) (ExtractionJob, error) {
	if !extractionID.MatchString(jobID) {
		return ExtractionJob{}, errors.New("invalid extraction job ID")
	}
	path := s.path(jobID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ExtractionJob{}, os.ErrNotExist
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return ExtractionJob{}, errors.Join(errors.New("extraction job is unavailable or unsafe"), err)
	}
	body, err := os.ReadFile(path)
	if err != nil || len(body) > 1<<20 {
		return ExtractionJob{}, errors.Join(errors.New("extraction job is unavailable or too large"), err)
	}
	var job ExtractionJob
	if err := strictjson.Decode(body, &job); err != nil {
		return ExtractionJob{}, err
	}
	if err := validateExtractionJob(job); err != nil || job.JobID != jobID {
		return ExtractionJob{}, errors.Join(errors.New("invalid extraction job"), err)
	}
	return job, nil
}

func (s *ExtractionJobStore) Latest(projectID string) (*ExtractionJob, error) {
	if !extractionID.MatchString(projectID) {
		return nil, errors.New("invalid extraction project ID")
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	var latest *ExtractionJob
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		job, loadErr := s.Load(strings.TrimSuffix(entry.Name(), ".json"))
		if loadErr != nil {
			return nil, loadErr
		}
		if job.ProjectID == projectID && (latest == nil || job.CreatedAt > latest.CreatedAt || job.CreatedAt == latest.CreatedAt && job.JobID > latest.JobID) {
			copy := job
			latest = &copy
		}
	}
	return latest, nil
}

func (s *ExtractionJobStore) CompareAndSwap(job ExtractionJob, expectedRevision int) error {
	return s.withLock(func() error { return s.compareAndSwapUnlocked(job, expectedRevision) })
}

func (s *ExtractionJobStore) Retry(jobID string, expectedRevision int, generationID string, now time.Time) (ExtractionJob, error) {
	var current ExtractionJob
	err := s.withLock(func() error {
		var err error
		current, err = s.Load(jobID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision || current.State != ExtractionFailed && current.State != ExtractionCancelled || !extractionID.MatchString(generationID) {
			return ErrExtractionJobRevisionConflict
		}
		current.GenerationID = generationID
		current.State, current.PID, current.CandidateCount, current.ErrorCode = ExtractionQueued, 0, 0, ""
		current.Revision++
		current.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
		return s.write(current)
	})
	return current, err
}

func (s *ExtractionJobStore) compareAndSwapUnlocked(job ExtractionJob, expectedRevision int) error {
	current, err := s.Load(job.JobID)
	if err != nil {
		return err
	}
	if current.Revision != expectedRevision || job.Revision != expectedRevision+1 || current.ProjectID != job.ProjectID || current.GenerationID != job.GenerationID || ExtractionIdentity(current.ProjectID, current.DependencyDigests) != job.JobID {
		return ErrExtractionJobRevisionConflict
	}
	return s.write(job)
}

func (s *ExtractionJobStore) withLock(operation func() error) (retErr error) {
	return withDecisionControlLock(s.dataRoot, operation)
}

func (s *ExtractionJobStore) Cancel(jobID string, expectedRevision int, now time.Time) (ExtractionJob, error) {
	var current ExtractionJob
	pid := 0
	err := s.withLock(func() error {
		var err error
		current, err = s.Load(jobID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision || current.State != ExtractionQueued && current.State != ExtractionRunning {
			return ErrExtractionJobRevisionConflict
		}
		pid = current.PID
		current.State, current.PID, current.Revision, current.UpdatedAt = ExtractionCancelled, 0, current.Revision+1, now.UTC().Format(time.RFC3339Nano)
		return s.compareAndSwapUnlocked(current, expectedRevision)
	})
	if err != nil {
		return ExtractionJob{}, err
	}
	// The durable cancellation wins before signalling the exact PID authorized by
	// the revision above. A stale caller therefore cannot terminate a newer job.
	if pid > 0 {
		if process, findErr := os.FindProcess(pid); findErr == nil {
			_ = terminateExtractionProcess(process)
		}
	}
	return current, nil
}

// Complete serializes the externally visible candidate commit with the job's
// terminal transition. The callback must be idempotent so a crash between the
// callback and the job write can safely retry the same extraction run.
func (s *ExtractionJobStore) Complete(jobID string, expectedRevision, candidateCount int, now time.Time, commit func() error) (ExtractionJob, error) {
	if commit == nil || candidateCount < 0 {
		return ExtractionJob{}, errors.New("invalid extraction completion")
	}
	var current ExtractionJob
	err := s.withLock(func() error {
		var err error
		current, err = s.Load(jobID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision || current.State != ExtractionRunning {
			return ErrExtractionJobRevisionConflict
		}
		if err := commit(); err != nil {
			return err
		}
		current.State, current.PID, current.CandidateCount, current.Revision, current.UpdatedAt = ExtractionCompleted, 0, candidateCount, current.Revision+1, now.UTC().Format(time.RFC3339Nano)
		return s.compareAndSwapUnlocked(current, expectedRevision)
	})
	return current, err
}

func (s *ExtractionJobStore) CompleteExtraction(jobID string, expectedRevision int, candidateStore *Store, run annotation.Run, candidates []annotation.Annotation, now time.Time) (ExtractionJob, error) {
	if candidateStore == nil || candidateStore.dataRoot != s.dataRoot {
		return ExtractionJob{}, errors.New("invalid extraction candidate store")
	}
	var current ExtractionJob
	err := s.withLock(func() error {
		var err error
		current, err = s.Load(jobID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision || current.State != ExtractionRunning || current.ProjectID != candidateStore.projectID || run.RunID != jobID {
			return ErrExtractionJobRevisionConflict
		}
		if err := candidateStore.mutateUnlocked(func(record *annotation.StoreRecord, _ bool) error {
			return applyExtraction(record, candidateStore.projectID, run, candidates)
		}); err != nil {
			return err
		}
		current.State, current.PID, current.CandidateCount, current.Revision, current.UpdatedAt = ExtractionCompleted, 0, len(candidates), current.Revision+1, now.UTC().Format(time.RFC3339Nano)
		return s.compareAndSwapUnlocked(current, expectedRevision)
	})
	return current, err
}

func (s *ExtractionJobStore) AuthorizeWorker(jobID string, pid, expectedRevision int, now time.Time) (ExtractionJob, error) {
	current, err := s.Load(jobID)
	if err != nil {
		return ExtractionJob{}, err
	}
	if current.State == ExtractionRunning && current.PID == pid && current.Revision == expectedRevision+1 {
		return current, nil
	}
	if current.State != ExtractionQueued || current.PID != 0 || current.Revision != expectedRevision || pid <= 0 {
		return ExtractionJob{}, ErrExtractionJobRevisionConflict
	}
	current.State, current.PID, current.Revision, current.UpdatedAt = ExtractionRunning, pid, expectedRevision+1, now.UTC().Format(time.RFC3339Nano)
	if err := s.CompareAndSwap(current, expectedRevision); err != nil {
		return ExtractionJob{}, err
	}
	return current, nil
}

func (s *ExtractionJobStore) path(id string) string { return filepath.Join(s.root, id+".json") }
func (s *ExtractionJobStore) write(job ExtractionJob) error {
	body, err := strictjson.Encode(job)
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path(job.JobID), body, 0o600)
}

type StartExtractionOptions struct {
	DataRoot             string
	ProjectID            string
	GenerationID         string
	NewDependencyDigests []string
	Now                  func() time.Time
	Launch               func(ExtractionJob) (int, error)
}

func StartExtraction(options StartExtractionOptions) (ExtractionJob, error) {
	if options.Launch == nil {
		return ExtractionJob{}, errors.New("extraction worker launcher is required")
	}
	store, err := OpenExtractionJobStore(options.DataRoot)
	if err != nil {
		return ExtractionJob{}, err
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	id := ExtractionIdentity(options.ProjectID, options.NewDependencyDigests)
	var job ExtractionJob
	if existing, loadErr := store.Load(id); loadErr == nil {
		if existing.State != ExtractionFailed && existing.State != ExtractionCancelled {
			return existing, nil
		}
		job, err = store.Retry(id, existing.Revision, options.GenerationID, now())
		if err != nil {
			if errors.Is(err, ErrExtractionJobRevisionConflict) {
				return store.Load(id)
			}
			return ExtractionJob{}, err
		}
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return ExtractionJob{}, loadErr
	} else {
		createdAt := now().UTC()
		job = ExtractionJob{SchemaVersion: 1, JobID: id, ProjectID: options.ProjectID, GenerationID: options.GenerationID, State: ExtractionQueued, Revision: 1, DependencyDigests: append([]string{}, options.NewDependencyDigests...), CreatedAt: createdAt.Format(time.RFC3339Nano), UpdatedAt: createdAt.Format(time.RFC3339Nano)}
		if len(job.DependencyDigests) == 0 {
			job.State = ExtractionCompleted
			if err := store.Create(job); err != nil {
				if errors.Is(err, ErrExtractionJobRevisionConflict) {
					return store.Load(id)
				}
				return ExtractionJob{}, err
			}
			return job, nil
		}
		if err := store.Create(job); err != nil {
			if errors.Is(err, ErrExtractionJobRevisionConflict) {
				return store.Load(id)
			}
			return ExtractionJob{}, err
		}
	}
	queuedRevision := job.Revision
	pid, err := options.Launch(job)
	if err != nil || pid <= 0 {
		job.Revision++
		job.UpdatedAt = now().UTC().Format(time.RFC3339Nano)
		job.State, job.ErrorCode = ExtractionFailed, "worker_spawn_failed"
		if saveErr := store.CompareAndSwap(job, queuedRevision); saveErr != nil {
			return ExtractionJob{}, errors.Join(err, saveErr)
		}
		return job, err
	}
	return store.AuthorizeWorker(job.JobID, pid, queuedRevision, now())
}

func validateExtractionJob(job ExtractionJob) error {
	createdAt, createdErr := time.Parse(time.RFC3339Nano, job.CreatedAt)
	updatedAt, updatedErr := time.Parse(time.RFC3339Nano, job.UpdatedAt)
	if job.SchemaVersion != 1 || !extractionID.MatchString(job.JobID) || !extractionID.MatchString(job.ProjectID) || !extractionID.MatchString(job.GenerationID) || job.Revision < 1 || job.Revision > 1<<53-1 || job.PID < 0 || job.DependencyDigests == nil || len(job.DependencyDigests) > 256 || createdErr != nil || updatedErr != nil || updatedAt.Before(createdAt) || ExtractionIdentity(job.ProjectID, job.DependencyDigests) != job.JobID {
		return errors.New("invalid extraction job")
	}
	switch job.State {
	case ExtractionQueued:
		if job.PID != 0 || job.ErrorCode != "" {
			return errors.New("invalid queued extraction job")
		}
	case ExtractionRunning:
		if job.PID <= 0 || job.ErrorCode != "" {
			return errors.New("invalid running extraction job")
		}
	case ExtractionCompleted:
		if job.PID != 0 || job.ErrorCode != "" {
			return errors.New("invalid completed extraction job")
		}
	case ExtractionFailed:
		if job.PID != 0 || !extractionID.MatchString(job.ErrorCode) {
			return errors.New("invalid failed extraction job")
		}
	case ExtractionCancelled:
		if job.PID != 0 || job.ErrorCode != "" {
			return errors.New("invalid cancelled extraction job")
		}
	default:
		return errors.New("invalid extraction job state")
	}
	for _, digest := range job.DependencyDigests {
		if len(digest) != 71 || !strings.HasPrefix(digest, "sha256:") {
			return errors.New("invalid extraction dependency digest")
		}
	}
	return nil
}
