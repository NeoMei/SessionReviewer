package sync

import (
	"errors"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/platform"
)

type EventDisposition string

const (
	EventIgnoredSelf EventDisposition = "ignored_self"
	EventDebounced   EventDisposition = "debounced"
	EventReady       EventDisposition = "ready"
)

type FileEvent struct {
	Side         Side
	RelativePath string
	ObservedHash string
	At           time.Time
}

type HashLookup interface {
	EntityForPath(Side, string) (string, bool, error)
	LastWrittenHash(string, Side) (string, bool, error)
}

type EventGate struct {
	mu       sync.Mutex
	window   time.Duration
	lookup   HashLookup
	goos     string
	caseMode platform.CaseMode
	pending  map[string]time.Time
}

func NewEventGate(window time.Duration, hashes HashLookup, goos string, caseMode platform.CaseMode) *EventGate {
	return &EventGate{window: window, lookup: hashes, goos: goos, caseMode: caseMode, pending: make(map[string]time.Time)}
}

func (gate *EventGate) Observe(event FileEvent) (EventDisposition, error) {
	if gate == nil || gate.lookup == nil || gate.window < 0 {
		return "", errors.New("invalid event gate configuration")
	}
	if event.Side != SideProject && event.Side != SideVault {
		return "", errors.New("invalid filesystem event side")
	}
	if event.At.IsZero() || !validObservedHash(event.ObservedHash) {
		return "", errors.New("invalid filesystem event")
	}
	pathKey, err := platform.PathKey(gate.goos, gate.caseMode, event.RelativePath)
	if err != nil {
		return "", errors.New("invalid filesystem event path")
	}
	if ignoredEventPath(pathKey) {
		return EventIgnoredSelf, nil
	}

	gate.mu.Lock()
	defer gate.mu.Unlock()
	entityID, found, err := gate.lookup.EntityForPath(event.Side, pathKey)
	if err != nil {
		return "", errors.New("cannot map filesystem event")
	}
	if !found {
		return EventIgnoredSelf, nil
	}
	if !stableBaseID.MatchString(entityID) {
		return "", errors.New("filesystem event mapped to invalid entity")
	}
	lastWritten, verified, err := gate.lookup.LastWrittenHash(entityID, event.Side)
	if err != nil {
		return "", errors.New("cannot verify filesystem event hash")
	}
	if verified && event.ObservedHash == lastWritten {
		return EventIgnoredSelf, nil
	}
	deadline := event.At.Add(gate.window)
	if current, exists := gate.pending[entityID]; !exists || deadline.After(current) {
		gate.pending[entityID] = deadline
	}
	if gate.window == 0 {
		return EventReady, nil
	}
	return EventDebounced, nil
}

func (gate *EventGate) Ready(now time.Time) []string {
	if gate == nil {
		return nil
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	ready := make([]string, 0, len(gate.pending))
	for entityID, deadline := range gate.pending {
		if !deadline.After(now) {
			ready = append(ready, entityID)
		}
	}
	sort.Strings(ready)
	for _, entityID := range ready {
		delete(gate.pending, entityID)
	}
	return ready
}

func validObservedHash(value string) bool {
	if len(value) > 128 || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func ignoredEventPath(pathKey string) bool {
	components := strings.Split(pathKey, "/")
	for _, component := range components {
		if component == "sync-conflicts" || component == ".session-reviewer" {
			return true
		}
	}
	leaf := path.Base(pathKey)
	if baseTempName.MatchString(leaf) {
		return true
	}
	backupSuffix := strings.TrimPrefix(atomicfile.BackupPath("x"), "x")
	return strings.HasSuffix(leaf, backupSuffix)
}
