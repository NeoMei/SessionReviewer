package decisions

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"time"

	"github.com/neomei/SessionReviewer/internal/annotation"
	"github.com/neomei/SessionReviewer/internal/atomicfile"
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
	path      string
}

func OpenStore(dataRoot, projectID string) (*Store, error) {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot || !storeIDPattern.MatchString(projectID) {
		return nil, errors.New("decision store requires an absolute data root and valid project ID")
	}
	return &Store{dataRoot: dataRoot, projectID: projectID, path: filepath.Join(dataRoot, "projects", projectID, "agent-annotations.json")}, nil
}

func (s *Store) Load() (annotation.StoreRecord, error) {
	body, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyStoreRecord(s.projectID), nil
	}
	if err != nil {
		return annotation.StoreRecord{}, err
	}
	info, err := os.Lstat(s.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return annotation.StoreRecord{}, errors.New("decision candidate store is not a private regular file")
	}
	record, err := annotation.Parse(body)
	if err != nil || record.ProjectID != s.projectID {
		return annotation.StoreRecord{}, errors.Join(errors.New("decision candidate store is invalid"), err)
	}
	return record, nil
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
	projectDir := filepath.Dir(s.path)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(projectDir, 0o700); err != nil {
			return err
		}
	}
	root, err := os.OpenRoot(s.dataRoot)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, root.Close()) }()
	lock, err := project.AcquireProjectLock(root, filepath.ToSlash(filepath.Join("projects", s.projectID, "agent-annotations.lock")), 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	_, statErr := os.Lstat(s.path)
	existed := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	record, err := s.Load()
	if err != nil {
		return err
	}
	if err := change(&record, existed); err != nil {
		return err
	}
	if err := validateStoredSemantics(record, s.projectID); err != nil {
		return err
	}
	body, err := annotation.Render(record)
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path, body, 0o600)
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
