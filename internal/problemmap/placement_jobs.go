package problemmap

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

type PlacementJobState string

const (
	PlacementQueued          PlacementJobState = "queued"
	PlacementRunning         PlacementJobState = "running"
	PlacementCancelRequested PlacementJobState = "cancel_requested"
	PlacementCompleted       PlacementJobState = "completed"
	PlacementFailed          PlacementJobState = "failed"
	PlacementCancelled       PlacementJobState = "cancelled"
)

type PlacementJob struct {
	SchemaVersion              int               `json:"schema_version" required:"true"`
	JobID                      string            `json:"job_id" required:"true"`
	ProjectID                  string            `json:"project_id" required:"true"`
	CandidateID                string            `json:"candidate_id" required:"true"`
	AnalysisIdentity           string            `json:"analysis_identity" required:"true"`
	ExpectedCandidateRevision  int               `json:"expected_candidate_revision" required:"true"`
	ExpectedProblemMapRevision int               `json:"expected_problem_map_revision" required:"true"`
	ExpectedGenerationID       string            `json:"expected_generation_id" required:"true"`
	ResultCandidateRevision    int               `json:"result_candidate_revision" required:"true"`
	State                      PlacementJobState `json:"state" required:"true"`
	Revision                   int               `json:"revision" required:"true"`
	LaunchTokenSHA256          string            `json:"launch_token_sha256" required:"true"`
	ErrorCode                  *string           `json:"error_code" required:"true" nullable:"true"`
	CreatedAt                  string            `json:"created_at" required:"true"`
	UpdatedAt                  string            `json:"updated_at" required:"true"`
	LeaseExpiresAt             string            `json:"lease_expires_at" required:"true"`
}

type PlacementStatus struct {
	SchemaVersion           int               `json:"schema_version"`
	JobID                   string            `json:"job_id"`
	ProjectID               string            `json:"project_id"`
	CandidateID             string            `json:"candidate_id"`
	State                   PlacementJobState `json:"state"`
	Revision                int               `json:"revision"`
	ResultCandidateRevision int               `json:"result_candidate_revision"`
	ErrorCode               *string           `json:"error_code"`
	CanCancel               bool              `json:"can_cancel"`
}

type placementJobFile struct {
	SchemaVersion int            `json:"schema_version" required:"true"`
	ProjectID     string         `json:"project_id" required:"true"`
	Jobs          []PlacementJob `json:"jobs" required:"true"`
}

type PlacementJobStore struct {
	dataRoot, projectID, path string
	mu                        *sync.Mutex
}

var placementJobLocks sync.Map

func OpenPlacementJobStore(dataRoot, projectID string) (*PlacementJobStore, error) {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot || !validID(projectID) {
		return nil, errors.New("placement job store identity is invalid")
	}
	path := filepath.Join(dataRoot, "projects", projectID, "problem-placement-jobs.json")
	lock, _ := placementJobLocks.LoadOrStore(path, &sync.Mutex{})
	return &PlacementJobStore{dataRoot: dataRoot, projectID: projectID, path: path, mu: lock.(*sync.Mutex)}, nil
}

func (s *PlacementJobStore) Reserve(candidate Candidate, expectedCandidateRevision, expectedMapRevision int, generationID, launchToken string, now time.Time) (PlacementJob, bool, error) {
	if candidate.ProjectID != s.projectID || candidate.Revision != expectedCandidateRevision || expectedMapRevision < 0 || !validID(generationID) || len(launchToken) < 32 {
		return PlacementJob{}, false, errors.New("placement reservation identity is stale")
	}
	var result PlacementJob
	var cached bool
	err := s.mutate(func(file *placementJobFile) error {
		identity := AnalysisIdentity(s.projectID, candidate.Question, DeterministicRuleVersion+"+"+AgentProposalVersion, candidate.DependencyDigests)
		for _, job := range file.Jobs {
			if job.AnalysisIdentity == identity && job.CandidateID == candidate.CandidateID && job.ExpectedProblemMapRevision == expectedMapRevision && job.ExpectedGenerationID == generationID && (job.State == PlacementQueued || job.State == PlacementRunning || job.State == PlacementCancelRequested || job.State == PlacementCompleted) {
				result, cached = job, true
				return nil
			}
		}
		stamp := now.UTC().Round(0).Format(time.RFC3339Nano)
		tokenHash := sha256.Sum256([]byte(launchToken))
		sum := sha256.Sum256([]byte(identity + "\x00" + generationID + "\x00" + stamp))
		result = PlacementJob{SchemaVersion: 1, JobID: "problem-job-" + hex.EncodeToString(sum[:16]), ProjectID: s.projectID, CandidateID: candidate.CandidateID, AnalysisIdentity: identity, ExpectedCandidateRevision: expectedCandidateRevision, ExpectedProblemMapRevision: expectedMapRevision, ExpectedGenerationID: generationID, State: PlacementQueued, Revision: 1, LaunchTokenSHA256: hex.EncodeToString(tokenHash[:]), CreatedAt: stamp, UpdatedAt: stamp}
		file.Jobs = append(file.Jobs, result)
		return nil
	})
	return result, cached, err
}

