package pricing

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"unicode"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

const (
	maxID                 = 256
	maxText               = 4096
	maxTimestamp          = 128
	maxURL                = 2048
	maxWireInteger uint64 = 1<<53 - 1
)

func validID(value string) bool { return len(value) <= maxID && idRE.MatchString(value) }
func bounded(value string, maximum int, nonempty bool) bool {
	return len(value) <= maximum && (!nonempty || value != "")
}
func validURL(value string) bool {
	if !bounded(value, maxURL, true) || len(value) <= len("https://") || !strings.HasPrefix(value, "https://") {
		return false
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return false
		}
	}
	return true
}
func validOptional(value *string, maximum int) bool { return value == nil || len(*value) <= maximum }
func validOptionalURL(value *string) bool           { return value == nil || validURL(*value) }
func validMoney(value *float64) bool {
	return value == nil || (!math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0)
}

func ValidateSnapshot(snapshot Snapshot) error {
	if snapshot.SchemaVersion != 1 || snapshot.MinimumReaderVersion != "0.4.0" ||
		!validID(snapshot.SnapshotID) || !validID(snapshot.ProjectID) || !validID(snapshot.Provider) || !validID(snapshot.SessionID) ||
		!digestRE.MatchString(snapshot.UsageRecordDigest) || !bounded(snapshot.BillingHost, maxText, true) ||
		!bounded(snapshot.BilledModelID, maxText, true) || !bounded(snapshot.BillingMode, maxText, true) ||
		!validID(snapshot.BillingRuleVersion) || !validOptional(snapshot.Region, maxTimestamp) ||
		!bounded(snapshot.PricedAt, maxTimestamp, true) || !bounded(snapshot.CreatedAt, maxTimestamp, true) ||
		!validOptional(snapshot.ModelPriceWatchListingID, maxID) || !validOptionalURL(snapshot.SourceURL) || !validOptionalURL(snapshot.DetailURL) ||
		!validOptional(snapshot.SourceLastUpdated, maxTimestamp) || !validOptional(snapshot.RetrievedAt, maxTimestamp) ||
		!validOptional(snapshot.PromoUntil, maxTimestamp) || !validOptional(snapshot.SupersedesSnapshotID, maxID) ||
		!bounded(snapshot.AuditReason, maxText, true) {
		return errors.New("invalid pricing snapshot fields")
	}
	switch snapshot.Status {
	case PricePending, PriceCurrent, PricePromotion, PriceStaleEstimate, PriceManualSupplement, PriceAmbiguous, PriceLegacyUnverified, PriceSuperseded:
	default:
		return fmt.Errorf("invalid price status %q", snapshot.Status)
	}
	switch snapshot.SourceKind {
	case "modelpricewatch", "official", "manual", "unresolved":
	default:
		return fmt.Errorf("invalid pricing source kind %q", snapshot.SourceKind)
	}
	if snapshot.SourceKind == "unresolved" {
		if snapshot.SourceURL != nil || snapshot.PricingComplete {
			return errors.New("unresolved pricing cannot carry resolved source evidence")
		}
	} else if snapshot.SourceURL == nil {
		return errors.New("resolved pricing requires HTTPS source evidence")
	}
	if snapshot.SourceKind == "modelpricewatch" && (snapshot.ModelPriceWatchListingID == nil || *snapshot.ModelPriceWatchListingID == "" || snapshot.RetrievedAt == nil) {
		return errors.New("modelpricewatch pricing requires listing and retrieval evidence")
	}
	amounts := []*float64{
		snapshot.Rates.Input, snapshot.Rates.CachedInput, snapshot.Rates.CacheWriteInput, snapshot.Rates.Output, snapshot.Rates.ReasoningOutput,
		snapshot.LineCostsUSD.Input, snapshot.LineCostsUSD.CachedInput, snapshot.LineCostsUSD.CacheWriteInput, snapshot.LineCostsUSD.Output, snapshot.LineCostsUSD.ReasoningOutput,
		snapshot.TotalCostUSD,
	}
	for _, value := range amounts {
		if !validMoney(value) {
			return errors.New("pricing amount must be finite and nonnegative")
		}
	}
	if math.IsNaN(snapshot.KnownSubtotalUSD) || math.IsInf(snapshot.KnownSubtotalUSD, 0) || snapshot.KnownSubtotalUSD < 0 {
		return errors.New("known subtotal must be finite and nonnegative")
	}
	if len(snapshot.MissingBillingDimensions) > 32 {
		return errors.New("too many missing billing dimensions")
	}
	missing := map[string]bool{}
	for _, dimension := range snapshot.MissingBillingDimensions {
		if !bounded(dimension, maxText, true) || missing[dimension] {
			return errors.New("invalid or duplicate missing billing dimension")
		}
		missing[dimension] = true
	}
	rates := []*float64{snapshot.Rates.Input, snapshot.Rates.CachedInput, snapshot.Rates.CacheWriteInput, snapshot.Rates.Output, snapshot.Rates.ReasoningOutput}
	quantities := []uint64{snapshot.BillableQuantities.Input, snapshot.BillableQuantities.CachedInput, snapshot.BillableQuantities.CacheWriteInput, snapshot.BillableQuantities.Output, snapshot.BillableQuantities.ReasoningOutput}
	costs := []*float64{snapshot.LineCostsUSD.Input, snapshot.LineCostsUSD.CachedInput, snapshot.LineCostsUSD.CacheWriteInput, snapshot.LineCostsUSD.Output, snapshot.LineCostsUSD.ReasoningOutput}
	names := []string{"input", "cached_input", "cache_write_input", "output", "reasoning_output"}
	subtotal := 0.0
	for i := range rates {
		if quantities[i] > maxWireInteger {
			return fmt.Errorf("billable quantity %s exceeds wire integer limit", names[i])
		}
		if quantities[i] > 0 && (rates[i] == nil || costs[i] == nil) && !missing[names[i]] {
			return fmt.Errorf("unknown billed dimension %s is not reported", names[i])
		}
		if rates[i] != nil && costs[i] != nil {
			expected := float64(quantities[i]) * *rates[i] / 1_000_000
			if !nearlyEqual(*costs[i], expected) {
				return fmt.Errorf("line cost %s does not match rate and quantity", names[i])
			}
			subtotal += *costs[i]
		} else if (rates[i] == nil) != (costs[i] == nil) {
			return fmt.Errorf("rate and line cost availability disagree for %s", names[i])
		}
	}
	if !nearlyEqual(snapshot.KnownSubtotalUSD, subtotal) {
		return errors.New("known subtotal does not equal known line costs")
	}
	if snapshot.PricingComplete {
		for _, value := range append(rates, costs...) {
			if value == nil {
				return errors.New("complete pricing contains an unknown amount")
			}
		}
		if snapshot.TotalCostUSD == nil || len(snapshot.MissingBillingDimensions) != 0 || !nearlyEqual(*snapshot.TotalCostUSD, snapshot.KnownSubtotalUSD) {
			return errors.New("complete pricing total or missing dimensions do not reconcile")
		}
	} else if snapshot.TotalCostUSD != nil {
		return errors.New("incomplete pricing total must be null")
	}
	return nil
}

