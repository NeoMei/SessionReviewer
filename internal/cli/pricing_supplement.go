package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

func applyPricingSupplement(ctx context.Context, request PricingRequest, input io.Reader) (pricing.Snapshot, error) {
	body, err := io.ReadAll(io.LimitReader(input, MaxDecisionInputBytes+1))
	if err != nil || len(body) > MaxDecisionInputBytes {
		return pricing.Snapshot{}, errors.New("pricing supplement exceeds input limit")
	}
	supplement, err := pricing.ParseSupplement(body)
	if err != nil {
		return pricing.Snapshot{}, err
	}
	if supplement.ProjectID != request.ProjectID || supplement.Provider != request.Provider || supplement.SessionID != request.SessionID || supplement.UsageRecordDigest != request.UsageRecordDigest {
		return pricing.Snapshot{}, errors.New("supplement identity differs from requested usage")
	}
	return applyAuthenticatedPricing(ctx, request, pricingTarget{ModelID: supplement.BilledModelID, SupersedesSnapshotID: supplement.SupersedesSnapshotID}, func(auth authenticatedPricingUsage) (pricing.Snapshot, error) {
		service, err := pricing.NewService(nil, nil, map[string]pricing.UsageAdapter{"codex": pricing.CodexUsageAdapter{}}, nil)
		if err != nil {
			return pricing.Snapshot{}, err
		}
		return service.Supplement(ctx, pricing.ResolutionRequest{ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, UsageRecordDigest: request.UsageRecordDigest, Route: pricing.BillingRoute{Host: supplement.BillingHost, ModelID: supplement.BilledModelID, Mode: supplement.BillingMode, Region: supplement.Region}, Usage: auth.Usage, StartedAt: auth.StartedAt, PricedAt: auth.EndedAt}, supplement, auth.Prior)
	})
}

type pricingTarget struct {
	ModelID              string
	SupersedesSnapshotID *string
}

type authenticatedPricingUsage struct {
	DataRoot  string
	Usage     accounting.ModelUsage
	StartedAt time.Time
	EndedAt   time.Time
	Prior     *pricing.Snapshot
}

