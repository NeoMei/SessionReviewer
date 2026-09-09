// Package candidatepublication persists the private half of a
// candidate-to-Markdown publication until an immutable accepted-result receipt
// proves whether the public half committed.
package candidatepublication

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

const maxStoreBytes int64 = 4 << 20

var (
	ErrIntentConflict   = errors.New("candidate publication intent conflict")
	ErrRevisionConflict = errors.New("candidate publication intent revision conflict")
	ErrTerminalIntent   = errors.New("candidate publication intent is terminal")
	idPattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	digestPattern       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	storeLocks          sync.Map
)

type State string

const (
	StatePrepared  State = "prepared"
	StateCompleted State = "completed"
	StateAborted   State = "aborted"
)

type IntentInput struct {
	ProjectID                 string
	Namespace                 string
	CandidateID               string
	ExpectedCandidateRevision int
	CandidateDigest           string
	Action                    string
	EntityID                  string
	ResultFingerprint         string
	TerminalStatus            string
	PreparedAt                time.Time
}

type Intent struct {
	SchemaVersion             int    `json:"schema_version" required:"true"`
	OperationID               string `json:"operation_id" required:"true"`
	ProjectID                 string `json:"project_id" required:"true"`
	Namespace                 string `json:"namespace" required:"true"`
	CandidateID               string `json:"candidate_id" required:"true"`
	ExpectedCandidateRevision int    `json:"expected_candidate_revision" required:"true"`
	CandidateDigest           string `json:"candidate_digest" required:"true"`
	Action                    string `json:"action" required:"true"`
	EntityID                  string `json:"entity_id" required:"true"`
	ResultFingerprint         string `json:"result_fingerprint" required:"true"`
	TerminalStatus            string `json:"terminal_status" required:"true"`
	State                     State  `json:"state" required:"true"`
	Revision                  int    `json:"revision" required:"true"`
	PreparedAt                string `json:"prepared_at" required:"true"`
	UpdatedAt                 string `json:"updated_at" required:"true"`
}

type document struct {
	SchemaVersion int      `json:"schema_version" required:"true"`
	ProjectID     string   `json:"project_id" required:"true"`
	Namespace     string   `json:"namespace" required:"true"`
	Intents       []Intent `json:"intents" required:"true"`
}

type Store struct {
	dataRoot, projectID, namespace, relative, leaf string
	mu                                             *sync.Mutex
}

func NewIntent(input IntentInput) (Intent, error) {
	stamp := input.PreparedAt.UTC().Round(0)
	intent := Intent{
		SchemaVersion: 1, ProjectID: input.ProjectID, Namespace: input.Namespace,
		CandidateID: input.CandidateID, ExpectedCandidateRevision: input.ExpectedCandidateRevision,
		CandidateDigest: input.CandidateDigest, Action: input.Action, EntityID: input.EntityID,
		ResultFingerprint: input.ResultFingerprint, TerminalStatus: input.TerminalStatus,
		State: StatePrepared, Revision: 1, PreparedAt: stamp.Format(time.RFC3339Nano), UpdatedAt: stamp.Format(time.RFC3339Nano),
	}
	intent.OperationID = operationID(intent)
	if err := validateIntent(intent); err != nil {
		return Intent{}, err
	}
	return intent, nil
}

func OpenStore(dataRoot, projectID, namespace string) (*Store, error) {
	if !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot || !idPattern.MatchString(projectID) || !idPattern.MatchString(namespace) {
		return nil, errors.New("candidate publication store requires a clean absolute data root and valid identity")
	}
	relative := filepath.ToSlash(filepath.Join("projects", projectID))
	leaf := "candidate-publication-" + namespace + ".json"
	path := filepath.Join(dataRoot, filepath.FromSlash(relative), leaf)
	lock, _ := storeLocks.LoadOrStore(path, &sync.Mutex{})
	return &Store{dataRoot: dataRoot, projectID: projectID, namespace: namespace, relative: relative, leaf: leaf, mu: lock.(*sync.Mutex)}, nil
}

func (store *Store) Prepare(intent Intent) (Intent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := validateIntent(intent); err != nil || intent.ProjectID != store.projectID || intent.Namespace != store.namespace || intent.State != StatePrepared || intent.Revision != 1 {
		return Intent{}, errors.Join(errors.New("candidate publication intent is invalid"), err)
	}
	document, err := store.load()
	if err != nil {
		return Intent{}, err
	}
	for _, current := range document.Intents {
		if current.OperationID != intent.OperationID {
			continue
		}
		if !reflect.DeepEqual(current, intent) {
			return Intent{}, ErrIntentConflict
		}
		return current, nil
	}
	if intent.OperationID != operationID(intent) {
		return Intent{}, errors.New("candidate publication intent operation ID is invalid")
	}
	document.Intents = append(document.Intents, intent)
	if err := store.save(document); err != nil {
		return Intent{}, err
	}
	return intent, nil
}

func (store *Store) Get(operationID string) (Intent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	document, err := store.load()
	if err != nil {
		return Intent{}, err
	}
	for _, intent := range document.Intents {
		if intent.OperationID == operationID {
			return intent, nil
		}
	}
	return Intent{}, os.ErrNotExist
}

func (store *Store) Prepared() ([]Intent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	document, err := store.load()
	if err != nil {
		return nil, err
	}
	result := []Intent{}
	for _, intent := range document.Intents {
		if intent.State == StatePrepared {
			result = append(result, intent)
		}
	}
	return result, nil
}

