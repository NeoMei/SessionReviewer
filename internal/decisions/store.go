package decisions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"time"

	"github.com/neomei/SessionReviewer/internal/annotation"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/project"
)

var (
	ErrCandidateRevisionConflict = errors.New("candidate revision conflict")
	ErrCandidateTerminal         = errors.New("candidate revision is terminal")
	storeIDPattern               = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
)

type Store struct {
	dataRoot  string
	projectID string
}

// CandidatePublicationDigest binds an intent to the immutable candidate
// identity and evidence while excluding lifecycle fields changed by a
// successful confirmation.
func CandidatePublicationDigest(candidate annotation.Annotation) (string, error) {
	identity := struct {
		ID, ProjectID, AnnotationKind, Text, GenerationID string
		EntityID, Field                                   *string
		SchemaVersion                                     int
		AnalysisProfile, AgentRunID                       string
		Dependencies                                      []annotation.Dependency
		CreatedAt                                         string
		TargetMilestoneID, PromptSchemaVersion            *string
	}{
		ID: candidate.ID, ProjectID: candidate.ProjectID, AnnotationKind: candidate.AnnotationKind,
		Text: candidate.Text, GenerationID: candidate.GenerationID, EntityID: candidate.EntityID, Field: candidate.Field,
		SchemaVersion: candidate.SchemaVersion, AnalysisProfile: candidate.AnalysisProfile, AgentRunID: candidate.AgentRunID,
		Dependencies: candidate.Dependencies, CreatedAt: candidate.CreatedAt,
		TargetMilestoneID: candidate.TargetMilestoneID, PromptSchemaVersion: candidate.PromptSchemaVersion,
	}
	return memory.Digest(identity)
}

func OpenStore(dataRoot, projectID string) (*Store, error) {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot || !storeIDPattern.MatchString(projectID) {
		return nil, errors.New("decision store requires an absolute data root and valid project ID")
	}
	return &Store{dataRoot: dataRoot, projectID: projectID}, nil
}

func (s *Store) Load() (annotation.StoreRecord, error) {
	store, err := annotation.OpenStoreReadOnly(s.dataRoot, s.projectID)
	if err != nil {
		return annotation.StoreRecord{}, err
	}
	defer store.Close()
	state, err := store.Load(context.Background())
	if errors.Is(err, annotation.ErrStoreNotFound) {
		return emptyStoreRecord(s.projectID), nil
	}
	if err != nil {
		return annotation.StoreRecord{}, err
	}
	return state.Record, nil
}

func (s *Store) ReplaceAbsent(record annotation.StoreRecord) error {
	return s.mutate(func(current *annotation.StoreRecord, existed bool) error {
		if existed {
			return ErrCandidateRevisionConflict
		}
		*current = cloneStoreRecord(record)
		return validateStoredSemantics(*current, s.projectID)
	})
}

func (s *Store) List(status annotation.CandidateStatus) ([]annotation.Annotation, error) {
	record, err := s.Load()
	if err != nil {
		return nil, err
	}
	values := []annotation.Annotation{}
	for _, candidate := range record.Annotations {
		if candidate.AnnotationKind != "decision_candidate" && candidate.AnnotationKind != "agreement_candidate" {
			continue
		}
		if status == "" || candidate.Status == status {
			values = append(values, cloneAnnotation(candidate))
		}
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].CreatedAt < values[j].CreatedAt || values[i].CreatedAt == values[j].CreatedAt && values[i].ID < values[j].ID
	})
	return values, nil
}

func (s *Store) Get(id string) (annotation.Annotation, error) {
	values, err := s.List("")
	if err != nil {
		return annotation.Annotation{}, err
	}
	for _, value := range values {
		if value.ID == id {
			return value, nil
		}
	}
	return annotation.Annotation{}, os.ErrNotExist
}

func (s *Store) CommitExtraction(run annotation.Run, candidates []annotation.Annotation) error {
	return s.mutate(func(record *annotation.StoreRecord, _ bool) error {
		return applyExtraction(record, s.projectID, run, candidates)
	})
}

