package reviewv4

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/pricing"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func DecodePresentation(data []byte) (Presentation, error) {
	var presentation Presentation
	if err := strictjson.Decode(data, &presentation); err != nil {
		return presentation, err
	}
	if err := ValidatePresentation(presentation); err != nil {
		return presentation, err
	}
	return presentation, nil
}

func DecodeLedger(data []byte) (MachineLedger, error) {
	var ledger MachineLedger
	if err := strictjson.Decode(data, &ledger); err != nil {
		return ledger, err
	}
	if err := ValidateLedger(ledger); err != nil {
		return ledger, err
	}
	if !isZeroSHA(ledger.SyncHashes.LedgerSHA256) && CanonicalLedgerSHA256(ledger) != ledger.SyncHashes.LedgerSHA256 {
		return ledger, errors.New("machine ledger self digest mismatch")
	}
	return ledger, nil
}

func Parse(review, history, ledger []byte) (Accepted, error) {
	return parse(review, history, ledger, nil)
}
func LoadProjection(review, history, ledger, index []byte) (Accepted, error) {
	if len(index) == 0 {
		return Accepted{}, errors.New("session index is required")
	}
	return parse(review, history, ledger, index)
}

func parse(reviewBytes, historyBytes, ledgerBytes, indexBytes []byte) (Accepted, error) {
	var accepted Accepted
	var err error
	accepted.Review, err = DecodePresentation(reviewBytes)
	if err != nil {
		return accepted, fmt.Errorf("review: %w", err)
	}
	if len(historyBytes) > strictjson.MaxBytes || !utf8.Valid(historyBytes) {
		return accepted, errors.New("history exceeds the byte limit or is not UTF-8")
	}
	accepted.History = append([]byte(nil), historyBytes...)
	accepted.Ledger, err = DecodeLedger(ledgerBytes)
	if err != nil {
		return accepted, fmt.Errorf("ledger: %w", err)
	}
	if err := ValidateAccepted(accepted); err != nil {
		return accepted, err
	}
	if isZeroSHA(accepted.Ledger.SyncHashes.LedgerSHA256) {
		return accepted, errors.New("machine ledger self digest is unset")
	}
	if accepted.Ledger.ReviewSHA256 != sha256Hex(reviewBytes) || accepted.Ledger.HistorySHA256 != sha256Hex(historyBytes) {
		return accepted, errors.New("review or history content hash mismatch")
	}
	if len(indexBytes) > 0 {
		accepted.SessionIndex, err = sessionindex.Parse(indexBytes)
		if err != nil {
			return accepted, fmt.Errorf("session index: %w", err)
		}
		index := accepted.SessionIndex
		if index.Digest == "sha256:"+strings.Repeat("0", 64) {
			return accepted, errors.New("session index digest is unset")
		}
		if index.ProjectID != accepted.Review.ProjectID || index.GenerationID != accepted.Review.GenerationID || index.ProjectViewDigest != accepted.Review.ProjectViewDigest || index.Digest != accepted.Ledger.SyncHashes.SessionIndexDigest {
			return accepted, errors.New("session index identity, generation, or digest mismatch")
		}
	}
	return accepted, nil
}

func RenderLedger(ledger MachineLedger) ([]byte, error) {
	normalizeLedger(&ledger)
	ledger.SyncHashes.LedgerSHA256 = strings.Repeat("0", 64)
	if err := ValidateLedger(ledger); err != nil {
		return nil, err
	}
	ledger.SyncHashes.LedgerSHA256 = CanonicalLedgerSHA256(ledger)
	body, err := strictjson.Encode(ledger)
	if err != nil {
		return nil, err
	}
	parsed, err := DecodeLedger(body)
	if err != nil {
		return nil, fmt.Errorf("rendered machine ledger failed validation: %w", err)
	}
	if !reflect.DeepEqual(ledger, parsed) {
		return nil, errors.New("rendered machine ledger changed semantic value")
	}
	return body, nil
}

func CanonicalLedgerSHA256(ledger MachineLedger) string {
	ledger.SyncHashes.LedgerSHA256 = ""
	type syncWithoutSelf struct {
		ReviewSHA256       string `json:"review_sha256"`
		HistorySHA256      string `json:"history_sha256"`
		SessionIndexDigest string `json:"session_index_digest"`
	}
	type bodyWithoutSelf struct {
		SchemaVersion             int             `json:"schema_version"`
		MinimumReaderVersion      string          `json:"minimum_reader_version"`
		MinimumWriterVersion      string          `json:"minimum_writer_version"`
		ProjectID                 string          `json:"project_id"`
		GenerationID              string          `json:"generation_id"`
		ProjectViewDigest         string          `json:"project_view_digest"`
		AcceptedRevision          int             `json:"accepted_revision"`
		ReviewSHA256              string          `json:"review_sha256"`
		HistorySHA256             string          `json:"history_sha256"`
		Accounting                Accounting      `json:"accounting"`
		Sessions                  []LedgerSession `json:"sessions"`
		HumanPatches              []Patch         `json:"human_patches"`
		OrphanPatches             []Patch         `json:"orphan_patches"`
		GeneratedBaselines        []Baseline      `json:"generated_baselines"`
		PricingSnapshots          any             `json:"pricing_snapshots"`
		CurrentPricingSnapshotIDs []string        `json:"current_pricing_snapshot_ids"`
		SyncHashes                syncWithoutSelf `json:"sync_hashes"`
	}
	view := bodyWithoutSelf{ledger.SchemaVersion, ledger.MinimumReaderVersion, ledger.MinimumWriterVersion, ledger.ProjectID, ledger.GenerationID, ledger.ProjectViewDigest, ledger.AcceptedRevision, ledger.ReviewSHA256, ledger.HistorySHA256, ledger.Accounting, ledger.Sessions, ledger.HumanPatches, ledger.OrphanPatches, ledger.GeneratedBaselines, ledger.PricingSnapshots, ledger.CurrentPricingSnapshotIDs, syncWithoutSelf{ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256, ledger.SyncHashes.SessionIndexDigest}}
	body, err := strictjson.Encode(view)
	if err != nil {
		return ""
	}
	return sha256Hex(body)
}

func normalizeLedger(ledger *MachineLedger) {
	if ledger.Accounting.Models == nil {
		ledger.Accounting.Models = []Model{}
	}
	if ledger.Sessions == nil {
		ledger.Sessions = []LedgerSession{}
	}
	if ledger.HumanPatches == nil {
		ledger.HumanPatches = []Patch{}
	}
	if ledger.OrphanPatches == nil {
		ledger.OrphanPatches = []Patch{}
	}
	if ledger.GeneratedBaselines == nil {
		ledger.GeneratedBaselines = []Baseline{}
	}
	if ledger.PricingSnapshots == nil {
		ledger.PricingSnapshots = []pricing.Snapshot{}
	}
	for index := range ledger.PricingSnapshots {
		if ledger.PricingSnapshots[index].MissingBillingDimensions == nil {
			ledger.PricingSnapshots[index].MissingBillingDimensions = []string{}
		}
	}
	if ledger.CurrentPricingSnapshotIDs == nil {
		ledger.CurrentPricingSnapshotIDs = []string{}
	}
}
func sha256Hex(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func isZeroSHA(value string) bool  { return value == strings.Repeat("0", 64) }