func ValidateSupplement(supplement Supplement) error {
	if supplement.SchemaVersion != 1 || supplement.MinimumReaderVersion != "0.4.0" ||
		!validID(supplement.ProjectID) || !validID(supplement.Provider) || !validID(supplement.SessionID) ||
		!digestRE.MatchString(supplement.UsageRecordDigest) || !bounded(supplement.BillingHost, maxText, true) ||
		!bounded(supplement.BilledModelID, maxText, true) || !bounded(supplement.BillingMode, maxText, true) ||
		!validID(supplement.BillingRuleVersion) || !validOptional(supplement.Region, maxTimestamp) ||
		!bounded(supplement.EffectiveFrom, maxTimestamp, true) || !validOptional(supplement.EffectiveUntil, maxTimestamp) ||
		!validURL(supplement.SourceURL) || !validOptionalURL(supplement.DetailURL) ||
		!bounded(supplement.AuditReason, maxText, true) || !validOptional(supplement.SupersedesSnapshotID, maxID) {
		return errors.New("invalid pricing supplement fields")
	}
	for _, value := range []*float64{supplement.Rates.Input, supplement.Rates.CachedInput, supplement.Rates.CacheWriteInput, supplement.Rates.Output, supplement.Rates.ReasoningOutput} {
		if !validMoney(value) {
			return errors.New("supplement rate must be finite and nonnegative")
		}
	}
	return nil
}

func nearlyEqual(left, right float64) bool {
	delta := math.Abs(left - right)
	return delta <= 1e-12*math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
}

func Parse(data []byte) (Snapshot, error) {
	var snapshot Snapshot
	if err := strictjson.Decode(data, &snapshot); err != nil {
		return snapshot, err
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		return snapshot, strictjson.NewRejection(strictjson.CodeContractInvalid, err)
	}
	return snapshot, nil
}

func Render(snapshot Snapshot) ([]byte, error) {
	if snapshot.MissingBillingDimensions == nil {
		snapshot.MissingBillingDimensions = []string{}
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		return nil, err
	}
	return encodeRoundTrip(snapshot, ValidateSnapshot)
}

func ParseSupplement(data []byte) (Supplement, error) {
	var supplement Supplement
	if err := strictjson.Decode(data, &supplement); err != nil {
		return supplement, err
	}
	if err := ValidateSupplement(supplement); err != nil {
		return supplement, strictjson.NewRejection(strictjson.CodeContractInvalid, err)
	}
	return supplement, nil
}

func RenderSupplement(supplement Supplement) ([]byte, error) {
	if err := ValidateSupplement(supplement); err != nil {
		return nil, err
	}
	body, err := strictjson.Encode(supplement)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseSupplement(body)
	if err != nil {
		return nil, fmt.Errorf("rendered supplement failed validation: %w", err)
	}
	if !reflect.DeepEqual(supplement, parsed) {
		return nil, errors.New("rendered supplement changed semantic value")
	}
	return body, nil
}

func encodeRoundTrip(value Snapshot, validate func(Snapshot) error) ([]byte, error) {
	body, err := strictjson.Encode(value)
	if err != nil {
		return nil, err
	}
	parsed, err := Parse(body)
	if err != nil {
		return nil, fmt.Errorf("rendered snapshot failed validation: %w", err)
	}
	if err := validate(parsed); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(value, parsed) {
		return nil, errors.New("rendered snapshot changed semantic value")
	}
	return body, nil
}
