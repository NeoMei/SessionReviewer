package contextupdate

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

type scanPriceClock struct{ at time.Time }

func (c scanPriceClock) Now() time.Time { return c.at }

// scanPricingRequests uses only the already authenticated source accounting.
// Model labels do not establish the billing host or API/subscription mode.
func scanPricingRequests(projectID string, index sessionindex.Document, reports []ledger.SessionReport) ([]pricing.ResolutionRequest, error) {
	entries := map[string]sessionindex.Entry{}
	for _, entry := range index.Sessions {
		entries[entry.Provider+"/"+entry.SessionID] = entry
	}
	result := []pricing.ResolutionRequest{}
	for _, report := range reports {
		entry, ok := entries[report.SessionID]
		if !ok || entry.UsageRecordDigest == nil || report.Accounting == nil {
			return nil, errors.New("pricing requires authenticated source usage identity")
		}
		at, err := time.Parse(time.RFC3339Nano, report.Accounting.EndedAt)
		if err != nil {
			return nil, err
		}
		start, err := time.Parse(time.RFC3339Nano, report.Accounting.StartedAt)
		if err != nil {
			return nil, err
		}
		for _, model := range report.Accounting.Models {
			result = append(result, pricing.ResolutionRequest{ProjectID: projectID, Provider: entry.Provider, SessionID: entry.SessionID, UsageRecordDigest: *entry.UsageRecordDigest, Route: pricing.BillingRoute{ModelID: model.Model}, Usage: model.ModelUsage, StartedAt: start.UTC(), PricedAt: at.UTC()})
		}
	}
	return result, nil
}

func scanPriceIdentity(provider, sessionID, digest, model string) string {
	return strings.Join([]string{provider, sessionID, digest, model}, "\x00")
}

// applyScanPricing retains every historic price byte-for-byte. A new usage
// identity gets a pending snapshot until an exact billing route is known;
// periodic scans never silently reprice an existing identity.
func applyScanPricing(ctx context.Context, ledger *reviewv4.MachineLedger, requests []pricing.ResolutionRequest, now time.Time) error {
	service, err := pricing.NewService(nil, nil, map[string]pricing.UsageAdapter{"codex": pricing.CodexUsageAdapter{}}, scanPriceClock{now.UTC()})
	if err != nil {
		return err
	}
	effective := map[string]pricing.Snapshot{}
	for _, snapshot := range ledger.PricingSnapshots {
		if snapshot.Status == pricing.PriceSuperseded {
			continue
		}
		key := scanPriceIdentity(snapshot.Provider, snapshot.SessionID, snapshot.UsageRecordDigest, snapshot.BilledModelID)
		if _, exists := effective[key]; exists {
			return errors.New("multiple effective prices for one model usage")
		}
		effective[key] = snapshot
	}
	selected := []string{}
	seen := map[string]bool{}
	totals := map[string]float64{}
	incomplete := map[string]bool{}
	represented := map[string]bool{}
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return err
		}
		if request.ProjectID != ledger.ProjectID {
			return errors.New("pricing project mismatch")
		}
		key := scanPriceIdentity(request.Provider, request.SessionID, request.UsageRecordDigest, request.Usage.Model)
		if seen[key] {
			return errors.New("duplicate model usage")
		}
		seen[key] = true
		snapshot, exists := effective[key]
		if !exists {
			snapshot, err = service.Resolve(ctx, request)
			if err != nil {
				return err
			}
			ledger.PricingSnapshots = append(ledger.PricingSnapshots, snapshot)
			effective[key] = snapshot
		}
		selected = append(selected, snapshot.SnapshotID)
		represented[request.Usage.Model] = true
		if snapshot.TotalCostUSD == nil {
			incomplete[request.Usage.Model] = true
		} else {
			totals[request.Usage.Model] += *snapshot.TotalCostUSD
		}
	}
	sort.Strings(selected)
	ledger.CurrentPricingSnapshotIDs = selected
	complete := len(ledger.Accounting.Models) > 0
	total := 0.0
	for i := range ledger.Accounting.Models {
		model := &ledger.Accounting.Models[i]
		model.TotalCostUSD = nil
		if represented[model.Model] && !incomplete[model.Model] {
			value := totals[model.Model]
			model.TotalCostUSD = &value
			total += value
		} else {
			complete = false
		}
	}
	ledger.Accounting.TotalCostUSD = nil
	if complete {
		ledger.Accounting.TotalCostUSD = &total
	}
	return nil
}