func (s *PlacementJobStore) Get(jobID string) (PlacementJob, error) {
	var result PlacementJob
	err := s.read(func(file placementJobFile) error {
		for _, job := range file.Jobs {
			if job.JobID == jobID && job.ProjectID == s.projectID {
				result = job
				return nil
			}
		}
		return os.ErrNotExist
	})
	return result, err
}

func (s *PlacementJobStore) Status(jobID string) (PlacementStatus, error) {
	job, err := s.Get(jobID)
	if err != nil {
		return PlacementStatus{}, err
	}
	return PlacementStatus{SchemaVersion: 1, JobID: job.JobID, ProjectID: job.ProjectID, CandidateID: job.CandidateID, State: job.State, Revision: job.Revision, ResultCandidateRevision: job.ResultCandidateRevision, ErrorCode: job.ErrorCode, CanCancel: job.State == PlacementQueued || job.State == PlacementRunning}, nil
}

// FindExact returns a reusable job only when every caller-owned dependency
// still identifies the original request. Terminal failures and cancellations
// remain retryable as fresh work.
func (s *PlacementJobStore) FindExact(candidateID string, expectedCandidateRevision, expectedMapRevision int, generationID string) (PlacementJob, bool, error) {
	var result PlacementJob
	found := false
	err := s.read(func(file placementJobFile) error {
		for _, job := range file.Jobs {
			if job.CandidateID == candidateID && job.ExpectedCandidateRevision == expectedCandidateRevision && job.ExpectedProblemMapRevision == expectedMapRevision && job.ExpectedGenerationID == generationID && (job.State == PlacementQueued || job.State == PlacementRunning || job.State == PlacementCancelRequested || job.State == PlacementCompleted) {
				result, found = job, true
				return nil
			}
		}
		return nil
	})
	return result, found, err
}

func (s *PlacementJobStore) FindActiveCandidate(candidateID string) (PlacementJob, bool, error) {
	var result PlacementJob
	found := false
	err := s.read(func(file placementJobFile) error {
		for _, job := range file.Jobs {
			if job.CandidateID == candidateID && (job.State == PlacementQueued || job.State == PlacementRunning || job.State == PlacementCancelRequested) && (!found || job.CreatedAt > result.CreatedAt) {
				result, found = job, true
			}
		}
		return nil
	})
	return result, found, err
}

func (s *PlacementJobStore) Claim(jobID, launchToken string, now time.Time, lease time.Duration) (PlacementJob, error) {
	if len(launchToken) < 32 || lease <= 0 {
		return PlacementJob{}, errors.New("placement launch authority is invalid")
	}
	hash := sha256.Sum256([]byte(launchToken))
	return s.update(jobID, 0, now, func(job *PlacementJob) error {
		if job.State != PlacementQueued || job.LaunchTokenSHA256 != hex.EncodeToString(hash[:]) {
			return errors.New("placement launch authority is stale")
		}
		job.State, job.LaunchTokenSHA256, job.LeaseExpiresAt = PlacementRunning, "", now.UTC().Add(lease).Round(0).Format(time.RFC3339Nano)
		return nil
	})
}

func (s *PlacementJobStore) Cancel(jobID string, expectedRevision int, now time.Time) (PlacementJob, error) {
	return s.update(jobID, expectedRevision, now, func(job *PlacementJob) error {
		switch job.State {
		case PlacementQueued:
			job.State, job.LaunchTokenSHA256 = PlacementCancelled, ""
		case PlacementRunning:
			job.State = PlacementCancelRequested
		case PlacementCancelRequested:
			return nil
		default:
			return errors.New("placement job is already terminal")
		}
		return nil
	})
}

