package source

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/memory"
)

type managerFakeAdapter struct {
	discovery      Discovery
	discoverErr    error
	freezeBoundary Boundary
	freezeErr      error
	decodeReport   DecodeReport
	decodeErr      error
	readBody       []byte
	readErr        error

	discoverOrder       *[]string
	name                string
	afterDiscover       func()
	freezeCandidates    []Candidate
	decodeBoundaries    []Boundary
	readRefs            []memory.SourceRef
	abandonedCandidates []Candidate
	abandonedBoundaries []Boundary
}

func (adapter *managerFakeAdapter) Discover(context.Context) (Discovery, error) {
	if adapter.discoverOrder != nil {
		*adapter.discoverOrder = append(*adapter.discoverOrder, adapter.name)
	}
	if adapter.afterDiscover != nil {
		adapter.afterDiscover()
	}
	return adapter.discovery, adapter.discoverErr
}

func (adapter *managerFakeAdapter) Freeze(_ context.Context, candidate Candidate) (Boundary, error) {
	adapter.freezeCandidates = append(adapter.freezeCandidates, candidate)
	boundary := adapter.freezeBoundary
	if boundary.Candidate.Provider == "" {
		boundary.Candidate = candidate
	}
	return boundary, adapter.freezeErr
}

func (adapter *managerFakeAdapter) Decode(_ context.Context, boundary Boundary, _ func(memory.ObservationRevision) error) (DecodeReport, error) {
	adapter.decodeBoundaries = append(adapter.decodeBoundaries, boundary)
	return adapter.decodeReport, adapter.decodeErr
}

func (adapter *managerFakeAdapter) Read(_ context.Context, ref memory.SourceRef, _ int64) ([]byte, error) {
	adapter.readRefs = append(adapter.readRefs, ref)
	return adapter.readBody, adapter.readErr
}

func (adapter *managerFakeAdapter) AbandonCandidate(candidate Candidate) {
	adapter.abandonedCandidates = append(adapter.abandonedCandidates, candidate)
}

func (adapter *managerFakeAdapter) AbandonBoundary(boundary Boundary) {
	adapter.abandonedBoundaries = append(adapter.abandonedBoundaries, boundary)
}

