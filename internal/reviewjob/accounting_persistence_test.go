package reviewjob

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/agent"
)

func TestReviewAccountingPersistsPrivatelyAndProjectsPartialPricingWithoutFakeZero(t *testing.T) {
	snapshot := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	resolver := fixturePricingResolver{"known": fixturePricing(1, 0, 0, 1)}
	value, err := AddReviewResult(ReviewAccounting{}, agent.Result{Model: "known", Usage: accounting.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}}, snapshot, resolver)
	if err != nil {
		t.Fatal(err)
	}
	value, err = AddReviewResult(value, agent.Result{Model: "", Usage: accounting.TokenUsage{InputTokens: 20, OutputTokens: 3, TotalTokens: 23}}, snapshot, resolver)
	if err != nil {
		t.Fatal(err)
	}

	job := validJobFixture()
	job.ReviewAccounting = value
	store := Store{Root: newStoreRoot(t)}
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	loaded, _, found, err := store.Load(job.ID)
	if err != nil || !found {
		t.Fatalf("Load() found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(loaded.ReviewAccounting, value) || len(loaded.ReviewAccounting.Models) != 2 {
		t.Fatalf("private accounting roundtrip=%+v want=%+v", loaded.ReviewAccounting, value)
	}
	status, err := ProjectStatus(&loaded, loaded.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if status.ReviewUsage == nil || status.ReviewUsage.TotalTokens != 35 || status.ReviewUsage.PricingComplete || status.ReviewUsage.TotalCostUSD != nil {
		t.Fatalf("partial public projection=%+v", status.ReviewUsage)
	}
	body := mustJSON(t, status)
	if bytes.Contains(body, []byte(`"total_cost_usd"`)) || bytes.Contains(body, []byte(`"models"`)) || bytes.Contains(body, []byte(`"known"`)) || bytes.Contains(body, []byte(`"pricing"`)) {
		t.Fatalf("public projection leaked private detail or fake cost: %s", body)
	}
	validateAgainstSchema(t, "../../schemas/review-job-status-v1.schema.json", body)
}

func TestReviewAccountingCompletePublicProjectionIncludesValidatedCost(t *testing.T) {
	snapshot := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	value, err := AddReviewResult(ReviewAccounting{}, agent.Result{Model: "known", Usage: accounting.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}}, snapshot, fixturePricingResolver{"known": fixturePricing(1, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	job := validJobFixture()
	job.ReviewAccounting = value
	status, err := ProjectStatus(&job, job.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if status.ReviewUsage == nil || status.ReviewUsage.TotalCostUSD == nil || math.Abs(*status.ReviewUsage.TotalCostUSD-.000012) > 1e-15 || !status.ReviewUsage.PricingComplete {
		t.Fatalf("complete projection=%+v", status.ReviewUsage)
	}
}

func TestReviewAccountingReadsLegacyStoredShapeWithoutPublishingUnverifiedCost(t *testing.T) {
	legacy := []byte(`{"token_usage":{"input_tokens":10,"cached_input_tokens":2,"cache_write_input_tokens":1,"output_tokens":3,"reasoning_output_tokens":1,"total_tokens":13},"cost_usd":99}`)
	var value ReviewAccounting
	if err := json.Unmarshal(legacy, &value); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReviewAccounting(value); err != nil {
		t.Fatalf("legacy accounting rejected: %v", err)
	}
	roundtrip, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(roundtrip, legacy) {
		t.Fatalf("legacy canonical shape changed\ngot =%s\nwant=%s", roundtrip, legacy)
	}
	job := validJobFixture()
	job.ReviewAccounting = value
	status, err := ProjectStatus(&job, job.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if status.ReviewUsage == nil || status.ReviewUsage.TotalTokens != 13 || status.ReviewUsage.TotalCostUSD != nil || status.ReviewUsage.PricingComplete {
		t.Fatalf("legacy public projection invented cost: %+v", status.ReviewUsage)
	}
}

func TestReviewAccountingStoreLoadsCanonicalLegacyJobWithoutPublishingUnverifiedCost(t *testing.T) {
	root := newStoreRoot(t)
	store := Store{Root: root}
	job := validJobFixture()
	if _, err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "review-jobs", "jobs", job.ID+".json")
	body := readFile(t, path)
	legacy := legacyReviewUsage{
		TokenUsage: accounting.TokenUsage{InputTokens: 10, CachedInputTokens: 2, CacheWriteInputTokens: 1, OutputTokens: 3, ReasoningOutputTokens: 1, TotalTokens: 13},
		CostUSD:    99,
	}
	legacyBody, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	legacyBody = bytes.ReplaceAll(legacyBody, []byte("\n"), []byte("\n    "))
	body = replaceCanonicalObject(t, body, []byte(`"review_usage": `), legacyBody)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, revision, found, err := store.Load(job.ID)
	if err != nil || !found || revision != 1 {
		t.Fatalf("legacy Load() revision=%d found=%v err=%v", revision, found, err)
	}
	status, err := ProjectStatus(&loaded, loaded.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if status.ReviewUsage == nil || status.ReviewUsage.TotalTokens != 13 || status.ReviewUsage.TotalCostUSD != nil || status.ReviewUsage.PricingComplete {
		t.Fatalf("legacy stored projection=%+v", status.ReviewUsage)
	}
}

func replaceCanonicalObject(t *testing.T, body, field, replacement []byte) []byte {
	t.Helper()
	fieldAt := bytes.Index(body, field)
	if fieldAt < 0 {
		t.Fatalf("field %q absent from %s", field, body)
	}
	start := fieldAt + len(field)
	if start >= len(body) || body[start] != '{' {
		t.Fatalf("field %q is not an object in %s", field, body)
	}
	depth := 0
	inString := false
	escaped := false
	end := -1
	for index := start; index < len(body); index++ {
		character := body[index]
		if inString {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = index + 1
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		t.Fatalf("unterminated field %q in %s", field, body)
	}
	result := append([]byte(nil), body[:start]...)
	result = append(result, replacement...)
	result = append(result, body[end:]...)
	return result
}
