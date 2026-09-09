package problemmap

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

var ErrCandidateRevisionConflict = errors.New("candidate revision conflict")

type Store struct {
	dataRoot, projectID, path string
	mu                        *sync.Mutex
}

var storeLocks sync.Map

func OpenStore(dataRoot, projectID string) (*Store, error) {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot || !validID(projectID) {
		return nil, errors.New("problem store requires an absolute data root and valid project ID")
	}
	path := filepath.Join(dataRoot, "projects", projectID, "problem-map-candidates.json")
	lock, _ := storeLocks.LoadOrStore(path, &sync.Mutex{})
	return &Store{dataRoot: dataRoot, projectID: projectID, path: path, mu: lock.(*sync.Mutex)}, nil
}

func (s *Store) ProjectID() string { return s.projectID }

func (s *Store) load() (CandidateStore, error) {
	body, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return CandidateStore{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", Digest: zeroDigest(), ProjectID: s.projectID, Candidates: []Candidate{}}, nil
	}
	if err != nil {
		return CandidateStore{}, err
	}
	return ParseCandidates(body)
}

func (s *Store) List(status CandidateStatus) ([]Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.load()
	if err != nil {
		return nil, err
	}
	result := []Candidate{}
	for _, candidate := range store.Candidates {
		if status == "" || candidate.Status == status {
			result = append(result, candidate)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt || (result[i].CreatedAt == result[j].CreatedAt && result[i].CandidateID < result[j].CandidateID)
	})
	return result, nil
}

func (s *Store) Get(id string) (Candidate, error) {
	values, err := s.List("")
	if err != nil {
		return Candidate{}, err
	}
	for _, value := range values {
		if value.CandidateID == id {
			return value, nil
		}
	}
	return Candidate{}, os.ErrNotExist
}

func (s *Store) CompareAndSwap(candidate Candidate, expectedRevision int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if candidate.ProjectID != s.projectID {
		return errors.New("candidate belongs to another project")
	}
	store, err := s.load()
	if err != nil {
		return err
	}
	position := -1
	for i := range store.Candidates {
		if store.Candidates[i].CandidateID == candidate.CandidateID {
			position = i
			break
		}
	}
	if position < 0 {
		if expectedRevision != 0 || candidate.Revision != 1 {
			return ErrCandidateRevisionConflict
		}
		store.Candidates = append(store.Candidates, candidate)
	} else {
		if store.Candidates[position].Revision != expectedRevision || candidate.Revision != expectedRevision+1 {
			return ErrCandidateRevisionConflict
		}
		store.Candidates[position] = candidate
	}
	capability := "0.4.0"
	for _, value := range store.Candidates {
		for _, ref := range value.SourceTurnRefs {
			if ref.SessionViewDigest != "" {
				capability = "0.4.3"
			}
		}
	}
	store.MinimumReaderVersion = capability
	body, err := RenderCandidates(store)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(s.path, body, 0o600)
}

func AnalysisIdentity(projectID, question, ruleVersion string, dependencies []string) string {
	values := append([]string(nil), dependencies...)
	sort.Strings(values)
	normalized := strings.ToLower(strings.Join(strings.Fields(question), " "))
	sum := sha256.Sum256([]byte(projectID + "\x00" + normalized + "\x00" + ruleVersion + "\x00" + strings.Join(values, "\x00")))
	return "placement-" + hex.EncodeToString(sum[:16])
}

func NewHumanCandidate(projectID, question string, now time.Time) Candidate {
	stamp := now.UTC().Round(0).Format(time.RFC3339Nano)
	return Candidate{
		CandidateID: AnalysisIdentity(projectID, question, "human-created-v1", nil), ProjectID: projectID, Question: question,
		SourceTurnRefs: []reviewv4.SourceTurnRef{}, RecommendedRelation: RelationKeepPending, RecommendedTargetID: nil,
		AlternateTargetIDs: []string{}, RelatedNodeIDs: []string{}, Grounds: []Ground{{RuleID: "human-created", RuleVersion: "v1", MatchedFactRefs: []string{}, Explanation: "由用户明确创建，等待确认正式位置。"}},
		Confidence: ConfidenceLow, Status: CandidatePending, DependencyDigests: []string{}, AnalysisMode: AnalysisDeterministic, AgentRunID: nil,
		Revision: 1, CreatedAt: stamp, UpdatedAt: stamp,
	}
}