func (s *PlacementJobStore) FailQueued(jobID string, expectedRevision int, errorCode *string, now time.Time) (PlacementJob, error) {
	return s.update(jobID, expectedRevision, now, func(job *PlacementJob) error {
		if job.State != PlacementQueued || errorCode == nil || *errorCode == "" {
			return errors.New("queued placement failure is invalid")
		}
		job.State, job.ErrorCode, job.LaunchTokenSHA256 = PlacementFailed, errorCode, ""
		return nil
	})
}

func (s *PlacementJobStore) Finish(jobID string, expectedRevision int, state PlacementJobState, resultRevision int, errorCode *string, now time.Time) (PlacementJob, error) {
	return s.update(jobID, expectedRevision, now, func(job *PlacementJob) error {
		if job.State == PlacementCancelRequested {
			state, resultRevision = PlacementCancelled, 0
			code := "E_AGENT_CANCELLED"
			errorCode = &code
		}
		if job.State != PlacementRunning && job.State != PlacementCancelRequested || state != PlacementCompleted && state != PlacementFailed && state != PlacementCancelled {
			return errors.New("invalid placement terminal transition")
		}
		if state == PlacementCompleted && resultRevision <= job.ExpectedCandidateRevision || state != PlacementCompleted && resultRevision != 0 {
			return errors.New("invalid placement result revision")
		}
		job.State, job.ResultCandidateRevision, job.ErrorCode, job.LeaseExpiresAt = state, resultRevision, errorCode, ""
		return nil
	})
}

// CompleteCandidate serializes cancellation with the private candidate CAS.
// The candidate's exact Agent run ID is durable proof for retry recovery if
// the job-file replacement fails after the candidate replacement succeeds.
func (s *PlacementJobStore) CompleteCandidate(jobID string, expectedJobRevision int, candidates *Store, candidate Candidate, expectedCandidateRevision int, now time.Time) (PlacementJob, error) {
	if candidates == nil || candidate.AgentRunID == nil || *candidate.AgentRunID != jobID || candidate.Revision != expectedCandidateRevision+1 {
		return PlacementJob{}, errors.New("placement result identity is invalid")
	}
	var result PlacementJob
	err := s.mutate(func(file *placementJobFile) error {
		for index := range file.Jobs {
			job := &file.Jobs[index]
			if job.JobID != jobID {
				continue
			}
			if job.Revision != expectedJobRevision || job.State != PlacementRunning || job.CandidateID != candidate.CandidateID || job.ExpectedCandidateRevision != expectedCandidateRevision {
				return ErrCandidateRevisionConflict
			}
			if err := candidates.CompareAndSwap(candidate, expectedCandidateRevision); err != nil {
				return err
			}
			job.State, job.ResultCandidateRevision, job.LeaseExpiresAt = PlacementCompleted, candidate.Revision, ""
			job.Revision++
			job.UpdatedAt = now.UTC().Round(0).Format(time.RFC3339Nano)
			result = *job
			return nil
		}
		return os.ErrNotExist
	})
	return result, err
}

func (s *PlacementJobStore) RecoverCompleted(jobID string, candidates *Store, now time.Time) (PlacementJob, bool, error) {
	if candidates == nil {
		return PlacementJob{}, false, errors.New("candidate store is required")
	}
	var result PlacementJob
	recovered := false
	err := s.mutate(func(file *placementJobFile) error {
		for index := range file.Jobs {
			job := &file.Jobs[index]
			if job.JobID != jobID {
				continue
			}
			result = *job
			if job.State != PlacementRunning && job.State != PlacementCancelRequested {
				return nil
			}
			candidate, err := candidates.Get(job.CandidateID)
			if err != nil || candidate.AgentRunID == nil || *candidate.AgentRunID != job.JobID || candidate.Revision <= job.ExpectedCandidateRevision {
				return nil
			}
			job.State, job.ResultCandidateRevision, job.LeaseExpiresAt = PlacementCompleted, candidate.Revision, ""
			job.Revision++
			job.UpdatedAt = now.UTC().Round(0).Format(time.RFC3339Nano)
			result, recovered = *job, true
			return nil
		}
		return os.ErrNotExist
	})
	return result, recovered, err
}