func (store *Store) Transition(operationID string, expectedRevision int, next State, at time.Time) (Intent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !idPattern.MatchString(operationID) || expectedRevision < 1 || (next != StateCompleted && next != StateAborted) || at.IsZero() {
		return Intent{}, errors.New("candidate publication transition is invalid")
	}
	document, err := store.load()
	if err != nil {
		return Intent{}, err
	}
	for index := range document.Intents {
		intent := document.Intents[index]
		if intent.OperationID != operationID {
			continue
		}
		if intent.Revision != expectedRevision {
			return Intent{}, ErrRevisionConflict
		}
		if intent.State != StatePrepared {
			return Intent{}, ErrTerminalIntent
		}
		intent.State, intent.Revision = next, intent.Revision+1
		intent.UpdatedAt = at.UTC().Round(0).Format(time.RFC3339Nano)
		if err := validateIntent(intent); err != nil {
			return Intent{}, err
		}
		document.Intents[index] = intent
		if err := store.save(document); err != nil {
			return Intent{}, err
		}
		return intent, nil
	}
	return Intent{}, os.ErrNotExist
}

func (store *Store) load() (document, error) {
	empty := document{SchemaVersion: 1, ProjectID: store.projectID, Namespace: store.namespace, Intents: []Intent{}}
	data, err := pathguard.Open(store.dataRoot)
	if err != nil {
		return document{}, err
	}
	defer data.Close()
	body, found, err := data.ReadRegularOptional(filepath.ToSlash(filepath.Join(store.relative, store.leaf)), maxStoreBytes)
	if err != nil {
		return document{}, err
	}
	if !found {
		return empty, nil
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Lstat(filepath.Join(store.dataRoot, filepath.FromSlash(store.relative), store.leaf))
		if statErr != nil || info.Mode().Perm() != 0o600 {
			return document{}, errors.New("candidate publication store is not private")
		}
	}
	var value document
	if err := strictjson.Decode(body, &value); err != nil {
		return document{}, err
	}
	if err := validateDocument(value, store.projectID, store.namespace); err != nil {
		return document{}, err
	}
	return value, nil
}

func (store *Store) save(value document) error {
	sort.Slice(value.Intents, func(i, j int) bool { return value.Intents[i].OperationID < value.Intents[j].OperationID })
	if err := validateDocument(value, store.projectID, store.namespace); err != nil {
		return err
	}
	body, err := strictjson.Encode(value)
	if err != nil || int64(len(body)) > maxStoreBytes {
		return errors.Join(errors.New("candidate publication store exceeds limit"), err)
	}
	data, err := pathguard.Open(store.dataRoot)
	if err != nil {
		return err
	}
	defer data.Close()
	if err := data.EnsureDirectory(store.relative, 0o700); err != nil {
		return err
	}
	parent, _, err := data.OpenDirectory(store.relative)
	if err != nil {
		return err
	}
	defer parent.Close()
	return atomicfile.WriteRootFile(parent, store.leaf, body, 0o600)
}

func validateDocument(value document, projectID, namespace string) error {
	if value.SchemaVersion != 1 || value.ProjectID != projectID || value.Namespace != namespace || value.Intents == nil || len(value.Intents) > 65536 {
		return errors.New("candidate publication store is invalid")
	}
	seen := map[string]bool{}
	for _, intent := range value.Intents {
		if err := validateIntent(intent); err != nil || intent.ProjectID != projectID || intent.Namespace != namespace || seen[intent.OperationID] || intent.OperationID != operationID(intent) {
			return errors.Join(errors.New("candidate publication store contains an invalid intent"), err)
		}
		seen[intent.OperationID] = true
	}
	return nil
}

func validateIntent(intent Intent) error {
	if intent.SchemaVersion != 1 || !idPattern.MatchString(intent.OperationID) || !idPattern.MatchString(intent.ProjectID) || !idPattern.MatchString(intent.Namespace) || !idPattern.MatchString(intent.CandidateID) || intent.ExpectedCandidateRevision < 1 || !digestPattern.MatchString(intent.CandidateDigest) || !idPattern.MatchString(intent.Action) || !idPattern.MatchString(intent.EntityID) || !digestPattern.MatchString(intent.ResultFingerprint) || !idPattern.MatchString(intent.TerminalStatus) || intent.Revision < 1 || intent.Revision > 1<<53-1 {
		return errors.New("candidate publication intent identity is invalid")
	}
	if intent.State != StatePrepared && intent.State != StateCompleted && intent.State != StateAborted {
		return errors.New("candidate publication intent state is invalid")
	}
	prepared, err := time.Parse(time.RFC3339Nano, intent.PreparedAt)
	if err != nil {
		return errors.New("candidate publication prepared time is invalid")
	}
	updated, err := time.Parse(time.RFC3339Nano, intent.UpdatedAt)
	if err != nil || updated.Before(prepared) {
		return errors.New("candidate publication update time is invalid")
	}
	if intent.State == StatePrepared && intent.Revision != 1 || intent.State != StatePrepared && intent.Revision != 2 {
		return errors.New("candidate publication lifecycle is invalid")
	}
	return nil
}

func operationID(intent Intent) string {
	identity := struct {
		ProjectID                 string `json:"project_id"`
		Namespace                 string `json:"namespace"`
		CandidateID               string `json:"candidate_id"`
		ExpectedCandidateRevision int    `json:"expected_candidate_revision"`
		CandidateDigest           string `json:"candidate_digest"`
		Action                    string `json:"action"`
		EntityID                  string `json:"entity_id"`
		ResultFingerprint         string `json:"result_fingerprint"`
		TerminalStatus            string `json:"terminal_status"`
	}{intent.ProjectID, intent.Namespace, intent.CandidateID, intent.ExpectedCandidateRevision, intent.CandidateDigest, intent.Action, intent.EntityID, intent.ResultFingerprint, intent.TerminalStatus}
	body, err := strictjson.Encode(identity)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return "candidate-op-" + hex.EncodeToString(sum[:16])
}
