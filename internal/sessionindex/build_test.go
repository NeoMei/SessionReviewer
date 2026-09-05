package sessionindex

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
)

var buildTime = time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)

func TestBuildCreatesCumulativeMixedProviderIndex(t *testing.T) {
	views := make(map[SessionKey]*memory.SessionView, 153)
	dependencies := make([]memory.SessionViewDependency, 0, 153)
	measurements := make([]memory.SessionIndexMeasurement, 0, 153)
	for index := 0; index < 153; index++ {
		provider := []string{"codex", "claude", "opencode"}[index%3]
		id := fmt.Sprintf("session-%03d", index)
		view := buildView(provider, id, memory.Indexed, memory.SourceAvailable, index != 7)
		if index == 11 {
			view.Diagnostics = []memory.Diagnostic{{Code: "malformed_source_records"}}
		}
		views[SessionKey{Provider: provider, SessionID: id}] = &view
		dependencies = append(dependencies, memory.SessionViewDependency{Provider: provider, SessionID: id, Digest: view.Digest})
		measurements = append(measurements, memory.SessionIndexMeasurement{
			Provider: provider, SessionID: id, RecordCount: uint64ptr(2),
			Seen: 2, Indexed: 1, Collapsed: 1,
		})
	}
	errorView := buildView("claude", "same-native-id", memory.Unreadable, memory.SourceUnavailable, false)
	views[SessionKey{Provider: "claude", SessionID: "same-native-id"}] = &errorView
	dependencies = append(dependencies, memory.SessionViewDependency{Provider: "claude", SessionID: "same-native-id", Digest: errorView.Digest})
	measurements = append(measurements, memory.SessionIndexMeasurement{Provider: "claude", SessionID: "same-native-id", RecordCount: uint64ptr(1), Seen: 1, Undecodable: 1})

	previous := buildPreviousIndex(Entry{
		Provider: "codex", SessionID: "same-native-id", ProcessingState: ProcessingComplete,
		StateReasonCodes: []string{}, SourceAvailability: "available", RecordCount: uint64ptr(9),
		Coverage: Coverage{Seen: 9, Indexed: 9}, IndexedEventCount: 9,
		FactCounts: FactCounts{Verification: 2}, SessionViewDigest: stringptr(buildDigest("9")),
		LastSeenGenerationID: stringptr("generation-old"), LastSuccessfulGenerationID: stringptr("generation-old"),
	})
	manifest := buildManifest(dependencies, measurements)
	got, err := Build(BuildInput{ProjectView: buildProjectView(dependencies), Manifest: manifest, SessionViews: views, Previous: &previous, GeneratedAt: buildTime})
	if err != nil {
		t.Fatal(err)
	}
	if got.Coverage.Total != 155 || got.Coverage.Complete != 1 || got.Coverage.Partial != 153 || got.Coverage.Error != 1 || got.Coverage.SourceUnavailable != 2 {
		t.Fatalf("coverage=%+v", got.Coverage)
	}
	if len(got.Sessions) != 155 {
		t.Fatalf("sessions=%d", len(got.Sessions))
	}
	retained := requireBuildEntry(t, got, SessionKey{Provider: "codex", SessionID: "same-native-id"})
	if retained.ProcessingState != ProcessingComplete || retained.SourceAvailability != "unavailable" || retained.RecordCount == nil || *retained.RecordCount != 9 || retained.LastSeenGenerationID == nil || *retained.LastSeenGenerationID != "generation-old" {
		t.Fatalf("retained=%+v", retained)
	}
	unknown := requireBuildEntry(t, got, SessionKey{Provider: "claude", SessionID: "session-007"})
	if unknown.StartedAt != nil || unknown.EndedAt != nil || unknown.DurationMS != nil {
		t.Fatalf("unknown timestamps were fabricated: %+v", unknown)
	}
}