func (s *Store) completedExtraction(job ExtractionJob) (int, bool, error) {
	record, err := s.Load()
	if err != nil {
		return 0, false, err
	}
	for _, run := range record.ExtractionRuns {
		if run.RunID != job.JobID {
			continue
		}
		if run.ProjectID != job.ProjectID || run.Status != "completed" || run.ExtractorVersion != ExtractorVersion || run.PromptSchemaVersion != PromptSchemaVersion || !reflect.DeepEqual(run.DependencyDigests, job.DependencyDigests) {
			return 0, false, ErrCandidateRevisionConflict
		}
		count := 0
		for _, candidate := range record.Annotations {
			if candidate.AgentRunID == run.RunID {
				count++
			}
		}
		return count, true, nil
	}
	return 0, false, nil
}

func applyExtraction(record *annotation.StoreRecord, projectID string, run annotation.Run, candidates []annotation.Annotation) error {
	for _, existing := range record.ExtractionRuns {
		if existing.RunID != run.RunID {
			continue
		}
		if !reflect.DeepEqual(existing, run) {
			return ErrCandidateRevisionConflict
		}
		byID := map[string]annotation.Annotation{}
		for _, value := range record.Annotations {
			byID[value.ID] = value
		}
		for _, candidate := range candidates {
			if !reflect.DeepEqual(byID[candidate.ID], candidate) {
				return ErrCandidateRevisionConflict
			}
		}
		return nil
	}
	if run.Status != "completed" || run.ProjectID != projectID {
		return errors.New("only a completed extraction run can advance candidates")
	}
	existingIDs := map[string]bool{}
	for _, candidate := range record.Annotations {
		existingIDs[candidate.ID] = true
	}
	for _, candidate := range candidates {
		if candidate.AgentRunID != run.RunID || existingIDs[candidate.ID] {
			return ErrCandidateRevisionConflict
		}
		existingIDs[candidate.ID] = true
	}
	record.ExtractionRuns = append(record.ExtractionRuns, run)
	record.Annotations = append(record.Annotations, candidates...)
	return nil
}

func SuccessfulExtractionDependencies(record annotation.StoreRecord) map[string]bool {
	result := map[string]bool{}
	for _, run := range record.ExtractionRuns {
		if run.Status != "completed" || run.ExtractorVersion != ExtractorVersion || run.PromptSchemaVersion != PromptSchemaVersion {
			continue
		}
		for _, digest := range run.DependencyDigests {
			result[digest] = true
		}
	}
	return result
}

func (s *Store) Transition(id string, expectedRevision int, action, confirmedEntityID string, at time.Time) (annotation.Annotation, error) {
	var result annotation.Annotation
	err := s.mutate(func(record *annotation.StoreRecord, _ bool) error {
		position := -1
		for index := range record.Annotations {
			if record.Annotations[index].ID == id && (record.Annotations[index].AnnotationKind == "decision_candidate" || record.Annotations[index].AnnotationKind == "agreement_candidate") {
				position = index
				break
			}
		}
		if position < 0 {
			return os.ErrNotExist
		}
		candidate := cloneAnnotation(record.Annotations[position])
		if candidate.Revision != expectedRevision {
			return ErrCandidateRevisionConflict
		}
		if candidate.Status == annotation.CandidateConfirmed || candidate.Status == annotation.CandidateNotDecision || candidate.Status == annotation.CandidateStale {
			return ErrCandidateTerminal
		}
		var next annotation.CandidateStatus
		switch candidate.Status {
		case annotation.CandidatePending:
			switch action {
			case "confirm":
				next = annotation.CandidateConfirmed
			case "ignore":
				next = annotation.CandidateIgnored
			case "not_decision":
				next = annotation.CandidateNotDecision
			case "stale":
				next = annotation.CandidateStale
			}
		case annotation.CandidateIgnored:
			switch action {
			case "restore":
				next = annotation.CandidatePending
			case "stale":
				next = annotation.CandidateStale
			}
		}
		if next == "" {
			return errors.New("candidate transition is not allowed")
		}
		if next == annotation.CandidateConfirmed {
			if !storeIDPattern.MatchString(confirmedEntityID) {
				return errors.New("confirmed candidate requires a valid entity ID")
			}
			candidate.ConfirmedEntityID = &confirmedEntityID
		} else {
			if confirmedEntityID != "" {
				return errors.New("non-confirmation transition cannot name an entity")
			}
			candidate.ConfirmedEntityID = nil
		}
		candidate.Status = next
		candidate.Revision++
		if candidate.Revision > 1<<53-1 || at.IsZero() {
			return errors.New("candidate revision or transition time is invalid")
		}
		record.Annotations[position] = candidate
		result = cloneAnnotation(candidate)
		return nil
	})
	return result, err
}

