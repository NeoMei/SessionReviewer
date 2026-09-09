package pricing

import (
	"errors"
	"fmt"
	"sort"
)

type AggregateResult struct {
	KnownSubtotalUSD   float64
	TotalCostUSD       *float64
	PricingComplete    bool
	CurrentSnapshotIDs []string
}

func Aggregate(snapshots []Snapshot) (AggregateResult, error) {
	byID := map[string]Snapshot{}
	successor := map[string]string{}
	for _, s := range snapshots {
		if err := ValidateSnapshot(s); err != nil {
			return AggregateResult{}, fmt.Errorf("snapshot %q: %w", s.SnapshotID, err)
		}
		if _, exists := byID[s.SnapshotID]; exists {
			return AggregateResult{}, errors.New("duplicate snapshot id")
		}
		byID[s.SnapshotID] = s
	}
	for _, s := range snapshots {
		if s.SupersedesSnapshotID == nil {
			continue
		}
		parent, ok := byID[*s.SupersedesSnapshotID]
		if !ok {
			return AggregateResult{}, errors.New("superseded snapshot is missing")
		}
		if s.SnapshotID == parent.SnapshotID || identity(s) != identity(parent) {
			return AggregateResult{}, errors.New("invalid supersession identity")
		}
		if _, exists := successor[parent.SnapshotID]; exists {
			return AggregateResult{}, errors.New("pricing supersession forks")
		}
		successor[parent.SnapshotID] = s.SnapshotID
		if parent.Status != PriceSuperseded {
			return AggregateResult{}, errors.New("superseded predecessor is not marked superseded")
		}
	}
	for _, s := range snapshots {
		_, hasSuccessor := successor[s.SnapshotID]
		if s.Status == PriceSuperseded && !hasSuccessor {
			return AggregateResult{}, errors.New("superseded snapshot has no successor")
		}
		seen := map[string]bool{}
		current := s
		for current.SupersedesSnapshotID != nil {
			if seen[current.SnapshotID] {
				return AggregateResult{}, errors.New("pricing supersession cycle")
			}
			seen[current.SnapshotID] = true
			current = byID[*current.SupersedesSnapshotID]
		}
	}
	result := AggregateResult{PricingComplete: true, CurrentSnapshotIDs: []string{}}
	leaves := map[string]bool{}
	total := 0.0
	for _, s := range snapshots {
		if _, ok := successor[s.SnapshotID]; ok {
			continue
		}
		key := identity(s)
		if leaves[key] {
			return AggregateResult{}, errors.New("disconnected pricing leaves for usage identity")
		}
		leaves[key] = true
		result.CurrentSnapshotIDs = append(result.CurrentSnapshotIDs, s.SnapshotID)
		result.KnownSubtotalUSD += s.KnownSubtotalUSD
		if !s.PricingComplete {
			result.PricingComplete = false
		} else {
			total += *s.TotalCostUSD
		}
	}
	sort.Strings(result.CurrentSnapshotIDs)
	if result.PricingComplete {
		result.TotalCostUSD = &total
	}
	return result, nil
}
func identity(s Snapshot) string {
	return s.Provider + "\x00" + s.SessionID + "\x00" + s.UsageRecordDigest + "\x00" + s.BilledModelID
}