func TestBuildRetainsAbsentPriorSessionAsUnavailable(t *testing.T) {
	previous := buildPreviousIndex(Entry{
		Provider: "claude", SessionID: "old", ProcessingState: ProcessingComplete,
		StateReasonCodes: []string{}, SourceAvailability: "available", Coverage: Coverage{},
	})
	got, err := Build(BuildInput{ProjectView: buildProjectView(nil), Manifest: buildManifest(nil, nil), SessionViews: map[SessionKey]*memory.SessionView{}, Previous: &previous, GeneratedAt: buildTime})
	if err != nil {
		t.Fatal(err)
	}
	row := requireBuildEntry(t, got, SessionKey{Provider: "claude", SessionID: "old"})
	if row.ProcessingState != ProcessingComplete || row.SourceAvailability != "unavailable" {
		t.Fatalf("row=%+v", row)
	}
}

func TestBuildRejectsSessionCapacity(t *testing.T) {
	previous := buildPreviousIndex()
	previous.Sessions = make([]Entry, 65537)
	for index := range previous.Sessions {
		previous.Sessions[index] = Entry{Provider: "codex", SessionID: fmt.Sprintf("s-%05d", index), ProcessingState: ProcessingUnprocessed, StateReasonCodes: []string{}, SourceAvailability: "unavailable", Coverage: Coverage{}}
	}
	previous.Coverage = calculateIndexCoverage(previous.Sessions)
	_, err := Build(BuildInput{ProjectView: buildProjectView(nil), Manifest: buildManifest(nil, nil), SessionViews: map[SessionKey]*memory.SessionView{}, Previous: &previous, GeneratedAt: buildTime})
	if !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("error=%v", err)
	}
}

func TestBuildRejectsRenderedDocumentAbove64MiBWithCapacitySentinel(t *testing.T) {
	previous := buildPreviousIndex()
	previous.Sessions = make([]Entry, 65536)
	longTimestamp := "t" + strings.Repeat("0", 127)
	longTerminal := strings.Repeat("t", 64)
	longGeneration := strings.Repeat("g", 256)
	digest := buildDigest("8")
	for index := range previous.Sessions {
		sessionID := fmt.Sprintf("s-%05d-%s", index, strings.Repeat("x", 248))
		previous.Sessions[index] = Entry{
			Provider: "codex", SessionID: sessionID,
			ProcessingState: ProcessingComplete, StateReasonCodes: []string{}, SourceAvailability: "available",
			SourceTerminalState: &longTerminal, StartedAt: &longTimestamp, EndedAt: &longTimestamp,
			Coverage: Coverage{}, SessionViewDigest: &digest, UsageRecordDigest: &digest, SummaryDigest: &digest,
			LastSeenGenerationID: &longGeneration, LastSuccessfulGenerationID: &longGeneration,
		}
	}
	previous.Coverage = calculateIndexCoverage(previous.Sessions)
	_, err := Build(BuildInput{
		ProjectView: buildProjectView(nil), Manifest: buildManifest(nil, nil),
		SessionViews: map[SessionKey]*memory.SessionView{}, Previous: &previous, GeneratedAt: buildTime,
	})
	if !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("error=%v", err)
	}
}

func TestBuildRejectsUnauthenticatedExtraSessionView(t *testing.T) {
	view := buildView("codex", "extra", memory.Indexed, memory.SourceAvailable, true)
	_, err := Build(BuildInput{
		ProjectView: buildProjectView(nil), Manifest: buildManifest(nil, nil),
		SessionViews: map[SessionKey]*memory.SessionView{{Provider: "codex", SessionID: "extra"}: &view}, GeneratedAt: buildTime,
	})
	if err == nil || !strings.Contains(err.Error(), "do not match manifest") {
		t.Fatalf("extra SessionView accepted: %v", err)
	}
}

