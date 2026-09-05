// Package publicationlock owns the one OS advisory lock shared by every
// public-projection publisher for a project.
package publicationlock

import (
	"errors"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/project"
)

const relativePath = "publication.lock"

var projectIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// Owner is an unforgeable in-process capability for one held publication
// lock. Its fields are private so callers can only obtain it through Acquire.
type Owner struct {
	mu        sync.Mutex
	lock      *project.ProjectLock
	directory *pathguard.Directory
	dataRoot  string
	projectID string
}

func Acquire(dataRoot, projectID string, timeout time.Duration) (*Owner, error) {
	if dataRoot == "" || !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot || !projectIDPattern.MatchString(projectID) {
		return nil, errors.New("valid publication lock identity is required")
	}
	data, err := pathguard.Open(dataRoot)
	if err != nil {
		return nil, err
	}
	closeData := true
	defer func() {
		if closeData {
			_ = data.Close()
		}
	}()
	for _, relative := range []string{"publication-locks", "publication-locks/" + projectID} {
		if err := data.EnsureDirectory(relative, 0o700); err != nil {
			return nil, err
		}
	}
	directory, err := pathguard.Open(filepath.Join(dataRoot, "publication-locks", projectID))
	if err != nil {
		return nil, err
	}
	lock, err := project.AcquireProjectLock(directory.Root, relativePath, timeout)
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	closeData = false
	if err := data.Close(); err != nil {
		_ = lock.Release()
		_ = directory.Close()
		return nil, err
	}
	return &Owner{lock: lock, directory: directory, dataRoot: dataRoot, projectID: projectID}, nil
}

// Use validates the ownership identity and prevents Release from racing the
// operation performed under the held OS lock.
func (owner *Owner) Use(dataRoot, projectID string, operation func() error) error {
	if owner == nil || operation == nil {
		return errors.New("publication lock ownership token is required")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.lock == nil || owner.dataRoot != dataRoot || owner.projectID != projectID {
		return errors.New("publication lock ownership token does not match project")
	}
	return operation()
}

func (owner *Owner) Release() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.lock == nil {
		return nil
	}
	lock := owner.lock
	directory := owner.directory
	owner.lock = nil
	owner.directory = nil
	return errors.Join(lock.Release(), directory.Close())
}