func TestDiscoverAllKeepsOtherProvidersWhenOptionalAdapterUnavailable(t *testing.T) {
	order := []string{}
	codexCandidate := Candidate{Provider: "codex", SessionID: "same", Handle: "codex-handle", Lease: "codex-lease"}
	opencodeCandidate := Candidate{Provider: "opencode", SessionID: "same", Handle: "opencode-handle", Lease: "opencode-lease"}
	codex := &managerFakeAdapter{name: "codex", discoverOrder: &order, discovery: Discovery{Candidates: []Candidate{codexCandidate}}}
	claude := &managerFakeAdapter{name: "claude", discoverOrder: &order, discoverErr: ErrProviderUnavailable}
	opencode := &managerFakeAdapter{name: "opencode", discoverOrder: &order, discovery: Discovery{Candidates: []Candidate{opencodeCandidate}}}

	got, diagnostics, err := DiscoverAll(context.Background(), []NamedAdapter{
		{Provider: "opencode", Adapter: opencode},
		{Provider: "claude", Adapter: claude},
		{Provider: "codex", Adapter: codex, Required: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"claude", "codex", "opencode"}) {
		t.Fatalf("adapter discovery order=%v", order)
	}
	if !reflect.DeepEqual(got.Candidates, []Candidate{codexCandidate, opencodeCandidate}) {
		t.Fatalf("candidates=%+v", got.Candidates)
	}
	wantDiagnostics := []ProviderDiagnostic{{Provider: "claude", Code: "provider_unavailable"}}
	if !reflect.DeepEqual(diagnostics, wantDiagnostics) {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
}

func TestDiscoverAllKeepsSameNativeSessionAcrossThreeProviders(t *testing.T) {
	adapters := []NamedAdapter{
		{Provider: "opencode", Adapter: &managerFakeAdapter{discovery: Discovery{Candidates: []Candidate{{Provider: "opencode", SessionID: "same", Handle: "o"}}}}},
		{Provider: "codex", Adapter: &managerFakeAdapter{discovery: Discovery{Candidates: []Candidate{{Provider: "codex", SessionID: "same", Handle: "c"}}}}},
		{Provider: "claude", Adapter: &managerFakeAdapter{discovery: Discovery{Candidates: []Candidate{{Provider: "claude", SessionID: "same", Handle: "a"}}}}},
	}
	discovery, diagnostics, err := DiscoverAll(context.Background(), adapters)
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("diagnostics=%+v err=%v", diagnostics, err)
	}
	want := []string{"claude/same", "codex/same", "opencode/same"}
	got := make([]string, len(discovery.Candidates))
	for index, candidate := range discovery.Candidates {
		got[index] = candidate.Provider + "/" + candidate.SessionID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("same-native candidates=%v", got)
	}
}

func TestDiscoverAllSortsSessionsAndIssuesStablyWithinProvider(t *testing.T) {
	adapter := &managerFakeAdapter{discovery: Discovery{
		Candidates: []Candidate{
			{Provider: "codex", SessionID: "z", Handle: "z"},
			{Provider: "codex", SessionID: "a", Handle: "a-first"},
			{Provider: "codex", SessionID: "a", Handle: "a-second"},
		},
		Issues: []Issue{
			{Provider: "codex", SessionID: "z", Path: "b", Code: "z"},
			{Provider: "codex", SessionID: "a", Path: "same", Code: "first"},
			{Provider: "codex", SessionID: "a", Path: "a", Code: "a"},
			{Provider: "codex", SessionID: "a", Path: "same", Code: "second"},
		},
	}}
	discovery, diagnostics, err := DiscoverAll(context.Background(), []NamedAdapter{{Provider: "codex", Adapter: adapter}})
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("diagnostics=%+v err=%v", diagnostics, err)
	}
	gotCandidates := make([]string, len(discovery.Candidates))
	for index, candidate := range discovery.Candidates {
		gotCandidates[index] = candidate.Handle
	}
	if want := []string{"a-first", "a-second", "z"}; !reflect.DeepEqual(gotCandidates, want) {
		t.Fatalf("candidate order=%v want=%v", gotCandidates, want)
	}
	gotIssues := make([]string, len(discovery.Issues))
	for index, issue := range discovery.Issues {
		gotIssues[index] = issue.Code
	}
	if want := []string{"a", "first", "second", "z"}; !reflect.DeepEqual(gotIssues, want) {
		t.Fatalf("issue order=%v want=%v", gotIssues, want)
	}
}

func TestDiscoverAllConfiguredCorruptionFailsClosedAndReleasesLeases(t *testing.T) {
	corrupt := errors.New("configured provider is corrupt")
	candidate := Candidate{Provider: "codex", SessionID: "kept-only-on-success", Handle: "handle", Lease: "lease"}
	codex := &managerFakeAdapter{discovery: Discovery{Candidates: []Candidate{candidate}}}
	opencode := &managerFakeAdapter{discoverErr: corrupt}

	got, diagnostics, err := DiscoverAll(context.Background(), []NamedAdapter{
		{Provider: "codex", Adapter: codex, Required: true},
		{Provider: "opencode", Adapter: opencode, Required: true},
	})
	if !errors.Is(err, corrupt) || len(got.Candidates) != 0 || len(diagnostics) != 0 {
		t.Fatalf("got=%+v diagnostics=%+v err=%v", got, diagnostics, err)
	}
	if !reflect.DeepEqual(codex.abandonedCandidates, []Candidate{candidate}) {
		t.Fatalf("successful provider leases were not released: %+v", codex.abandonedCandidates)
	}
}