func TestBuildUnavailableCurrentViewPreservesAcceptedFactsAndState(t *testing.T) {
	view := buildView("codex", "same", memory.Missing, memory.SourceUnavailable, true)
	dependency := memory.SessionViewDependency{Provider: "codex", SessionID: "same", Digest: view.Digest}
	prior := Entry{
		Provider: "codex", SessionID: "same", ProcessingState: ProcessingComplete, StateReasonCodes: []string{},
		SourceAvailability: "available", RecordCount: uint64ptr(23), IndexedEventCount: 4,
		Coverage: Coverage{Seen: 4, Indexed: 4}, FactCounts: FactCounts{Artifact: 3},
		SessionViewDigest: stringptr(buildDigest("4")), LastSeenGenerationID: stringptr("generation-old"), LastSuccessfulGenerationID: stringptr("generation-old"),
	}
	previous := buildPreviousIndex(prior)
	got, err := Build(BuildInput{
		ProjectView: buildProjectView([]memory.SessionViewDependency{dependency}), Manifest: buildManifest([]memory.SessionViewDependency{dependency}, nil),
		SessionViews: map[SessionKey]*memory.SessionView{{Provider: "codex", SessionID: "same"}: &view}, Previous: &previous, GeneratedAt: buildTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := got.Sessions[0]
	if entry.ProcessingState != ProcessingComplete || entry.RecordCount == nil || *entry.RecordCount != 23 || entry.FactCounts.Artifact != 3 || entry.LastSuccessfulGenerationID == nil || *entry.LastSuccessfulGenerationID != "generation-old" {
		t.Fatalf("accepted facts were replaced: %+v", entry)
	}
}

func buildView(provider, sessionID string, terminal memory.TerminalState, availability memory.SourceAvailability, knownTime bool) memory.SessionView {
	view := memory.SessionView{
		ProjectID: "project-p", Provider: provider, SessionID: sessionID,
		TerminalState: terminal, SourceAvailability: availability,
		Digest: buildDigest(fmt.Sprintf("%064x", len(provider)+len(sessionID))), UsageRecordDigest: buildDigest("8"),
		ObservationSummaries: []memory.ObservationSummary{{RevisionID: buildDigest("7"), Sequence: 1, Kind: "test", Subject: "focused", OccurredAt: "2026-09-04T00:00:00Z"}},
	}
	if knownTime {
		view.StartedAt = "2026-09-04T00:00:00Z"
		view.EndedAt = "2026-09-04T00:01:00Z"
	}
	if terminal != memory.Indexed {
		view.ObservationSummaries = nil
	}
	return view
}

func buildManifest(dependencies []memory.SessionViewDependency, measurements []memory.SessionIndexMeasurement) memory.GenerationManifest {
	return memory.GenerationManifest{
		ProjectID: "project-p", GenerationID: "generation-new", ProjectViewDigest: buildDigest("1"),
		SessionViews: dependencies, SessionIndexMeasurements: measurements,
	}
}

func buildProjectView(dependencies []memory.SessionViewDependency) memory.ProjectView {
	return memory.ProjectView{ProjectID: "project-p", Digest: buildDigest("1"), SessionViewDependencies: dependencies}
}

func buildPreviousIndex(entries ...Entry) Document {
	doc := Document{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", Digest: buildDigest("0"), ProjectID: "project-p", GenerationID: "generation-old", ProjectViewDigest: buildDigest("2"), GeneratedAt: "2026-09-03T00:00:00Z", SortVersion: SortVersion, Sessions: entries}
	doc.Coverage = calculateIndexCoverage(entries)
	return doc
}

func requireBuildEntry(t *testing.T, doc Document, key SessionKey) Entry {
	t.Helper()
	for _, entry := range doc.Sessions {
		if entry.Provider == key.Provider && entry.SessionID == key.SessionID {
			return entry
		}
	}
	t.Fatalf("missing entry %+v", key)
	return Entry{}
}

func buildDigest(seed string) string {
	if len(seed) == 64 {
		return "sha256:" + seed
	}
	return "sha256:" + fmt.Sprintf("%064s", seed)
}

func stringptr(value string) *string { return &value }
func uint64ptr(value uint64) *uint64 { return &value }