func (s *PlacementJobStore) update(jobID string, expected int, now time.Time, change func(*PlacementJob) error) (PlacementJob, error) {
	var result PlacementJob
	err := s.mutate(func(file *placementJobFile) error {
		for index := range file.Jobs {
			job := &file.Jobs[index]
			if job.JobID != jobID {
				continue
			}
			if job.ProjectID != s.projectID || expected > 0 && job.Revision != expected {
				return ErrCandidateRevisionConflict
			}
			before := *job
			if err := change(job); err != nil {
				return err
			}
			if *job != before {
				job.Revision++
				job.UpdatedAt = now.UTC().Round(0).Format(time.RFC3339Nano)
			}
			result = *job
			return nil
		}
		return os.ErrNotExist
	})
	return result, err
}

func (s *PlacementJobStore) read(read func(placementJobFile) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, err := publicationlock.Acquire(s.dataRoot, s.projectID, 10*time.Second)
	if err != nil {
		return err
	}
	defer owner.Release()
	file, err := s.load()
	if err != nil {
		return err
	}
	return read(file)
}

func (s *PlacementJobStore) mutate(change func(*placementJobFile) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, err := publicationlock.Acquire(s.dataRoot, s.projectID, 10*time.Second)
	if err != nil {
		return err
	}
	defer owner.Release()
	file, err := s.load()
	if err != nil {
		return err
	}
	if err := change(&file); err != nil {
		return err
	}
	return s.save(file)
}

func (s *PlacementJobStore) load() (placementJobFile, error) {
	body, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return placementJobFile{SchemaVersion: 1, ProjectID: s.projectID, Jobs: []PlacementJob{}}, nil
	}
	if err != nil {
		return placementJobFile{}, err
	}
	var file placementJobFile
	if err := strictjson.Decode(body, &file); err != nil {
		return placementJobFile{}, err
	}
	if err := validatePlacementJobs(file, s.projectID); err != nil {
		return placementJobFile{}, err
	}
	return file, nil
}

func (s *PlacementJobStore) save(file placementJobFile) error {
	sort.Slice(file.Jobs, func(i, j int) bool { return file.Jobs[i].JobID < file.Jobs[j].JobID })
	if err := validatePlacementJobs(file, s.projectID); err != nil {
		return err
	}
	body, err := strictjson.Encode(file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(s.path, body, 0o600)
}

func validatePlacementJobs(file placementJobFile, projectID string) error {
	if file.SchemaVersion != 1 || file.ProjectID != projectID || len(file.Jobs) > 65536 {
		return errors.New("invalid placement job file")
	}
	seen := map[string]bool{}
	for _, job := range file.Jobs {
		if job.SchemaVersion != 1 || job.ProjectID != projectID || !validID(job.JobID) || !validID(job.CandidateID) || !validID(job.AnalysisIdentity) || !validID(job.ExpectedGenerationID) || job.ExpectedCandidateRevision < 1 || job.ExpectedProblemMapRevision < 0 || job.ResultCandidateRevision < 0 || job.Revision < 1 || job.CreatedAt == "" || job.UpdatedAt == "" || seen[job.JobID] {
			return errors.New("invalid placement job")
		}
		seen[job.JobID] = true
		switch job.State {
		case PlacementQueued, PlacementRunning, PlacementCancelRequested, PlacementCompleted, PlacementFailed, PlacementCancelled:
		default:
			return errors.New("invalid placement job state")
		}
		if job.State == PlacementQueued && len(job.LaunchTokenSHA256) != 64 || job.State != PlacementQueued && job.LaunchTokenSHA256 != "" {
			return errors.New("invalid placement launch authority")
		}
		if job.State == PlacementRunning || job.State == PlacementCancelRequested {
			if job.LeaseExpiresAt == "" {
				return errors.New("active placement job lacks lease")
			}
		} else if job.LeaseExpiresAt != "" {
			return errors.New("inactive placement job has lease")
		}
		if (job.State == PlacementCompleted) != (job.ResultCandidateRevision > job.ExpectedCandidateRevision) {
			return errors.New("placement result binding is invalid")
		}
	}
	return nil
}