func applyAuthenticatedPricing(ctx context.Context, request PricingRequest, target pricingTarget, build func(authenticatedPricingUsage) (pricing.Snapshot, error)) (pricing.Snapshot, error) {
	dataRoot := resolveDataDir(request.DataDir)
	_, mapping, _, err := resolveSyncMapping("", request.ProjectID, dataRoot)
	if err != nil {
		return pricing.Snapshot{}, err
	}
	owner, err := publicationlock.Acquire(dataRoot, request.ProjectID, 10*time.Second)
	if err != nil {
		return pricing.Snapshot{}, err
	}
	defer owner.Release()
	opts := publication.Options{ProjectID: request.ProjectID, DataRoot: dataRoot, Mapping: mapping, Now: time.Now}
	if err := publication.RecoverMarkdownLocked(ctx, opts, owner); err != nil {
		return pricing.Snapshot{}, err
	}
	read, err := syncproject.ReadMarkdownForScan(ctx, syncproject.Options{ProjectID: request.ProjectID, CWD: mapping.Root, DataDir: dataRoot, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI}, owner)
	if err != nil {
		return pricing.Snapshot{}, err
	}
	sum := sha256.Sum256(read.ProjectExpected[reviewv2.MachineLedgerRelativePath])
	if hex.EncodeToString(sum[:]) != request.ExpectedLedgerSHA256 {
		return pricing.Snapshot{}, ContractError{Code: "ledger_changed", Message: "pricing ledger changed; reload before confirming"}
	}
	index, err := sessionindex.Parse(read.ProjectExpected[presentation.SessionIndexRelativePath])
	if err != nil {
		return pricing.Snapshot{}, err
	}
	entryFound := false
	for _, entry := range index.Sessions {
		if entry.Provider == request.Provider && entry.SessionID == request.SessionID && entry.UsageRecordDigest != nil && *entry.UsageRecordDigest == request.UsageRecordDigest {
			entryFound = true
			break
		}
	}
	if !entryFound {
		return pricing.Snapshot{}, errors.New("usage is not in the authenticated current Session index")
	}
	catalog, err := sourcecatalog.Open(dataRoot)
	if err != nil {
		return pricing.Snapshot{}, err
	}
	defer catalog.Close()
	key := sourcecatalog.SnapshotKey{Provider: request.Provider, SessionID: request.SessionID}
	sources, err := catalog.SnapshotSources([]sourcecatalog.SnapshotKey{key})
	if err != nil {
		return pricing.Snapshot{}, err
	}
	source := sources[key]
	digest, err := memory.Digest(source.Record.Usage)
	if err != nil || !source.Found || digest != request.UsageRecordDigest {
		return pricing.Snapshot{}, errors.New("authenticated source usage changed")
	}
	associated := false
	for _, id := range source.Record.ProjectIDs {
		associated = associated || id == request.ProjectID
	}
	if !associated {
		return pricing.Snapshot{}, errors.New("source does not belong to project")
	}
	var usage accounting.ModelUsage
	matched := false
	for _, model := range source.Record.Usage.Models {
		if model.Model == target.ModelID {
			usage = model
			matched = true
			break
		}
	}
	if !matched {
		return pricing.Snapshot{}, errors.New("billed model is not present in captured usage")
	}
	ledger, err := reviewv4.DecodeLedger(read.ProjectExpected[reviewv2.MachineLedgerRelativePath])
	if err != nil {
		return pricing.Snapshot{}, err
	}
	var prior *pricing.Snapshot
	priorIndex := -1
	for i, snapshot := range ledger.PricingSnapshots {
		if snapshot.Provider == request.Provider && snapshot.SessionID == request.SessionID && snapshot.UsageRecordDigest == request.UsageRecordDigest && snapshot.BilledModelID == target.ModelID && snapshot.Status != pricing.PriceSuperseded {
			if prior != nil {
				return pricing.Snapshot{}, errors.New("ambiguous prior price")
			}
			copy := snapshot
			prior = &copy
			priorIndex = i
		}
	}
	at, err := time.Parse(time.RFC3339Nano, source.Record.Usage.EndedAt)
	if err != nil {
		return pricing.Snapshot{}, err
	}
	start, err := time.Parse(time.RFC3339Nano, source.Record.Usage.StartedAt)
	if err != nil {
		return pricing.Snapshot{}, err
	}
	if prior == nil {
		if target.SupersedesSnapshotID != nil {
			return pricing.Snapshot{}, errors.New("pricing predecessor does not exist")
		}
	} else if target.SupersedesSnapshotID == nil || *target.SupersedesSnapshotID != prior.SnapshotID {
		return pricing.Snapshot{}, errors.New("pricing predecessor differs from current snapshot")
	}
	snapshot, err := build(authenticatedPricingUsage{DataRoot: dataRoot, Usage: usage, StartedAt: start.UTC(), EndedAt: at.UTC(), Prior: prior})
	if err != nil {
		return pricing.Snapshot{}, err
	}
	if prior != nil && (snapshot.SupersedesSnapshotID == nil || *snapshot.SupersedesSnapshotID != prior.SnapshotID) {
		return pricing.Snapshot{}, errors.New("new pricing snapshot does not bind current predecessor")
	}
	if priorIndex >= 0 {
		ledger.PricingSnapshots[priorIndex].Status = pricing.PriceSuperseded
	}
	ledger.PricingSnapshots = append(ledger.PricingSnapshots, snapshot)
	ids := []string{}
	for _, id := range ledger.CurrentPricingSnapshotIDs {
		if prior == nil || id != prior.SnapshotID {
			ids = append(ids, id)
		}
	}
	ledger.CurrentPricingSnapshotIDs = append(ids, snapshot.SnapshotID)
	expected, err := expectedPricingUsage(catalog, index)
	if err != nil {
		return pricing.Snapshot{}, err
	}
	if err := recomputePricingAccounting(&ledger, expected); err != nil {
		return pricing.Snapshot{}, err
	}
	p := read.Pending.Presentation
	plan, err := presentation.RenderV4(presentation.V4RenderInput{Presentation: p, Ledger: ledger, Index: index, Previous: &read.AcceptedPair, Pending: &read.Pending.Documents, PreviousLedger: &read.OldAccepted.Ledger, ExpectedFiles: read.ProjectExpected})
	if err != nil {
		return pricing.Snapshot{}, err
	}
	// Human publication writes review/history/ledger and guards the immutable index.
	files := plan.Files[:0]
	for _, file := range plan.Files {
		if file.Relative != presentation.SessionIndexRelativePath {
			files = append(files, file)
		}
	}
	plan.Files = files
	edit := syncproject.MarkdownSyncPlan{Plan: plan, Index: read.ProjectExpected[presentation.SessionIndexRelativePath], ExpectedGenerationID: read.ExpectedGenerationID, ExpectedIndexDigest: read.ExpectedIndexDigest, VaultExpected: read.VaultExpected, ExpectedReceiptRevision: read.ExpectedReceiptRevision, ExpectedBaseDigest: read.ExpectedBaseDigest}
	if _, err := publication.PublishMarkdownEditLocked(ctx, opts, edit, owner); err != nil {
		return pricing.Snapshot{}, err
	}
	return snapshot, nil
}