func (s *Store) mutate(change func(*annotation.StoreRecord, bool) error) (retErr error) {
	return s.mutateUnlocked(change)
}

func (s *Store) mutateUnlocked(change func(*annotation.StoreRecord, bool) error) error {
	store, err := annotation.OpenStore(s.dataRoot, s.projectID)
	if err != nil {
		return err
	}
	defer store.Close()
	state, loadErr := store.Load(context.Background())
	existed := loadErr == nil
	if errors.Is(loadErr, annotation.ErrStoreNotFound) {
		state = annotation.StoredState{Record: emptyStoreRecord(s.projectID)}
	} else if loadErr != nil {
		return loadErr
	}
	record := cloneStoreRecord(state.Record)
	if err := change(&record, existed); err != nil {
		return err
	}
	if err := validateStoredSemantics(record, s.projectID); err != nil {
		return err
	}
	_, err = store.CompareAndSwap(context.Background(), state.Revision, state.Digest, record)
	if errors.Is(err, annotation.ErrCandidateRevisionConflict) {
		return ErrCandidateRevisionConflict
	}
	return err
}

func withDecisionControlLock(dataRoot string, operation func() error) (retErr error) {
	lockDir := filepath.Join(dataRoot, "decision-extraction-jobs")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dataRoot)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, root.Close()) }()
	lock, err := project.AcquireProjectLock(root, "decision-extraction-jobs/control.lock", 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	return operation()
}

func validateStoredSemantics(record annotation.StoreRecord, projectID string) error {
	if record.ProjectID != projectID {
		return errors.New("candidate store belongs to another project")
	}
	if err := annotation.Validate(record); err != nil {
		return err
	}
	runStatus := map[string]string{}
	for _, run := range record.ExtractionRuns {
		runStatus[run.RunID] = run.Status
	}
	for _, candidate := range record.Annotations {
		if runStatus[candidate.AgentRunID] != "completed" {
			return errors.New("persisted candidate does not reference a completed extraction run")
		}
	}
	return nil
}

func emptyStoreRecord(projectID string) annotation.StoreRecord {
	return annotation.StoreRecord{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: projectID, Annotations: []annotation.Annotation{}, ExtractionRuns: []annotation.Run{}}
}

func cloneStoreRecord(record annotation.StoreRecord) annotation.StoreRecord {
	next := record
	next.Annotations = make([]annotation.Annotation, len(record.Annotations))
	for index, candidate := range record.Annotations {
		next.Annotations[index] = cloneAnnotation(candidate)
	}
	next.ExtractionRuns = make([]annotation.Run, len(record.ExtractionRuns))
	for index, run := range record.ExtractionRuns {
		next.ExtractionRuns[index] = run
		next.ExtractionRuns[index].DependencyDigests = cloneSlice(run.DependencyDigests)
	}
	return next
}

func cloneAnnotation(candidate annotation.Annotation) annotation.Annotation {
	next := candidate
	next.Dependencies = cloneSlice(candidate.Dependencies)
	clone := func(value *string) *string {
		if value == nil {
			return nil
		}
		copy := *value
		return &copy
	}
	next.EntityID, next.Field, next.ConfirmedEntityID = clone(candidate.EntityID), clone(candidate.Field), clone(candidate.ConfirmedEntityID)
	next.TargetMilestoneID, next.PromptSchemaVersion = clone(candidate.TargetMilestoneID), clone(candidate.PromptSchemaVersion)
	return next
}