func TestDiscoverAllCancellationReleasesPriorProviderLeases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	candidate := Candidate{Provider: "codex", SessionID: "cancelled", Handle: "handle", Lease: "lease"}
	codex := &managerFakeAdapter{
		discovery:     Discovery{Candidates: []Candidate{candidate}},
		afterDiscover: cancel,
	}
	opencode := &managerFakeAdapter{}
	_, _, err := DiscoverAll(ctx, []NamedAdapter{
		{Provider: "codex", Adapter: codex, Required: true},
		{Provider: "opencode", Adapter: opencode},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was lost: %v", err)
	}
	if !reflect.DeepEqual(codex.abandonedCandidates, []Candidate{candidate}) {
		t.Fatalf("cancellation leaked candidate lease: %+v", codex.abandonedCandidates)
	}
}

func TestDiscoverAllRequiredProviderUnavailableFailsClosed(t *testing.T) {
	_, diagnostics, err := DiscoverAll(context.Background(), []NamedAdapter{{
		Provider: "codex", Adapter: &managerFakeAdapter{discoverErr: ErrProviderUnavailable}, Required: true,
	}})
	if !errors.Is(err, ErrProviderUnavailable) || len(diagnostics) != 0 {
		t.Fatalf("required unavailable provider diagnostics=%+v err=%v", diagnostics, err)
	}
}

func TestDiscoverAllRejectsProviderSpoofingAndDuplicateRegistration(t *testing.T) {
	spoofed := Candidate{Provider: "codex", SessionID: "same", Handle: "handle", Lease: "lease"}
	claude := &managerFakeAdapter{discovery: Discovery{Candidates: []Candidate{spoofed}}}
	if _, _, err := DiscoverAll(context.Background(), []NamedAdapter{{Provider: "claude", Adapter: claude}}); err == nil || !strings.Contains(err.Error(), "spoof") {
		t.Fatalf("spoofed provider was accepted: %v", err)
	}
	if !reflect.DeepEqual(claude.abandonedCandidates, []Candidate{spoofed}) {
		t.Fatalf("spoofed candidate lease was not released: %+v", claude.abandonedCandidates)
	}

	adapter := &managerFakeAdapter{}
	if _, _, err := DiscoverAll(context.Background(), []NamedAdapter{
		{Provider: "codex", Adapter: adapter},
		{Provider: "codex", Adapter: adapter},
	}); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("duplicate provider registration was accepted: %v", err)
	}
}

func TestManagerRoutesOpaqueLifecycleToOriginAdapter(t *testing.T) {
	codex := &managerFakeAdapter{}
	claude := &managerFakeAdapter{
		freezeBoundary: Boundary{Handle: "boundary-handle", Lease: "boundary-lease"},
		decodeReport:   DecodeReport{BoundaryRelation: BoundaryInitial},
		readBody:       []byte("claude-source"),
	}
	manager, err := NewManager([]NamedAdapter{
		{Provider: "codex", Adapter: codex, Required: true},
		{Provider: "claude", Adapter: claude},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{Provider: "claude", SessionID: "same", Handle: "candidate-handle", Lease: "candidate-lease"}
	boundary, err := manager.Freeze(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Decode(context.Background(), boundary, func(memory.ObservationRevision) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ref := memory.SourceRef{Provider: "claude", SessionID: "same"}
	body, err := manager.Read(context.Background(), ref, 16)
	if err != nil || string(body) != "claude-source" {
		t.Fatalf("read body=%q err=%v", body, err)
	}
	manager.AbandonCandidate(candidate)
	manager.AbandonBoundary(boundary)

	if len(codex.freezeCandidates)+len(codex.decodeBoundaries)+len(codex.readRefs)+len(codex.abandonedCandidates)+len(codex.abandonedBoundaries) != 0 {
		t.Fatalf("lifecycle escaped to Codex adapter: %+v", codex)
	}
	if !reflect.DeepEqual(claude.freezeCandidates, []Candidate{candidate}) ||
		!reflect.DeepEqual(claude.decodeBoundaries, []Boundary{boundary}) ||
		!reflect.DeepEqual(claude.readRefs, []memory.SourceRef{ref}) ||
		!reflect.DeepEqual(claude.abandonedCandidates, []Candidate{candidate}) ||
		!reflect.DeepEqual(claude.abandonedBoundaries, []Boundary{boundary}) {
		t.Fatalf("origin lifecycle calls were lost: %+v", claude)
	}
}
