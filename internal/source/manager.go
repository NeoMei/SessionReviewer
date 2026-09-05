package source

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/neomei/SessionReviewer/internal/memory"
)

var (
	ErrProviderUnavailable = errors.New("session provider is unavailable")
)

type NamedAdapter struct {
	Provider string
	Adapter  Adapter
	Required bool
}

type ProviderDiagnostic struct {
	Provider string `json:"provider"`
	Code     string `json:"code"`
}

// Manager composes provider adapters while preserving ownership of every
// opaque candidate and boundary lease at the adapter that issued it.
type Manager struct {
	registrations []NamedAdapter
	adapters      map[string]Adapter
}

func NewManager(adapters []NamedAdapter) (*Manager, error) {
	sorted, err := validateAndSortAdapters(adapters)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		registrations: sorted,
		adapters:      make(map[string]Adapter, len(sorted)),
	}
	for _, named := range sorted {
		manager.adapters[named.Provider] = named.Adapter
	}
	return manager, nil
}

// DiscoverAll discovers enabled providers in deterministic provider order.
// An optional provider's typed unavailability is isolated as a diagnostic;
// all other errors fail closed and release leases already returned.
func DiscoverAll(ctx context.Context, adapters []NamedAdapter) (Discovery, []ProviderDiagnostic, error) {
	manager, err := NewManager(adapters)
	if err != nil {
		return Discovery{}, nil, err
	}
	return manager.DiscoverAll(ctx)
}

func (manager *Manager) Discover(ctx context.Context) (Discovery, error) {
	discovery, _, err := manager.DiscoverAll(ctx)
	return discovery, err
}

func (manager *Manager) DiscoverAll(ctx context.Context) (combined Discovery, diagnostics []ProviderDiagnostic, returnedErr error) {
	if manager == nil {
		return Discovery{}, nil, errors.New("source adapter manager is required")
	}
	ownedCandidates := make([]Candidate, 0)
	defer func() {
		if returnedErr != nil {
			manager.abandonCandidates(ownedCandidates)
			combined = Discovery{}
		}
	}()
	for _, named := range manager.registrations {
		if err := ctx.Err(); err != nil {
			return Discovery{}, diagnostics, err
		}
		discovered, err := named.Adapter.Discover(ctx)
		if errors.Is(err, ErrProviderUnavailable) && !named.Required {
			abandonAdapterCandidates(named.Adapter, discovered.Candidates)
			diagnostics = append(diagnostics, ProviderDiagnostic{Provider: named.Provider, Code: "provider_unavailable"})
			continue
		}
		if err != nil {
			abandonAdapterCandidates(named.Adapter, discovered.Candidates)
			return Discovery{}, diagnostics, fmt.Errorf("discover %s: %w", named.Provider, err)
		}
		if err := appendVerifiedProvider(&combined, named, discovered); err != nil {
			abandonAdapterCandidates(named.Adapter, discovered.Candidates)
			return Discovery{}, diagnostics, err
		}
		ownedCandidates = append(ownedCandidates, discovered.Candidates...)
	}
	sortDiscovery(&combined)
	return combined, diagnostics, nil
}

func (manager *Manager) Freeze(ctx context.Context, candidate Candidate) (Boundary, error) {
	adapter, err := manager.adapterFor(candidate.Provider)
	if err != nil {
		return Boundary{}, err
	}
	boundary, err := adapter.Freeze(ctx, candidate)
	if err != nil {
		return Boundary{}, err
	}
	if boundary.Candidate.Provider != candidate.Provider {
		abandonAdapterBoundary(adapter, boundary)
		return Boundary{}, fmt.Errorf("source adapter %q spoofed boundary provider %q", candidate.Provider, boundary.Candidate.Provider)
	}
	return boundary, nil
}

func (manager *Manager) Decode(ctx context.Context, boundary Boundary, visit func(memory.ObservationRevision) error) (DecodeReport, error) {
	adapter, err := manager.adapterFor(boundary.Candidate.Provider)
	if err != nil {
		return DecodeReport{}, err
	}
	return adapter.Decode(ctx, boundary, visit)
}

func (manager *Manager) Read(ctx context.Context, ref memory.SourceRef, limit int64) ([]byte, error) {
	adapter, err := manager.adapterFor(ref.Provider)
	if err != nil {
		return nil, err
	}
	return adapter.Read(ctx, ref, limit)
}

