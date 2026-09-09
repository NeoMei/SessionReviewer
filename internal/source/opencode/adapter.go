package opencode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

const adapterVersion = "opencode-sqlite-v1"

type AdapterOptions struct {
	DatabasePath string
	Bindings     []projectidentity.Binding
	Catalog      *sourcecatalog.Catalog
	Redactor     *redact.Redactor
}
type snapshotSession struct {
	row           sessionRow
	binding       projectidentity.Binding
	records       []canonicalRecord
	deferred      bool
	identity      string
	baseline      source.CatalogBaselineSnapshot
	candidate     source.Candidate
	boundary      source.Boundary
	storageKey    string
	decoding      bool
	sessionTotals sessionTokenTotals
}
type retainedRecords struct {
	records    []canonicalRecord
	bytes      int
	references int
}
type adapter struct {
	options         AdapterOptions
	mu              sync.Mutex
	discoveryMu     sync.Mutex
	retainedBytes   int
	snapshots       map[string]*retainedRecords
	readable        map[string]*snapshotSession
	counter         uint64
	candidates      map[string]*snapshotSession
	boundaries      map[string]*snapshotSession
	candidateLeases map[string]bool
	boundaryLeases  map[string]bool
}

func New(options AdapterOptions) (source.Adapter, error) {
	if options.DatabasePath != "" && (!filepath.IsAbs(options.DatabasePath) || filepath.Clean(options.DatabasePath) != options.DatabasePath) {
		return nil, errors.New("OpenCode database must be an absolute clean path")
	}
	if len(options.Bindings) == 0 || options.Catalog == nil || options.Redactor == nil {
		return nil, errors.New("OpenCode authenticated bindings, catalog and redactor required")
	}
	seen := map[string]bool{}
	for _, b := range options.Bindings {
		if b.ProjectID == "" || seen[b.ProjectID] {
			return nil, errors.New("invalid OpenCode project bindings")
		}
		seen[b.ProjectID] = true
		if err := authenticateBinding(b); err != nil {
			return nil, err
		}
	}
	options.Bindings = append([]projectidentity.Binding(nil), options.Bindings...)
	return &adapter{options: options, candidates: map[string]*snapshotSession{}, boundaries: map[string]*snapshotSession{}, candidateLeases: map[string]bool{}, boundaryLeases: map[string]bool{}, snapshots: map[string]*retainedRecords{}, readable: map[string]*snapshotSession{}}, nil
}
func (a *adapter) Discover(ctx context.Context) (source.Discovery, error) {
	a.discoveryMu.Lock()
	defer a.discoveryMu.Unlock()
	if ctx == nil {
		return source.Discovery{}, errors.New("context required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if a.options.DatabasePath == "" {
		return source.Discovery{}, source.ErrProviderUnavailable
	}
	for _, b := range a.options.Bindings {
		if err := authenticateBinding(b); err != nil {
			return source.Discovery{}, err
		}
	}
	found := []*snapshotSession{}
	result := source.Discovery{Candidates: []source.Candidate{}, Issues: []source.Issue{}}
	totalBytes := 0
	err := withSnapshot(ctx, a.options.DatabasePath, func(db *sql.DB) error {
		if err := checkSchema(ctx, db); err != nil {
			return err
		}
		totalColumns, err := sessionTokenColumns(ctx, db)
		if err != nil {
			return err
		}
		rows, associated, err := projectSessions(ctx, db, a.options.Bindings)
		if err != nil {
			return err
		}
		for _, row := range rows {
			actual, messages, err := readSession(ctx, db, row.ID)
			if err != nil {
				return err
			}
			if actual != row {
				return errors.New("OpenCode Session metadata changed")
			}
			records, deferred, err := stableRecords(row, messages)
			if err != nil {
				return err
			}
			for _, r := range records {
				totalBytes += len(r.Raw)
			}
			if totalBytes > maxContentBytes {
				return errors.New("OpenCode project content budget exceeded")
			}
			identity, err := sourceIdentity(row)
			if err != nil {
				return err
			}
			sessionTotals, err := readSessionTokenTotals(ctx, db, row.ID, totalColumns)
			if err != nil {
				return err
			}
			found = append(found, &snapshotSession{row: row, binding: associated[row.ID], records: records, deferred: deferred, identity: identity, sessionTotals: sessionTotals})
		}
		return nil
	})
	if err != nil {
		return source.Discovery{}, err
	}
	keys := []sourcecatalog.SnapshotKey{}
	for _, s := range found {
		keys = append(keys, sourcecatalog.SnapshotKey{Provider: "opencode", SessionID: s.row.ID})
	}
	baselines, err := a.options.Catalog.SnapshotSources(keys)
	if err != nil {
		return source.Discovery{}, err
	}
	// Validate every prefix before publishing any candidate/lease.
	for _, s := range found {
		prior := baselines[sourcecatalog.SnapshotKey{Provider: "opencode", SessionID: s.row.ID}]
		s.baseline = source.CatalogBaselineSnapshot{ExpectedDigest: prior.Digest}
		if prior.Found {
			old := prior.Record
			s.baseline.PriorSource = &old
			n := old.FrozenBoundary.Location.RecordOrdinal()
			if old.SourceIdentity != s.identity || n < 1 || n > len(s.records) || prefixHash(s.records[:n]) != old.FrozenBoundary.SourceHash {
				return source.Discovery{}, errors.New("OpenCode accepted source prefix changed")
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Admission is all-or-none. Shared canonical bytes are counted once across
	// independent leases and the successful-decode read cache.
	addedBytes, addedSnapshots := 0, 0
	for _, s := range found {
		if len(s.records) == 0 {
			continue
		}
		s.storageKey = s.identity + ":" + prefixHash(s.records)
		if a.snapshots[s.storageKey] == nil {
			addedSnapshots++
			for _, record := range s.records {
				addedBytes += len(record.Raw)
			}
		}
	}
	if addedBytes > maxContentBytes-a.retainedBytes || addedSnapshots > maxSessions-len(a.snapshots) || len(found) > maxSessions-len(a.candidates)-len(a.boundaries) {
		return source.Discovery{}, errors.New("OpenCode retained snapshot budget exceeded")
	}
	for _, s := range found {
		if len(s.records) == 0 {
			result.Issues = append(result.Issues, source.Issue{Code: "no_finalized_records", Provider: "opencode", SessionID: s.row.ID, TerminalState: memory.Unsupported})
			continue
		}
		retained := a.snapshots[s.storageKey]
		if retained == nil {
			retained = &retainedRecords{records: s.records}
			for _, record := range s.records {
				retained.bytes += len(record.Raw)
			}
			a.retainedBytes += retained.bytes
			a.snapshots[s.storageKey] = retained
		}
		retained.references++
		s.records = retained.records
		a.counter++
		handle := "candidate-" + hashBytes([]byte(s.identity+prefixHash(s.records)+s.baseline.ExpectedDigest))
		s.baseline.Handle = "baseline-" + handle
		s.candidate = source.Candidate{Provider: "opencode", SessionID: s.row.ID, StartedAt: timestamp(s.row.Created), InitialCWD: s.row.Directory, Handle: handle, Lease: strconv.FormatUint(a.counter, 10), CatalogBaseline: cloneBaseline(s.baseline)}
		a.candidates[s.candidate.Lease] = s
		a.candidateLeases[s.candidate.Lease] = true
		result.Candidates = append(result.Candidates, cloneCandidate(s.candidate))
	}
	return result, nil
}
func cloneCandidate(c source.Candidate) source.Candidate {
	c.CatalogBaseline = cloneBaseline(c.CatalogBaseline)
	return c
}
func cloneBaseline(b source.CatalogBaselineSnapshot) source.CatalogBaselineSnapshot {
	if b.PriorSource != nil {
		r := *b.PriorSource
		r.ProjectIDs = append([]string(nil), r.ProjectIDs...)
		r.Usage.Models = append([]accounting.ModelUsage(nil), r.Usage.Models...)
		if r.FrozenBoundary.Location.Canonical != nil {
			c := *r.FrozenBoundary.Location.Canonical
			r.FrozenBoundary.Location.Canonical = &c
		}
		b.PriorSource = &r
	}
	return b
}
func cloneBoundary(b source.Boundary) source.Boundary {
	b.Candidate.CatalogBaseline = cloneBaseline(b.Candidate.CatalogBaseline)
	if b.Frozen.Location.Canonical != nil {
		c := *b.Frozen.Location.Canonical
		b.Frozen.Location.Canonical = &c
	}
	return b
}
func (a *adapter) Freeze(ctx context.Context, c source.Candidate) (source.Boundary, error) {
	if err := ctx.Err(); err != nil {
		return source.Boundary{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.candidates[c.Lease]
	if !ok || !a.candidateLeases[c.Lease] || !reflect.DeepEqual(c, s.candidate) {
		return source.Boundary{}, errors.New("unowned OpenCode candidate")
	}
	if err := authenticateBinding(s.binding); err != nil {
		return source.Boundary{}, err
	}
	delete(a.candidateLeases, c.Lease)
	delete(a.candidates, c.Lease)
	a.counter++
	b := source.Boundary{Candidate: c, SourceIdentity: s.identity, Frozen: memory.FrozenBoundary{Location: memory.SourceLocation{Kind: memory.SourceLocationCanonical, Canonical: &memory.CanonicalSourceLocation{Record: len(s.records)}}, SourceHash: prefixHash(s.records)}, TerminalState: memory.Indexed, Handle: "boundary-" + c.Handle, Lease: strconv.FormatUint(a.counter, 10)}
	s.boundary = cloneBoundary(b)
	a.boundaries[b.Lease] = s
	a.boundaryLeases[b.Lease] = true
	return cloneBoundary(b), nil
}
func (a *adapter) releaseSnapshot(s *snapshotSession) {
	retained := a.snapshots[s.storageKey]
	if retained == nil {
		return
	}
	retained.references--
	if retained.references == 0 {
		a.retainedBytes -= retained.bytes
		delete(a.snapshots, s.storageKey)
	}
}
func (a *adapter) AbandonCandidate(c source.Candidate) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.candidates[c.Lease]; s != nil && reflect.DeepEqual(c, s.candidate) {
		delete(a.candidateLeases, c.Lease)
		delete(a.candidates, c.Lease)
		a.releaseSnapshot(s)
	}
}
func (a *adapter) AbandonBoundary(b source.Boundary) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.boundaries[b.Lease]; s != nil && !s.decoding && reflect.DeepEqual(b, s.boundary) {
		delete(a.boundaryLeases, b.Lease)
		delete(a.boundaries, b.Lease)
		a.releaseSnapshot(s)
	}
}
func (a *adapter) Decode(ctx context.Context, b source.Boundary, visit func(memory.ObservationRevision) error) (source.DecodeReport, error) {
	if err := ctx.Err(); err != nil {
		return source.DecodeReport{}, err
	}
	a.mu.Lock()
	s, ok := a.boundaries[b.Lease]
	valid := ok && a.boundaryLeases[b.Lease] && reflect.DeepEqual(b, s.boundary) && visit != nil
	if valid {
		delete(a.boundaryLeases, b.Lease)
		s.decoding = true
	}
	a.mu.Unlock()
	if !valid {
		return source.DecodeReport{}, errors.New("unowned OpenCode boundary or nil visitor")
	}
	decoded := false
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.boundaries, b.Lease)
		s.decoding = false
		// Visible materialization happens after the scan releases Decode leases.
		// Keep one bounded snapshot per exact source prefix for subsequent reads.
		if decoded && a.readable[s.storageKey] == nil {
			a.readable[s.storageKey] = s
		} else {
			a.releaseSnapshot(s)
		}
	}()
	if err := authenticateBinding(s.binding); err != nil {
		return source.DecodeReport{}, err
	}
	usage, usageErr := recordUsage(s.records)
	if usageErr == nil && !s.deferred {
		usageErr = reconcileSessionTokenTotals(usage, s.sessionTotals)
	}
	report := source.DecodeReport{TerminalState: memory.Indexed, BoundaryRelation: source.BoundaryInitial, ExpectedCatalogDigest: s.baseline.ExpectedDigest}
	if usageErr != nil {
		usage = accounting.SessionUsage{StartedAt: timestamp(s.row.Created), EndedAt: timestamp(s.records[len(s.records)-1].Info.Time.Completed), Models: []accounting.ModelUsage{}}
		report.Diagnostics = append(report.Diagnostics, memory.Diagnostic{Code: "usage_unavailable"})
	}
	usage.StartedAt = timestamp(s.row.Created)
	usage.DurationMS = s.records[len(s.records)-1].Info.Time.Completed - s.row.Created
	if s.deferred {
		report.Diagnostics = append(report.Diagnostics, memory.Diagnostic{Code: "active_tail_deferred"})
		report.UnsupportedRecords = 1
	}
	count := uint64(len(s.records))
	report.RecordCount = &count
	report.ProposedSource = memory.SourceRecord{SchemaVersion: memory.MemorySchemaVersion, Provider: "opencode", SessionID: s.row.ID, SourceIdentity: s.identity, StartedAt: timestamp(s.row.Created), EndedAt: usage.EndedAt, FrozenBoundary: b.Frozen, Availability: memory.SourceAvailable, Usage: usage, ProjectIDs: []string{s.binding.ProjectID}}
	if prior := s.baseline.PriorSource; prior != nil {
		report.BoundaryRelation = source.BoundaryUnchanged
		if len(s.records) > prior.FrozenBoundary.Location.RecordOrdinal() {
			report.BoundaryRelation = source.BoundaryAppend
		} else if !reflect.DeepEqual(report.ProposedSource.Usage, prior.Usage) {
			if report.ProposedSource.StartedAt != prior.StartedAt || report.ProposedSource.EndedAt != prior.EndedAt {
				return report, errors.New("OpenCode usage refresh changed source time range")
			}
			report.BoundaryRelation = source.BoundaryUsageRefresh
		}
	}
	if err := memory.ValidateSourceRecord(report.ProposedSource); err != nil {
		return report, err
	}
	observations, diagnostics := s.observations(*a.options.Redactor)
	report.Diagnostics = append(report.Diagnostics, diagnostics...)
	for _, v := range observations {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := memory.ValidateObservationRevision(v); err != nil {
			return report, fmt.Errorf("OpenCode observation: %w", err)
		}
		if err := visit(v); err != nil {
			return report, err
		}
		report.EmittedRevisions++
	}
	decoded = true
	return report, nil
}
func (a *adapter) ReadVisiblePrefix(ctx context.Context, record memory.SourceRecord) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage, error) {
	if err := ctx.Err(); err != nil {
		return nil, conversationchain.VisibleCoverage{}, err
	}
	if memory.ValidateSourceRecord(record) != nil {
		return nil, conversationchain.VisibleCoverage{}, errors.New("invalid OpenCode source")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, snapshots := range []map[string]*snapshotSession{a.boundaries, a.readable} {
		for _, s := range snapshots {
			if s.identity == record.SourceIdentity && s.row.ID == record.SessionID && record.Provider == "opencode" && reflect.DeepEqual(record.ProjectIDs, []string{s.binding.ProjectID}) && reflect.DeepEqual(record.FrozenBoundary, s.boundary.Frozen) {
				messages, coverage := visibleRecords(s.records, s.row.ID, s.identity)
				return messages, coverage, nil
			}
		}
	}
	return nil, conversationchain.VisibleCoverage{}, errors.New("OpenCode source is not frozen by this adapter")
}
func (a *adapter) Read(ctx context.Context, ref memory.SourceRef, limit int64) ([]byte, error) {
	if err := source.ValidateReadLimit(limit); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := ref.Location.RecordOrdinal()
	if ref.Provider != "opencode" || ref.Location.Kind != memory.SourceLocationCanonical || n < 1 {
		return nil, errors.New("invalid OpenCode reference")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, snapshots := range []map[string]*snapshotSession{a.boundaries, a.readable} {
		for _, s := range snapshots {
			if s.identity != ref.SourceIdentity || s.row.ID != ref.SessionID || n > len(s.records) {
				continue
			}
			r := s.records[n-1]
			if r.Hash != ref.SourceHash {
				return nil, errors.New("OpenCode record hash mismatch")
			}
			return visibleRecordBytes(r, limit), nil
		}
	}
	return nil, errors.New("unowned OpenCode reference")
}
func ReadPublishedVisible(ctx context.Context, path string, record memory.SourceRecord) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage, error) {
	var coverage conversationchain.VisibleCoverage
	var messages []conversationchain.SourceMessage
	if ctx == nil || memory.ValidateSourceRecord(record) != nil || record.Provider != "opencode" || record.FrozenBoundary.Location.Kind != memory.SourceLocationCanonical || len(record.ProjectIDs) != 1 {
		return nil, coverage, errors.New("invalid OpenCode published source")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := withSnapshot(ctx, path, func(db *sql.DB) error {
		if err := checkSchema(ctx, db); err != nil {
			return err
		}
		row, raw, err := readSession(ctx, db, record.SessionID)
		if err != nil {
			return err
		}
		identity, err := sourceIdentity(row)
		if err != nil || identity != record.SourceIdentity {
			return errors.New("OpenCode published source identity mismatch")
		}
		records, _, err := stableRecords(row, raw)
		if err != nil {
			return err
		}
		n := record.FrozenBoundary.Location.RecordOrdinal()
		if n < 1 || n > len(records) || prefixHash(records[:n]) != record.FrozenBoundary.SourceHash {
			return errors.New("OpenCode published prefix changed")
		}
		messages, coverage = visibleRecords(records[:n], row.ID, identity)
		return nil
	})
	return messages, coverage, err
}
