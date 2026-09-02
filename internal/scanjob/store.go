package scanjob

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

const (
	jobDirectoryMode fs.FileMode = 0o700
	jobFileMode      fs.FileMode = 0o600
	maxJobBytes                  = 4 << 20
	activeJobLeaf                = "active.json"
)

var (
	jobIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

type Store struct {
	mu        sync.Mutex
	data      *pathguard.Directory
	jobs      *pathguard.Directory
	projectID string
}

func OpenStore(dataRoot, projectID string) (*Store, error) {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return nil, errors.New("data root must be absolute and clean")
	}
	if !jobIDPattern.MatchString(projectID) {
		return nil, errors.New("invalid project ID")
	}
	data, err := pathguard.Open(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("open data root: %w", err)
	}
	closeData := true
	defer func() {
		if closeData {
			_ = data.Close()
		}
	}()

	rel := filepath.ToSlash(filepath.Join("projects", projectID, "jobs"))
	for _, d := range []string{"projects", filepath.ToSlash(filepath.Join("projects", projectID)), rel} {
		if err := data.EnsureDirectory(d, jobDirectoryMode); err != nil {
			return nil, fmt.Errorf("ensure jobs directory %q: %w", d, err)
		}
	}
	jobsPath := filepath.Join(data.Path, "projects", projectID, "jobs")
	jobsDir, err := pathguard.Open(jobsPath)
	if err != nil {
		return nil, fmt.Errorf("open jobs directory: %w", err)
	}
	closeData = false
	return &Store{data: data, jobs: jobsDir, projectID: projectID}, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	if s.jobs != nil {
		errs = append(errs, s.jobs.Close())
	}
	if s.data != nil {
		errs = append(errs, s.data.Close())
	}
	return errors.Join(errs...)
}

func (s *Store) SaveJob(job JobRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.SchemaVersion == 0 {
		job.SchemaVersion = 1
	}
	if !jobIDPattern.MatchString(job.JobID) {
		return errors.New("invalid job ID")
	}
	body, err := encodeCanonicalJSON(job)
	if err != nil {
		return err
	}
	leaf := job.JobID + ".json"
	if err := atomicfile.WriteRootFileChecked(s.jobs.Root, leaf, body, jobFileMode, nil); err != nil {
		return err
	}
	return atomicfile.WriteRootFileChecked(s.jobs.Root, activeJobLeaf, body, jobFileMode, nil)
}

func (s *Store) LoadJob(jobID string) (JobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !jobIDPattern.MatchString(jobID) {
		return JobRecord{}, errors.New("invalid job ID")
	}
	body, found, err := s.jobs.ReadRegular(jobID+".json", maxJobBytes)
	if err != nil {
		return JobRecord{}, err
	}
	if !found {
		return JobRecord{}, os.ErrNotExist
	}
	var job JobRecord
	if err := decodeStrictJSON(body, &job); err != nil {
		return JobRecord{}, err
	}
	return job, nil
}

func (s *Store) LoadActiveJob() (JobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, found, err := s.jobs.ReadRegular(activeJobLeaf, maxJobBytes)
	if err != nil {
		return JobRecord{}, err
	}
	if !found {
		return JobRecord{}, os.ErrNotExist
	}
	var job JobRecord
	if err := decodeStrictJSON(body, &job); err != nil {
		return JobRecord{}, err
	}
	return job, nil
}

func encodeCanonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}
	return buf.Bytes(), nil
}

func decodeStrictJSON(data []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing bytes after JSON object")
	}
	return nil
}