func (manager *Manager) AbandonCandidate(candidate Candidate) {
	adapter, err := manager.adapterFor(candidate.Provider)
	if err == nil {
		abandonAdapterCandidate(adapter, candidate)
	}
}

func (manager *Manager) AbandonBoundary(boundary Boundary) {
	adapter, err := manager.adapterFor(boundary.Candidate.Provider)
	if err == nil {
		abandonAdapterBoundary(adapter, boundary)
	}
}

func (manager *Manager) adapterFor(provider string) (Adapter, error) {
	if manager == nil {
		return nil, errors.New("source adapter manager is required")
	}
	adapter, found := manager.adapters[provider]
	if !found {
		return nil, fmt.Errorf("source provider %q is not registered", provider)
	}
	return adapter, nil
}

func (manager *Manager) abandonCandidates(candidates []Candidate) {
	for _, candidate := range candidates {
		manager.AbandonCandidate(candidate)
	}
}

func validateAndSortAdapters(adapters []NamedAdapter) ([]NamedAdapter, error) {
	if len(adapters) == 0 {
		return nil, errors.New("at least one named source adapter is required")
	}
	sorted := append([]NamedAdapter(nil), adapters...)
	for index := 1; index < len(sorted); index++ {
		for cursor := index; cursor > 0 && sorted[cursor].Provider < sorted[cursor-1].Provider; cursor-- {
			sorted[cursor], sorted[cursor-1] = sorted[cursor-1], sorted[cursor]
		}
	}
	for index, named := range sorted {
		if !validProviderName(named.Provider) {
			return nil, fmt.Errorf("source provider %q is invalid", named.Provider)
		}
		if named.Adapter == nil {
			return nil, fmt.Errorf("source adapter %q is required", named.Provider)
		}
		if index > 0 && sorted[index-1].Provider == named.Provider {
			return nil, fmt.Errorf("source provider %q is registered more than once", named.Provider)
		}
	}
	return sorted, nil
}

func validProviderName(provider string) bool {
	if len(provider) < 1 || len(provider) > 128 || !lowercaseAlphaNumeric(provider[0]) {
		return false
	}
	for index := 1; index < len(provider); index++ {
		character := provider[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func lowercaseAlphaNumeric(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}

func appendVerifiedProvider(combined *Discovery, named NamedAdapter, discovered Discovery) error {
	for _, candidate := range discovered.Candidates {
		if candidate.Provider != named.Provider {
			return fmt.Errorf("source adapter %q spoofed candidate provider %q", named.Provider, candidate.Provider)
		}
	}
	for _, issue := range discovered.Issues {
		if issue.Provider != named.Provider {
			return fmt.Errorf("source adapter %q spoofed issue provider %q", named.Provider, issue.Provider)
		}
	}
	combined.Candidates = append(combined.Candidates, discovered.Candidates...)
	combined.Issues = append(combined.Issues, discovered.Issues...)
	return nil
}

func sortDiscovery(discovery *Discovery) {
	sort.SliceStable(discovery.Candidates, func(i, j int) bool {
		left, right := discovery.Candidates[i], discovery.Candidates[j]
		return left.Provider < right.Provider || left.Provider == right.Provider && left.SessionID < right.SessionID
	})
	sort.SliceStable(discovery.Issues, func(i, j int) bool {
		left, right := discovery.Issues[i], discovery.Issues[j]
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		return left.SessionID < right.SessionID || left.SessionID == right.SessionID && left.Path < right.Path
	})
}

func abandonAdapterCandidates(adapter Adapter, candidates []Candidate) {
	for _, candidate := range candidates {
		abandonAdapterCandidate(adapter, candidate)
	}
}

func abandonAdapterCandidate(adapter Adapter, candidate Candidate) {
	if lifecycle, ok := adapter.(LeaseLifecycle); ok {
		lifecycle.AbandonCandidate(candidate)
	}
}

func abandonAdapterBoundary(adapter Adapter, boundary Boundary) {
	if lifecycle, ok := adapter.(LeaseLifecycle); ok {
		lifecycle.AbandonBoundary(boundary)
	}
}

var _ Adapter = (*Manager)(nil)
var _ LeaseLifecycle = (*Manager)(nil)
