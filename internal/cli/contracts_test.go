package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const contractTestSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const contractTestDigest = "sha256:" + contractTestSHA

func contractCode(err error) string {
	var contractErr ContractError
	if errors.As(err, &contractErr) {
		return contractErr.Code
	}
	var contractErrPtr *ContractError
	if errors.As(err, &contractErrPtr) && contractErrPtr != nil {
		return contractErrPtr.Code
	}
	return ""
}

func TestParseInspectContractAcceptsExactAllowlist(t *testing.T) {
	tests := [][]string{
		{"session-summary", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1", "--expected-generation-id", "generation-1", "--json"},
		{"session-events", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1", "--expected-generation-id", "generation-1", "--limit", "1", "--json"},
		{"session-events", "--json", "--limit", "100", "--anchor", "2", "--expected-generation-id", "generation-1", "--session-id", "session-1", "--provider", "codex", "--project-id", "project-p"},
		{"session-events", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1", "--expected-generation-id", "generation-1", "--cursor", "opaque", "--limit", "100", "--json"},
		{"session-search", "--project-id", "project-p", "--expected-generation-id", "generation-1", "--query-kind", "branch", "--query", "feature/login", "--limit", "1", "--json"},
		{"session-search", "--json", "--cursor", "opaque", "--limit", "100", "--query", "timeout", "--query-kind", "error", "--expected-generation-id", "generation-1", "--project-id", "project-p"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			if _, err := ParseInspectContract(args); err != nil {
				t.Fatalf("args=%v err=%v code=%q", args, err, contractCode(err))
			}
		})
	}
}

func TestParseInspectContractRejectsExactInvalidArgv(t *testing.T) {
	tooLong := strings.Repeat("q", MaxInspectQueryBytes+1)
	tooLongCursor := strings.Repeat("c", MaxOpaqueCursorBytes+1)
	tests := []struct {
		name string
		args []string
	}{
		{"missing subcommand", nil},
		{"unknown subcommand", []string{"sessions", "--json"}},
		{"summary missing required", []string{"session-summary", "--project-id", "project-p", "--json"}},
		{"summary unknown flag", []string{"session-summary", "--project-id", "project-p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--input", "x", "--json"}},
		{"summary duplicate flag", []string{"session-summary", "--project-id", "project-p", "--project-id", "project-p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--json"}},
		{"summary duplicate json", []string{"session-summary", "--project-id", "project-p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--json", "--json"}},
		{"summary extra positional", []string{"session-summary", "--project-id", "project-p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--json", "extra"}},
		{"summary empty id", []string{"session-summary", "--project-id", "", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--json"}},
		{"summary unsafe id", []string{"session-summary", "--project-id", "../project", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--json"}},
		{"summary invalid utf8", []string{"session-summary", "--project-id", string([]byte{0xff}), "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--json"}},
		{"events missing limit", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--json"}},
		{"events zero limit", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--limit", "0", "--json"}},
		{"events too large limit", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--limit", "101", "--json"}},
		{"events signed limit", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--limit", "+1", "--json"}},
		{"events leading zero limit", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--limit", "01", "--json"}},
		{"events mixed cursor anchor", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--cursor", "opaque", "--anchor", "2", "--limit", "100", "--json"}},
		{"events anchor zero", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--anchor", "0", "--limit", "1", "--json"}},
		{"events anchor noninteger", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--anchor", "x", "--limit", "1", "--json"}},
		{"events empty cursor", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--cursor", "", "--limit", "1", "--json"}},
		{"events oversized cursor", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--cursor", tooLongCursor, "--limit", "1", "--json"}},
		{"search invalid query kind", []string{"session-search", "--project-id", "p", "--expected-generation-id", "g", "--query-kind", "symbol", "--query", "x", "--limit", "1", "--json"}},
		{"search empty query", []string{"session-search", "--project-id", "p", "--expected-generation-id", "g", "--query-kind", "file", "--query", "", "--limit", "1", "--json"}},
		{"search oversized query", []string{"session-search", "--project-id", "p", "--expected-generation-id", "g", "--query-kind", "file", "--query", tooLong, "--limit", "1", "--json"}},
		{"search mixed invalid utf8 query", []string{"session-search", "--project-id", "p", "--expected-generation-id", "g", "--query-kind", "file", "--query", string([]byte{0xff}), "--limit", "1", "--json"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseInspectContract(test.args); err == nil || contractCode(err) != "invalid_argument" {
				t.Fatalf("args=%v err=%v code=%q", test.args, err, contractCode(err))
			}
		})
	}
}

func TestParseInspectContractRejectsMixedCursorAndAnchor(t *testing.T) {
	_, err := ParseInspectContract([]string{"session-events", "--project-id", "project-p", "--provider", "codex", "--session-id", "s1", "--expected-generation-id", "g1", "--cursor", "opaque", "--anchor", "2", "--limit", "100", "--json"})
	if contractCode(err) != "invalid_argument" {
		t.Fatalf("code=%q err=%v", contractCode(err), err)
	}
}

func TestParseDecisionContractAcceptsExactAllowlist(t *testing.T) {
	tests := [][]string{
		{"candidates", "list", "--project-id", "project-p", "--json"},
		{"candidates", "list", "--json", "--status", "stale", "--project-id", "project-p"},
		{"create", "--project-id", "project-p", "--expected-review-sha256", contractTestSHA, "--json"},
		{"extract", "--project-id", "project-p", "--expected-generation-id", "generation-1", "--json"},
		{"extract", "status", "--job-id", "job-1", "--json"},
		{"extract", "cancel", "--job-id", "job-1", "--expected-revision", "1", "--json"},
		{"candidate", "transition", "--project-id", "project-p", "--candidate-id", "candidate-1", "--expected-revision", "1", "--action", "confirm", "--expected-review-sha256", contractTestSHA, "--json"},
		{"candidate", "transition", "--json", "--expected-review-sha256", contractTestSHA, "--action", "restore", "--expected-revision", "10", "--candidate-id", "candidate-1", "--project-id", "project-p"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			if _, err := ParseDecisionContract(args); err != nil {
				t.Fatalf("args=%v err=%v code=%q", args, err, contractCode(err))
			}
		})
	}
}

func TestParseDecisionContractRejectsExactInvalidArgv(t *testing.T) {
	tests := [][]string{
		{"decisions", "--json"}, {"candidates", "status", "--project-id", "p", "--json"},
		{"candidates", "list", "--project-id", "p", "--status", "active", "--json"},
		{"candidates", "list", "--project-id", "p", "--status", "pending", "--status", "stale", "--json"},
		{"candidates", "list", "--project-id", "p", "--path", "x", "--json"},
		{"candidates", "list", "--project-id", "p", "--json", "--json"},
		{"create", "--project-id", "p", "--expected-review-sha256", contractTestDigest, "--json"},
		{"create", "--project-id", "p", "--expected-review-sha256", strings.Repeat("a", 63), "--json"},
		{"create", "--project-id", "p", "--expected-review-sha256", strings.Repeat("A", 64), "--json"},
		{"create", "--project-id", "p", "--expected-review-sha256", contractTestSHA, "--input", "file", "--json"},
		{"extract", "--project-id", "p", "--expected-generation-id", "g"},
		{"extract", "--project-id", "p", "--expected-generation-id", "g", "--json", "extra"},
		{"extract", "status", "--job-id", "job", "--json", "--project-id", "p"},
		{"extract", "cancel", "--job-id", "job", "--expected-revision", "0", "--json"},
		{"extract", "cancel", "--job-id", "job", "--expected-revision", "+1", "--json"},
		{"candidate", "transition", "--project-id", "p", "--candidate-id", "c", "--expected-revision", "1", "--action", "confirm", "--expected-review-sha256", contractTestSHA},
		{"candidate", "transition", "--project-id", "p", "--candidate-id", "c", "--expected-revision", "1", "--action", "approve", "--expected-review-sha256", contractTestSHA, "--json"},
		{"candidate", "transition", "--project-id", "p", "--candidate-id", "c", "--expected-revision", "1", "--action", "confirm", "--expected-review-sha256", contractTestSHA, "--file", "x", "--json"},
		{"candidate", "transition", "--project-id", "P", "--candidate-id", "c", "--expected-revision", "1", "--action", "confirm", "--expected-review-sha256", contractTestSHA, "--json"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			if _, err := ParseDecisionContract(args); err == nil || contractCode(err) != "invalid_argument" {
				t.Fatalf("args=%v err=%v code=%q", args, err, contractCode(err))
			}
		})
	}
}

func TestParsePricingContractAcceptsAndRejectsDigests(t *testing.T) {
	valid := []string{"supplement", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--json"}
	if _, err := ParsePricingContract(valid); err != nil {
		t.Fatalf("valid err=%v code=%q", err, contractCode(err))
	}
	for _, digest := range []string{contractTestSHA, "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("a", 63), "sha512:" + contractTestSHA} {
		args := append([]string(nil), valid...)
		for i := range args {
			if args[i] == contractTestDigest {
				args[i] = digest
			}
		}
		if _, err := ParsePricingContract(args); err == nil || contractCode(err) != "invalid_argument" {
			t.Fatalf("digest=%q err=%v code=%q", digest, err, contractCode(err))
		}
	}
	for _, args := range [][]string{
		{"supplement", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--data-dir", "/tmp/x", "--json"},
		{"supplement", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA},
	} {
		if _, err := ParsePricingContract(args); err == nil || contractCode(err) != "invalid_argument" {
			t.Fatalf("args=%v err=%v code=%q", args, err, contractCode(err))
		}
	}
}

func TestParseSyncMigrationContractAcceptsOnlyExplicitModes(t *testing.T) {
	valid := [][]string{
		{"--dry-run", "--json"},
		{"--project-id", "project-p", "--data-dir", "/tmp/session-reviewer", "--dry-run", "--json"},
		{"--confirm-migration", "--expected-preview-digest", contractTestDigest, "--json"},
		{"--json", "--data-dir", "/tmp/session-reviewer", "--expected-preview-digest", contractTestDigest, "--project-id", "project-p", "--confirm-migration"},
	}
	for _, args := range valid {
		if _, err := ParseSyncMigrationContract(args); err != nil {
			t.Fatalf("valid args=%v err=%v code=%q", args, err, contractCode(err))
		}
	}
	invalid := [][]string{
		nil, {}, {"--json"}, {"--dry-run"}, {"--confirm-migration", "--json"}, {"--confirm-migration", "--expected-preview-digest", contractTestSHA, "--json"},
		{"--dry-run", "--confirm-migration", "--json"}, {"--dry-run", "--project-id", "p", "--project-id", "p", "--json"}, {"--dry-run", "--unknown", "x", "--json"}, {"--dry-run", "--path", "/tmp/x", "--json"},
		{"--dry-run", "--data-dir", "", "--json"}, {"--dry-run", "--data-dir", string([]byte{0xff}), "--json"}, {"--dry-run", "--project-id", "../p", "--json"}, {"--confirm-migration", "--expected-preview-digest", contractTestDigest, "--json", "extra"},
	}
	for _, args := range invalid {
		if _, err := ParseSyncMigrationContract(args); err == nil || contractCode(err) != "invalid_argument" {
			t.Fatalf("invalid args=%v err=%v code=%q", args, err, contractCode(err))
		}
	}
}

func TestContractConstantsAndStableErrors(t *testing.T) {
	if MaxInspectPageSize != 100 || MaxInspectQueryBytes != 256 || MaxDecisionInputBytes != 64<<10 || MaxOpaqueCursorBytes != 4096 || MaxInspectResponseBytes != 1<<20 || InspectExecutionTimeout.Seconds() != 5 {
		t.Fatalf("constants changed")
	}
	codes := map[string]string{
		"invalid argument":                ContractCodeInvalidArgument,
		"generation mismatch":             ContractCodeGenerationMismatch,
		"stale cursor":                    ContractCodeStaleCursor,
		"anchor out of range":             ContractCodeAnchorOutOfRange,
		"response too large":              ContractCodeResponseTooLarge,
		"candidate revision conflict":     ContractCodeCandidateRevisionConflict,
		"review preimage conflict":        ContractCodeReviewPreimageConflict,
		"session index capacity exceeded": ContractCodeSessionIndexCapacityExceeded,
		"migration preview stale":         ContractCodeMigrationPreviewStale,
	}
	wantCodes := map[string]string{
		"invalid argument":                "invalid_argument",
		"generation mismatch":             "generation_mismatch",
		"stale cursor":                    "stale_cursor",
		"anchor out of range":             "anchor_out_of_range",
		"response too large":              "response_too_large",
		"candidate revision conflict":     "candidate_revision_conflict",
		"review preimage conflict":        "review_preimage_conflict",
		"session index capacity exceeded": "session_index_capacity_exceeded",
		"migration preview stale":         "migration_preview_stale",
	}
	if !reflect.DeepEqual(codes, wantCodes) {
		t.Fatalf("codes=%v want=%v", codes, wantCodes)
	}
	for _, code := range codes {
		err := ContractError{Code: code, Message: "message"}
		if err.Error() != "message" {
			t.Fatalf("code=%q Error()=%q", code, err.Error())
		}
	}
}

func TestContractParsersPopulateRequestsWithoutNormalizingValues(t *testing.T) {
	inspect, err := ParseInspectContract([]string{"session-events", "--project-id", "project-p", "--provider", "claude", "--session-id", "session-1", "--expected-generation-id", "generation-1", "--anchor", "7", "--limit", "25", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	wantInspect := InspectRequest{Command: "session-events", ProjectID: "project-p", Provider: "claude", SessionID: "session-1", ExpectedGenerationID: "generation-1", Anchor: 7, Limit: 25}
	if !reflect.DeepEqual(inspect, wantInspect) {
		t.Fatalf("inspect=%+v want=%+v", inspect, wantInspect)
	}

	decision, err := ParseDecisionContract([]string{"candidate", "transition", "--project-id", "project-p", "--candidate-id", "candidate-1", "--expected-revision", "9", "--action", "not_decision", "--expected-review-sha256", contractTestSHA, "--json"})
	if err != nil {
		t.Fatal(err)
	}
	wantDecision := DecisionRequest{Command: "candidate", Subcommand: "transition", ProjectID: "project-p", CandidateID: "candidate-1", ExpectedRevision: 9, Action: "not_decision", ExpectedReviewSHA256: contractTestSHA}
	if !reflect.DeepEqual(decision, wantDecision) {
		t.Fatalf("decision=%+v want=%+v", decision, wantDecision)
	}

	pricing, err := ParsePricingContract([]string{"supplement", "--project-id", "project-p", "--provider", "codex", "--session-id", "session-1", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--json"})
	if err != nil {
		t.Fatal(err)
	}
	wantPricing := PricingRequest{Command: "supplement", ProjectID: "project-p", Provider: "codex", SessionID: "session-1", UsageRecordDigest: contractTestDigest, ExpectedLedgerSHA256: contractTestSHA}
	if !reflect.DeepEqual(pricing, wantPricing) {
		t.Fatalf("pricing=%+v want=%+v", pricing, wantPricing)
	}

	migration, err := ParseSyncMigrationContract([]string{"--confirm-migration", "--expected-preview-digest", contractTestDigest, "--project-id", "project-p", "--data-dir", "/tmp/session-reviewer", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	wantMigration := SyncMigrationRequest{Mode: "confirm-migration", ProjectID: "project-p", DataDir: "/tmp/session-reviewer", ExpectedPreviewDigest: contractTestDigest}
	if !reflect.DeepEqual(migration, wantMigration) {
		t.Fatalf("migration=%+v want=%+v", migration, wantMigration)
	}
}

func TestContractParsersRequireEveryMandatoryFlag(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		required []string
		parse    func([]string) error
	}{
		{"inspect summary", []string{"session-summary", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--json"}, []string{"--project-id", "--provider", "--session-id", "--expected-generation-id", "--json"}, inspectContractError},
		{"inspect events", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--limit", "1", "--json"}, []string{"--project-id", "--provider", "--session-id", "--expected-generation-id", "--limit", "--json"}, inspectContractError},
		{"inspect search", []string{"session-search", "--project-id", "p", "--expected-generation-id", "g", "--query-kind", "file", "--query", "main.go", "--limit", "1", "--json"}, []string{"--project-id", "--expected-generation-id", "--query-kind", "--query", "--limit", "--json"}, inspectContractError},
		{"candidate list", []string{"candidates", "list", "--project-id", "p", "--json"}, []string{"--project-id", "--json"}, decisionContractError},
		{"decision create", []string{"create", "--project-id", "p", "--expected-review-sha256", contractTestSHA, "--json"}, []string{"--project-id", "--expected-review-sha256", "--json"}, decisionContractError},
		{"decision extract", []string{"extract", "--project-id", "p", "--expected-generation-id", "g", "--json"}, []string{"--project-id", "--expected-generation-id", "--json"}, decisionContractError},
		{"extract status", []string{"extract", "status", "--job-id", "j", "--json"}, []string{"--job-id", "--json"}, decisionContractError},
		{"extract cancel", []string{"extract", "cancel", "--job-id", "j", "--expected-revision", "1", "--json"}, []string{"--job-id", "--expected-revision", "--json"}, decisionContractError},
		{"candidate transition", []string{"candidate", "transition", "--project-id", "p", "--candidate-id", "c", "--expected-revision", "1", "--action", "confirm", "--expected-review-sha256", contractTestSHA, "--json"}, []string{"--project-id", "--candidate-id", "--expected-revision", "--action", "--expected-review-sha256", "--json"}, decisionContractError},
		{"pricing supplement", []string{"supplement", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--json"}, []string{"--project-id", "--provider", "--session-id", "--usage-record-digest", "--expected-ledger-sha256", "--json"}, pricingContractError},
		{"migration dry run", []string{"--dry-run", "--json"}, []string{"--dry-run", "--json"}, migrationContractError},
		{"migration confirmation", []string{"--confirm-migration", "--expected-preview-digest", contractTestDigest, "--json"}, []string{"--confirm-migration", "--expected-preview-digest", "--json"}, migrationContractError},
	}
	for _, test := range tests {
		for _, required := range test.required {
			t.Run(test.name+" without "+required, func(t *testing.T) {
				args := removeContractFlag(t, test.args, required)
				assertInvalidContract(t, test.parse(args))
			})
		}
	}
}

func TestContractParsersRejectEveryDuplicateAndUnknownFlag(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		parse func([]string) error
	}{
		{"inspect summary", []string{"session-summary", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--json"}, inspectContractError},
		{"inspect events cursor", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--cursor", "opaque", "--limit", "1", "--json"}, inspectContractError},
		{"inspect events anchor", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--anchor", "1", "--limit", "1", "--json"}, inspectContractError},
		{"inspect search", []string{"session-search", "--project-id", "p", "--expected-generation-id", "g", "--query-kind", "file", "--query", "main.go", "--cursor", "opaque", "--limit", "1", "--json"}, inspectContractError},
		{"candidate list", []string{"candidates", "list", "--project-id", "p", "--status", "pending", "--json"}, decisionContractError},
		{"decision create", []string{"create", "--project-id", "p", "--expected-review-sha256", contractTestSHA, "--json"}, decisionContractError},
		{"decision extract", []string{"extract", "--project-id", "p", "--expected-generation-id", "g", "--json"}, decisionContractError},
		{"extract status", []string{"extract", "status", "--job-id", "j", "--json"}, decisionContractError},
		{"extract cancel", []string{"extract", "cancel", "--job-id", "j", "--expected-revision", "1", "--json"}, decisionContractError},
		{"candidate transition", []string{"candidate", "transition", "--project-id", "p", "--candidate-id", "c", "--expected-revision", "1", "--action", "confirm", "--expected-review-sha256", contractTestSHA, "--json"}, decisionContractError},
		{"pricing supplement", []string{"supplement", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--json"}, pricingContractError},
		{"migration dry run", []string{"--dry-run", "--project-id", "p", "--data-dir", "/tmp/sr", "--json"}, migrationContractError},
		{"migration confirmation", []string{"--confirm-migration", "--expected-preview-digest", contractTestDigest, "--project-id", "p", "--data-dir", "/tmp/sr", "--json"}, migrationContractError},
	}
	for _, test := range tests {
		t.Run(test.name+" unknown", func(t *testing.T) {
			assertInvalidContract(t, test.parse(append(append([]string(nil), test.args...), "--unknown", "value")))
		})
		for flagIndex, token := range test.args {
			if !strings.HasPrefix(token, "--") {
				continue
			}
			t.Run(test.name+" duplicate "+token, func(t *testing.T) {
				duplicate := []string{token}
				if token != "--json" && token != "--dry-run" && token != "--confirm-migration" {
					duplicate = append(duplicate, test.args[flagIndex+1])
				}
				args := append(append([]string(nil), test.args...), duplicate...)
				assertInvalidContract(t, test.parse(args))
			})
		}
	}
}

func TestContractParsersRejectEveryMissingFlagValue(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		parse func([]string) error
	}{
		{"inspect summary", []string{"session-summary", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--json"}, inspectContractError},
		{"inspect events cursor", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--cursor", "opaque", "--limit", "1", "--json"}, inspectContractError},
		{"inspect events anchor", []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--anchor", "1", "--limit", "1", "--json"}, inspectContractError},
		{"inspect search", []string{"session-search", "--project-id", "p", "--expected-generation-id", "g", "--query-kind", "file", "--query", "main.go", "--cursor", "opaque", "--limit", "1", "--json"}, inspectContractError},
		{"candidate list", []string{"candidates", "list", "--project-id", "p", "--status", "pending", "--json"}, decisionContractError},
		{"decision create", []string{"create", "--project-id", "p", "--expected-review-sha256", contractTestSHA, "--json"}, decisionContractError},
		{"decision extract", []string{"extract", "--project-id", "p", "--expected-generation-id", "g", "--json"}, decisionContractError},
		{"extract status", []string{"extract", "status", "--job-id", "j", "--json"}, decisionContractError},
		{"extract cancel", []string{"extract", "cancel", "--job-id", "j", "--expected-revision", "1", "--json"}, decisionContractError},
		{"candidate transition", []string{"candidate", "transition", "--project-id", "p", "--candidate-id", "c", "--expected-revision", "1", "--action", "confirm", "--expected-review-sha256", contractTestSHA, "--json"}, decisionContractError},
		{"pricing supplement", []string{"supplement", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--json"}, pricingContractError},
		{"migration dry run", []string{"--dry-run", "--project-id", "p", "--data-dir", "/tmp/sr", "--json"}, migrationContractError},
		{"migration confirmation", []string{"--confirm-migration", "--expected-preview-digest", contractTestDigest, "--project-id", "p", "--data-dir", "/tmp/sr", "--json"}, migrationContractError},
	}
	for _, test := range tests {
		for i, token := range test.args {
			if !strings.HasPrefix(token, "--") || token == "--json" || token == "--dry-run" || token == "--confirm-migration" {
				continue
			}
			t.Run(test.name+" "+token, func(t *testing.T) {
				args := append(append([]string(nil), test.args[:i+1]...), test.args[i+2:]...)
				assertInvalidContract(t, test.parse(args))
			})
		}
	}
}

func TestContractParsersRejectWrongCommandShapes(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		parse func([]string) error
	}{
		{"inspect missing command", nil, inspectContractError},
		{"inspect unknown command", []string{"summary", "--json"}, inspectContractError},
		{"decisions missing command", nil, decisionContractError},
		{"decisions unknown command", []string{"decisions", "--json"}, decisionContractError},
		{"candidates missing subcommand", []string{"candidates", "--json"}, decisionContractError},
		{"candidates unknown subcommand", []string{"candidates", "status", "--json"}, decisionContractError},
		{"candidate missing subcommand", []string{"candidate", "--json"}, decisionContractError},
		{"candidate unknown subcommand", []string{"candidate", "confirm", "--json"}, decisionContractError},
		{"extract unknown positional subcommand", []string{"extract", "start", "--json"}, decisionContractError},
		{"pricing missing command", nil, pricingContractError},
		{"pricing unknown command", []string{"estimate", "--json"}, pricingContractError},
		{"pricing extra subcommand", []string{"supplement", "status", "--json"}, pricingContractError},
		{"migration positional mode", []string{"dry-run", "--json"}, migrationContractError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { assertInvalidContract(t, test.parse(test.args)) })
	}
}

func TestContractParsersAcceptAllFrozenEnums(t *testing.T) {
	for _, kind := range []string{"branch", "file", "error"} {
		args := []string{"session-search", "--project-id", "p", "--expected-generation-id", "g", "--query-kind", kind, "--query", "x", "--limit", "1", "--json"}
		if err := inspectContractError(args); err != nil {
			t.Fatalf("query-kind=%q err=%v", kind, err)
		}
	}
	for _, status := range []string{"pending", "confirmed", "ignored", "not_decision", "stale"} {
		args := []string{"candidates", "list", "--project-id", "p", "--status", status, "--json"}
		if err := decisionContractError(args); err != nil {
			t.Fatalf("status=%q err=%v", status, err)
		}
	}
	for _, action := range []string{"confirm", "ignore", "not_decision", "restore"} {
		args := []string{"candidate", "transition", "--project-id", "p", "--candidate-id", "c", "--expected-revision", "1", "--action", action, "--expected-review-sha256", contractTestSHA, "--json"}
		if err := decisionContractError(args); err != nil {
			t.Fatalf("action=%q err=%v", action, err)
		}
	}
}

func TestContractParsersEnforceSafeIDOnEveryIDFlag(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		flags []string
		parse func([]string) error
	}{
		{"inspect summary", []string{"session-summary", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--json"}, []string{"--project-id", "--provider", "--session-id", "--expected-generation-id"}, inspectContractError},
		{"inspect search", []string{"session-search", "--project-id", "p", "--expected-generation-id", "g", "--query-kind", "file", "--query", "x", "--limit", "1", "--json"}, []string{"--project-id", "--expected-generation-id"}, inspectContractError},
		{"candidate list", []string{"candidates", "list", "--project-id", "p", "--json"}, []string{"--project-id"}, decisionContractError},
		{"decision extract", []string{"extract", "--project-id", "p", "--expected-generation-id", "g", "--json"}, []string{"--project-id", "--expected-generation-id"}, decisionContractError},
		{"extract status", []string{"extract", "status", "--job-id", "j", "--json"}, []string{"--job-id"}, decisionContractError},
		{"candidate transition", []string{"candidate", "transition", "--project-id", "p", "--candidate-id", "c", "--expected-revision", "1", "--action", "confirm", "--expected-review-sha256", contractTestSHA, "--json"}, []string{"--project-id", "--candidate-id"}, decisionContractError},
		{"pricing supplement", []string{"supplement", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--json"}, []string{"--project-id", "--provider", "--session-id"}, pricingContractError},
		{"migration", []string{"--dry-run", "--project-id", "p", "--json"}, []string{"--project-id"}, migrationContractError},
	}
	for _, test := range tests {
		for _, flag := range test.flags {
			t.Run(test.name+" "+flag, func(t *testing.T) {
				args := replaceContractFlagValue(t, test.args, flag, "../unsafe")
				assertInvalidContract(t, test.parse(args))
			})
		}
	}
	for _, value := range []string{"", "Uppercase", strings.Repeat("a", 129), string([]byte{0xff})} {
		args := []string{"session-summary", "--project-id", value, "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--json"}
		assertInvalidContract(t, inspectContractError(args))
	}
	validMaxID := strings.Repeat("a", 128)
	args := []string{"session-summary", "--project-id", validMaxID, "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--json"}
	if err := inspectContractError(args); err != nil {
		t.Fatalf("128-byte safe ID err=%v", err)
	}
}

func TestContractParsersEnforceIntegerAndUTF8ByteBounds(t *testing.T) {
	validQuery := strings.Repeat("界", 85) + "a"
	invalidQuery := strings.Repeat("界", 85) + "ab"
	for _, query := range []string{validQuery, strings.Repeat("q", MaxInspectQueryBytes)} {
		args := []string{"session-search", "--project-id", "p", "--expected-generation-id", "g", "--query-kind", "error", "--query", query, "--limit", "100", "--json"}
		if err := inspectContractError(args); err != nil {
			t.Fatalf("valid %d-byte query err=%v", len(query), err)
		}
	}
	for _, query := range []string{invalidQuery, string([]byte{0xff})} {
		args := []string{"session-search", "--project-id", "p", "--expected-generation-id", "g", "--query-kind", "error", "--query", query, "--limit", "1", "--json"}
		assertInvalidContract(t, inspectContractError(args))
	}

	validCursor := strings.Repeat("界", 1365) + "a"
	invalidCursor := strings.Repeat("界", 1365) + "ab"
	for _, cursor := range []string{validCursor, strings.Repeat("c", MaxOpaqueCursorBytes)} {
		args := []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--cursor", cursor, "--limit", "1", "--json"}
		if err := inspectContractError(args); err != nil {
			t.Fatalf("valid %d-byte cursor err=%v", len(cursor), err)
		}
	}
	for _, cursor := range []string{invalidCursor, string([]byte{0xff})} {
		args := []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--cursor", cursor, "--limit", "1", "--json"}
		assertInvalidContract(t, inspectContractError(args))
	}

	for _, value := range []string{"0", "-1", "+1", "01", "1.0", strings.Repeat("9", 100)} {
		events := []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--anchor", value, "--limit", "1", "--json"}
		assertInvalidContract(t, inspectContractError(events))
		cancel := []string{"extract", "cancel", "--job-id", "j", "--expected-revision", value, "--json"}
		assertInvalidContract(t, decisionContractError(cancel))
		transition := []string{"candidate", "transition", "--project-id", "p", "--candidate-id", "c", "--expected-revision", value, "--action", "confirm", "--expected-review-sha256", contractTestSHA, "--json"}
		assertInvalidContract(t, decisionContractError(transition))
	}
	for _, limit := range []string{"0", "101", "-1", "+1", "01", "1.0", strings.Repeat("9", 100)} {
		events := []string{"session-events", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--limit", limit, "--json"}
		assertInvalidContract(t, inspectContractError(events))
		search := []string{"session-search", "--project-id", "p", "--expected-generation-id", "g", "--query-kind", "file", "--query", "x", "--limit", limit, "--json"}
		assertInvalidContract(t, inspectContractError(search))
	}
}

func TestContractParsersEnforceEveryDigestFormat(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		flag    string
		valid   string
		invalid []string
		parse   func([]string) error
	}{
		{"decision create review sha", []string{"create", "--project-id", "p", "--expected-review-sha256", contractTestSHA, "--json"}, "--expected-review-sha256", contractTestSHA, []string{contractTestDigest, strings.Repeat("A", 64), strings.Repeat("a", 63)}, decisionContractError},
		{"candidate transition review sha", []string{"candidate", "transition", "--project-id", "p", "--candidate-id", "c", "--expected-revision", "1", "--action", "confirm", "--expected-review-sha256", contractTestSHA, "--json"}, "--expected-review-sha256", contractTestSHA, []string{contractTestDigest, strings.Repeat("A", 64), strings.Repeat("a", 65)}, decisionContractError},
		{"pricing ledger sha", []string{"supplement", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--json"}, "--expected-ledger-sha256", contractTestSHA, []string{contractTestDigest, strings.Repeat("A", 64), strings.Repeat("a", 63)}, pricingContractError},
		{"pricing usage digest", []string{"supplement", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--json"}, "--usage-record-digest", contractTestDigest, []string{contractTestSHA, "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("a", 63), "sha512:" + contractTestSHA}, pricingContractError},
		{"migration preview digest", []string{"--confirm-migration", "--expected-preview-digest", contractTestDigest, "--json"}, "--expected-preview-digest", contractTestDigest, []string{contractTestSHA, "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("a", 65), "sha512:" + contractTestSHA}, migrationContractError},
	}
	for _, test := range tests {
		if err := test.parse(replaceContractFlagValue(t, test.args, test.flag, test.valid)); err != nil {
			t.Fatalf("%s valid err=%v", test.name, err)
		}
		for _, value := range test.invalid {
			t.Run(test.name+" "+value, func(t *testing.T) {
				assertInvalidContract(t, test.parse(replaceContractFlagValue(t, test.args, test.flag, value)))
			})
		}
	}
}

func TestConversationChainContractRequiresTurnForMessageCursorAndCapsSourceReads(t *testing.T) {
	args := []string{"conversation-chain", "--project-id", "p", "--provider", "claude", "--session-id", "same", "--expected-generation-id", "g", "--turn-unit-id", "turn-1", "--message-cursor", "opaque", "--limit", "64", "--json"}
	request, err := ParseInspectContract(args)
	if err != nil {
		t.Fatal(err)
	}
	if request.Provider != "claude" || request.SessionID != "same" || request.TurnUnitID != "turn-1" || request.MessageCursor != "opaque" || request.Limit != 64 {
		t.Fatalf("unexpected conversation-chain request: %+v", request)
	}
	withoutTurn := removeContractFlag(t, args, "--turn-unit-id")
	if _, err := ParseInspectContract(withoutTurn); err == nil {
		t.Fatal("accepted message cursor without a turn unit")
	}
	tooLarge := replaceContractFlagValue(t, args, "--limit", "65")
	if _, err := ParseInspectContract(tooLarge); err == nil {
		t.Fatal("accepted conversation page above 64 items")
	}
	if MaxConversationSourceReadBytes != 64<<10 {
		t.Fatalf("source read ceiling = %d", MaxConversationSourceReadBytes)
	}
	if err := ValidateConversationSourceCoverage(ConversationSourceCoverage{SourceBytes: MaxConversationSourceReadBytes + 1, ReturnedBytes: MaxConversationSourceReadBytes, Truncated: false}); err == nil {
		t.Fatal("accepted silent source clipping without truncation coverage")
	}
	if err := ValidateConversationSourceCoverage(ConversationSourceCoverage{SourceBytes: MaxConversationSourceReadBytes + 1, ReturnedBytes: MaxConversationSourceReadBytes, Truncated: true}); err != nil {
		t.Fatalf("valid explicit truncation coverage rejected: %v", err)
	}
}

func TestEvolutionContractsFreezeListSummarizeAndTransitionGrammar(t *testing.T) {
	list, err := ParseEvolutionContract([]string{"summary-candidates", "list", "--project-id", "p", "--milestone-id", "m", "--status", "pending", "--json"})
	if err != nil || list.Command != "summary-candidates" || list.Subcommand != "list" {
		t.Fatalf("summary candidate list = %+v err=%v", list, err)
	}
	summarize, err := ParseEvolutionContract([]string{"summarize", "--project-id", "p", "--milestone-id", "m", "--expected-generation-id", "g", "--json"})
	if err != nil || summarize.Command != "summarize" || summarize.ExpectedGenerationID != "g" {
		t.Fatalf("summarize = %+v err=%v", summarize, err)
	}
	transition, err := ParseEvolutionContract([]string{"summary-candidate", "transition", "--project-id", "p", "--milestone-id", "m", "--candidate-id", "c", "--expected-candidate-revision", "2", "--expected-review-sha256", contractTestSHA, "--action", "confirm", "--json"})
	if err != nil || transition.ExpectedCandidateRevision != 2 || transition.Action != "confirm" {
		t.Fatalf("summary transition = %+v err=%v", transition, err)
	}
	for _, action := range []string{"confirm", "ignore", "restore"} {
		args := []string{"summary-candidate", "transition", "--project-id", "p", "--milestone-id", "m", "--candidate-id", "c", "--expected-candidate-revision", "1", "--expected-review-sha256", contractTestSHA, "--action", action, "--json"}
		if _, err := ParseEvolutionContract(args); err != nil {
			t.Fatalf("action %q rejected: %v", action, err)
		}
	}
}

func TestProblemContractsEnforceTargetAndCASGrammar(t *testing.T) {
	if _, err := ParseProblemContract([]string{"candidates", "list", "--project-id", "p", "--status", "pending", "--json"}); err != nil {
		t.Fatal(err)
	}
	base := []string{"candidate", "transition", "--project-id", "p", "--candidate-id", "c", "--expected-candidate-revision", "1", "--expected-problem-map-revision", "2", "--expected-review-sha256", contractTestSHA, "--action", "apply_child", "--json"}
	if _, err := ParseProblemContract(base); err == nil {
		t.Fatal("accepted apply without target problem ID")
	}
	withTarget := append(append([]string(nil), base[:len(base)-1]...), "--target-problem-id", "target", "--json")
	if request, err := ParseProblemContract(withTarget); err != nil || request.TargetProblemID != "target" {
		t.Fatalf("targeted apply = %+v err=%v", request, err)
	}
	for _, action := range []string{"keep_pending", "dismiss"} {
		args := replaceContractFlagValue(t, withTarget, "--action", action)
		if _, err := ParseProblemContract(args); err == nil {
			t.Fatalf("accepted target problem ID for %s", action)
		}
	}
	for _, action := range []string{"apply_child", "apply_sibling", "merge"} {
		args := replaceContractFlagValue(t, withTarget, "--action", action)
		if _, err := ParseProblemContract(args); err != nil {
			t.Fatalf("targeted action %q rejected: %v", action, err)
		}
	}
	for _, action := range []string{"keep_pending", "dismiss", "restore"} {
		args := replaceContractFlagValue(t, base, "--action", action)
		if _, err := ParseProblemContract(args); err != nil {
			t.Fatalf("targetless action %q rejected: %v", action, err)
		}
	}
}

func TestProblemMoveAndReorderRequireCompleteSiblingSet(t *testing.T) {
	move, err := ParseProblemContract([]string{"move", "--project-id", "p", "--problem-id", "child", "--new-parent-id", "root", "--expected-problem-map-revision", "2", "--expected-review-sha256", contractTestSHA, "--json"})
	if err != nil || move.NewParentID != "root" {
		t.Fatalf("move = %+v err=%v", move, err)
	}
	reorder, err := ParseProblemContract([]string{"reorder", "--project-id", "p", "--parent-id", "root", "--expected-problem-map-revision", "2", "--expected-review-sha256", contractTestSHA, "--json"})
	if err != nil || reorder.ParentID != "root" {
		t.Fatalf("reorder = %+v err=%v", reorder, err)
	}
	for _, ordered := range [][]string{{"a"}, {"a", "a"}, {"a", "foreign", "b"}} {
		if err := ValidateCompleteSiblingOrder([]string{"a", "b"}, ordered); err == nil {
			t.Fatalf("accepted incomplete or foreign sibling order: %#v", ordered)
		}
	}
	if err := ValidateCompleteSiblingOrder([]string{"a", "b"}, []string{"b", "a"}); err != nil {
		t.Fatalf("complete sibling order rejected: %v", err)
	}
}

func TestContractParsersRejectForbiddenInputSurfacesAndPositionals(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		parse func([]string) error
	}{
		{"inspect file", []string{"session-summary", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--expected-generation-id", "g", "--file", "input.json", "--json"}, inspectContractError},
		{"inspect input", []string{"session-search", "--project-id", "p", "--expected-generation-id", "g", "--query-kind", "file", "--query", "x", "--input", "input.json", "--limit", "1", "--json"}, inspectContractError},
		{"decision path", []string{"create", "--project-id", "p", "--expected-review-sha256", contractTestSHA, "--path", "input.json", "--json"}, decisionContractError},
		{"decision positional", []string{"extract", "--project-id", "p", "--expected-generation-id", "g", "payload.md", "--json"}, decisionContractError},
		{"pricing data dir", []string{"supplement", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--data-dir", "/tmp/sr", "--json"}, pricingContractError},
		{"pricing positional", []string{"supplement", "--project-id", "p", "--provider", "codex", "--session-id", "s", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "payload.json", "--json"}, pricingContractError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { assertInvalidContract(t, test.parse(test.args)) })
	}
}

func TestParseSyncMigrationContractHasNoFilesystemSideEffects(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "must-not-be-created")
	args := []string{"--dry-run", "--data-dir", dataDir, "--json"}
	original := append([]string(nil), args...)
	if _, err := ParseSyncMigrationContract(args); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, original) {
		t.Fatalf("args mutated: got=%v want=%v", args, original)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parser touched data-dir: %v", err)
	}
}

func inspectContractError(args []string) error {
	_, err := ParseInspectContract(args)
	return err
}

func decisionContractError(args []string) error {
	_, err := ParseDecisionContract(args)
	return err
}

func pricingContractError(args []string) error {
	_, err := ParsePricingContract(args)
	return err
}

func migrationContractError(args []string) error {
	_, err := ParseSyncMigrationContract(args)
	return err
}

func assertInvalidContract(t *testing.T, err error) {
	t.Helper()
	if err == nil || contractCode(err) != ContractCodeInvalidArgument {
		t.Fatalf("err=%v code=%q", err, contractCode(err))
	}
}

func removeContractFlag(t *testing.T, args []string, flag string) []string {
	t.Helper()
	for i, token := range args {
		if token != flag {
			continue
		}
		width := 2
		if flag == "--json" || flag == "--dry-run" || flag == "--confirm-migration" {
			width = 1
		}
		return append(append([]string(nil), args[:i]...), args[i+width:]...)
	}
	t.Fatalf("flag %q not found in %v", flag, args)
	return nil
}

func replaceContractFlagValue(t *testing.T, args []string, flag, value string) []string {
	t.Helper()
	result := append([]string(nil), args...)
	for i, token := range result {
		if token == flag {
			if i+1 >= len(result) {
				t.Fatalf("flag %q has no value in %v", flag, args)
			}
			result[i+1] = value
			return result
		}
	}
	t.Fatalf("flag %q not found in %v", flag, args)
	return nil
}