func recomputePricingAccounting(ledger *reviewv4.MachineLedger, expected map[string]string) error {
	byID := map[string]pricing.Snapshot{}
	for _, s := range ledger.PricingSnapshots {
		byID[s.SnapshotID] = s
	}
	totals := map[string]float64{}
	unknown := map[string]bool{}
	seen := map[string]bool{}
	selected := map[string]bool{}
	for _, id := range ledger.CurrentPricingSnapshotIDs {
		s, ok := byID[id]
		if !ok {
			return fmt.Errorf("missing current snapshot %q", id)
		}
		key := pricingUsageKey(s.Provider, s.SessionID, s.UsageRecordDigest, s.BilledModelID)
		if _, ok := expected[key]; !ok {
			return errors.New("price is outside current authenticated usage")
		}
		selected[key] = true
		seen[s.BilledModelID] = true
		if s.TotalCostUSD == nil {
			unknown[s.BilledModelID] = true
		} else {
			totals[s.BilledModelID] += *s.TotalCostUSD
		}
	}
	for key, model := range expected {
		if !selected[key] {
			unknown[model] = true
		}
	}
	total := 0.0
	complete := len(ledger.Accounting.Models) > 0
	for i := range ledger.Accounting.Models {
		model := &ledger.Accounting.Models[i]
		model.TotalCostUSD = nil
		if seen[model.Model] && !unknown[model.Model] {
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

func pricingUsageKey(provider, session, digest, model string) string {
	return provider + "\x00" + session + "\x00" + digest + "\x00" + model
}

func expectedPricingUsage(catalog *sourcecatalog.Catalog, index sessionindex.Document) (map[string]string, error) {
	keys := []sourcecatalog.SnapshotKey{}
	for _, entry := range index.Sessions {
		if entry.UsageRecordDigest != nil {
			keys = append(keys, sourcecatalog.SnapshotKey{Provider: entry.Provider, SessionID: entry.SessionID})
		}
	}
	sources, err := catalog.SnapshotSources(keys)
	if err != nil {
		return nil, err
	}
	expected := map[string]string{}
	for _, entry := range index.Sessions {
		if entry.UsageRecordDigest == nil {
			continue
		}
		source := sources[sourcecatalog.SnapshotKey{Provider: entry.Provider, SessionID: entry.SessionID}]
		digest, err := memory.Digest(source.Record.Usage)
		if err != nil || !source.Found || digest != *entry.UsageRecordDigest {
			return nil, errors.New("current usage source changed; scan before supplementing")
		}
		for _, model := range source.Record.Usage.Models {
			expected[pricingUsageKey(entry.Provider, entry.SessionID, digest, model.Model)] = model.Model
		}
	}
	return expected, nil
}
