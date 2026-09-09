package modelpricewatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const refreshInterval = 24 * time.Hour
const maximumStaleAge = 7 * 24 * time.Hour

type Fetcher interface {
	Fetch(context.Context, Validators) (FetchResult, error)
}
type FreshnessStatus string

const (
	FreshCurrent FreshnessStatus = "current"
	FreshStale   FreshnessStatus = "stale"
	FreshExpired FreshnessStatus = "expired"
)

type Freshness struct {
	Status           FreshnessStatus
	RetrievedAt      time.Time
	AttemptedAt      time.Time
	Age              time.Duration
	LastRefreshError string
}
type Cache struct {
	root   string
	client Fetcher
}
type activeState struct {
	SchemaVersion int        `json:"schema_version"`
	Generation    string     `json:"generation"`
	RetrievedAt   time.Time  `json:"retrieved_at"`
	Validators    Validators `json:"validators"`
}
type attemptState struct {
	SchemaVersion int       `json:"schema_version"`
	AttemptedAt   time.Time `json:"attempted_at"`
	Error         string    `json:"error"`
}

func NewCache(root string, client Fetcher) (*Cache, error) {
	if strings.TrimSpace(root) == "" || client == nil {
		return nil, errors.New("private cache root and fetcher are required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "sets"), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Join(root, "sets"), 0o700); err != nil {
		return nil, err
	}
	return &Cache{root: root, client: client}, nil
}

func (c *Cache) LoadOrRefresh(ctx context.Context, now time.Time) (set CatalogSet, fresh Freshness, retErr error) {
	if c == nil || c.client == nil {
		return CatalogSet{}, Freshness{}, errors.New("cache is not initialized")
	}
	now = now.UTC()
	lock, err := acquireCacheLock(ctx, filepath.Join(c.root, "refresh.lock"), 10*time.Second)
	if err != nil {
		return CatalogSet{}, Freshness{}, err
	}
	defer func() { retErr = errors.Join(retErr, lock.release()) }()
	current, currentErr := c.loadCurrent()
	attempt, _ := c.loadAttempt()
	if currentErr == nil {
		set = current.set
		fresh = classifyFreshness(current.state.RetrievedAt, attempt, now)
		if fresh.Status == FreshCurrent {
			return set, fresh, nil
		}
	}
	if !attempt.AttemptedAt.IsZero() && now.Sub(attempt.AttemptedAt) <= refreshInterval {
		if currentErr == nil && fresh.Status != FreshExpired {
			return set, fresh, nil
		}
		return set, fresh, errors.New("modelpricewatch refresh is throttled after a failed attempt")
	}
	validators := Validators{}
	if currentErr == nil {
		validators = current.state.Validators
	}
	next, fetchErr := c.client.Fetch(ctx, validators)
	if fetchErr == nil && next.NotModified {
		if currentErr != nil {
			fetchErr = errors.New("not-modified response without a validated cache")
		} else {
			next.Catalogs = current.set
			next.Validators = current.state.Validators
		}
	}
	if fetchErr == nil {
		fetchErr = validatePair(next.Catalogs)
	}
	if fetchErr != nil {
		_ = c.saveAttempt(attemptState{SchemaVersion: 1, AttemptedAt: now, Error: safeError(fetchErr)})
		if currentErr == nil {
			fresh = classifyFreshness(current.state.RetrievedAt, attemptState{AttemptedAt: now, Error: safeError(fetchErr)}, now)
			if fresh.Status != FreshExpired {
				return current.set, fresh, nil
			}
		}
		return set, fresh, fetchErr
	}
	state, err := c.publish(next.Catalogs, next.Validators, now)
	if err != nil {
		return CatalogSet{}, Freshness{}, err
	}
	_ = os.Remove(filepath.Join(c.root, "attempt.json"))
	return next.Catalogs, Freshness{Status: FreshCurrent, RetrievedAt: state.RetrievedAt, Age: 0}, nil
}

type cachedPair struct {
	state activeState
	set   CatalogSet
}

func (c *Cache) loadCurrent() (cachedPair, error) {
	body, err := os.ReadFile(filepath.Join(c.root, "active.json"))
	if err != nil {
		return cachedPair{}, err
	}
	var state activeState
	if err = json.Unmarshal(body, &state); err != nil || state.SchemaVersion != 1 || state.Generation == "" || state.RetrievedAt.IsZero() {
		return cachedPair{}, errors.New("invalid active catalog metadata")
	}
	dir := filepath.Join(c.root, "sets", state.Generation)
	mf, err := os.Open(filepath.Join(dir, "models.json"))
	if err != nil {
		return cachedPair{}, err
	}
	models, err := DecodeModels(mf, DefaultBodyLimit)
	mf.Close()
	if err != nil {
		return cachedPair{}, err
	}
	hf, err := os.Open(filepath.Join(dir, "history.json"))
	if err != nil {
		return cachedPair{}, err
	}
	history, err := DecodeHistory(hf, DefaultBodyLimit)
	hf.Close()
	if err != nil {
		return cachedPair{}, err
	}
	set := CatalogSet{Models: models, History: history}
	if err := validatePair(set); err != nil {
		return cachedPair{}, err
	}
	return cachedPair{state: state, set: set}, nil
}
func (c *Cache) loadAttempt() (attemptState, error) {
	body, err := os.ReadFile(filepath.Join(c.root, "attempt.json"))
	if err != nil {
		return attemptState{}, err
	}
	var a attemptState
	err = json.Unmarshal(body, &a)
	if err != nil || a.SchemaVersion != 1 {
		return attemptState{}, errors.New("invalid attempt metadata")
	}
	return a, nil
}

func (c *Cache) publish(set CatalogSet, validators Validators, now time.Time) (activeState, error) {
	models, history, err := encodePair(set)
	if err != nil {
		return activeState{}, err
	}
	sum := sha256.Sum256(append(append([]byte{}, models...), history...))
	generation := hex.EncodeToString(sum[:])
	final := filepath.Join(c.root, "sets", generation)
	if _, err := os.Stat(final); errors.Is(err, os.ErrNotExist) {
		tmp, err := os.MkdirTemp(filepath.Join(c.root, "sets"), ".new-")
		if err != nil {
			return activeState{}, err
		}
		defer os.RemoveAll(tmp)
		if err = os.Chmod(tmp, 0o700); err != nil {
			return activeState{}, err
		}
		if err = os.WriteFile(filepath.Join(tmp, "models.json"), models, 0o600); err != nil {
			return activeState{}, err
		}
		if err = os.WriteFile(filepath.Join(tmp, "history.json"), history, 0o600); err != nil {
			return activeState{}, err
		}
		if err = os.Rename(tmp, final); err != nil && !errors.Is(err, os.ErrExist) {
			return activeState{}, err
		}
	}
	state := activeState{SchemaVersion: 1, Generation: generation, RetrievedAt: now, Validators: validators}
	body, _ := json.Marshal(state)
	if err := atomicPrivateWrite(filepath.Join(c.root, "active.json"), body); err != nil {
		return activeState{}, err
	}
	return state, nil
}
func (c *Cache) saveAttempt(a attemptState) error {
	body, _ := json.Marshal(a)
	return atomicPrivateWrite(filepath.Join(c.root, "attempt.json"), body)
}
func atomicPrivateWrite(path string, body []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".replace-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err = file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func encodePair(set CatalogSet) ([]byte, []byte, error) {
	rows := make([]Listing, 0, len(set.Models.Listings))
	ids := make([]string, 0, len(set.Models.Listings))
	for id := range set.Models.Listings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rows = append(rows, set.Models.Listings[id])
	}
	models, err := json.Marshal(modelsWire{Count: set.Models.Count, Updated: set.Models.Updated, Data: rows})
	if err != nil {
		return nil, nil, err
	}
	history, err := json.Marshal(historyWire{Count: set.History.Count, Updated: set.History.Updated, Data: set.History.Models})
	return models, history, err
}
func validatePair(set CatalogSet) error {
	if set.Models.Count != len(set.Models.Listings) || set.History.Count != len(set.History.Models) || set.Models.Count != set.History.Count {
		return errors.New("catalog pair counts do not match")
	}
	for id := range set.Models.Listings {
		if _, ok := set.History.Models[id]; !ok {
			return fmt.Errorf("history is missing listing %q", id)
		}
	}
	return nil
}
func classifyFreshness(retrieved time.Time, attempt attemptState, now time.Time) Freshness {
	age := now.Sub(retrieved)
	if age < 0 {
		age = 0
	}
	status := FreshCurrent
	if age > refreshInterval {
		status = FreshStale
	}
	if age > maximumStaleAge {
		status = FreshExpired
	}
	return Freshness{Status: status, RetrievedAt: retrieved, AttemptedAt: attempt.AttemptedAt, Age: age, LastRefreshError: attempt.Error}
}
func safeError(err error) string {
	if err == nil {
		return ""
	}
	v := err.Error()
	if len(v) > 512 {
		v = v[:512]
	}
	return v
}
