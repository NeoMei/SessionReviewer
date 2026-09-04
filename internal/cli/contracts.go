package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxInspectPageSize      = 100
	MaxInspectQueryBytes    = 256
	MaxDecisionInputBytes   = 64 << 10
	MaxOpaqueCursorBytes    = 4096
	MaxInspectResponseBytes = 1 << 20
)

const InspectExecutionTimeout = 5 * time.Second

const (
	ContractCodeInvalidArgument              = "invalid_argument"
	ContractCodeGenerationMismatch           = "generation_mismatch"
	ContractCodeStaleCursor                  = "stale_cursor"
	ContractCodeAnchorOutOfRange             = "anchor_out_of_range"
	ContractCodeResponseTooLarge             = "response_too_large"
	ContractCodeCandidateRevisionConflict    = "candidate_revision_conflict"
	ContractCodeReviewPreimageConflict       = "review_preimage_conflict"
	ContractCodeSessionIndexCapacityExceeded = "session_index_capacity_exceeded"
	ContractCodeMigrationPreviewStale        = "migration_preview_stale"
)

// ContractError is returned when an invocation does not match a frozen CLI
// contract. Code is stable for callers; Message is intended for diagnostics.
type ContractError struct {
	Code    string
	Message string
}

func (e ContractError) Error() string { return e.Message }

func contractError(parts ...string) error {
	code, message := ContractCodeInvalidArgument, "invalid argument"
	switch len(parts) {
	case 1:
		message = parts[0]
	case 2:
		code, message = parts[0], parts[1]
	default:
		panic("contractError requires a message or code and message")
	}
	return ContractError{Code: code, Message: message}
}

type InspectRequest struct {
	Command              string
	ProjectID            string
	Provider             string
	SessionID            string
	ExpectedGenerationID string
	Cursor               string
	Anchor               int
	Limit                int
	QueryKind            string
	Query                string
}

type DecisionRequest struct {
	Command              string
	Subcommand           string
	ProjectID            string
	Status               string
	ExpectedReviewSHA256 string
	ExpectedGenerationID string
	JobID                string
	ExpectedRevision     int
	Action               string
	CandidateID          string
}

type PricingRequest struct {
	Command              string
	ProjectID            string
	Provider             string
	SessionID            string
	UsageRecordDigest    string
	ExpectedLedgerSHA256 string
}

type SyncMigrationRequest struct {
	Mode                  string
	ProjectID             string
	DataDir               string
	ExpectedPreviewDigest string
}

type contractFlags struct {
	values map[string]string
	json   bool
}

func parseContractFlags(args []string, allowed map[string]bool) (contractFlags, error) {
	parsed := contractFlags{values: make(map[string]string)}
	seen := make(map[string]bool)
	for i := 0; i < len(args); i++ {
		if !utf8.ValidString(args[i]) {
			return contractFlags{}, contractError("arguments must contain valid UTF-8")
		}
		token := args[i]
		if !strings.HasPrefix(token, "--") || token == "--" || strings.Contains(token, "=") {
			return contractFlags{}, contractError("unexpected positional argument")
		}
		name := strings.TrimPrefix(token, "--")
		if name == "" || !allowed[name] || seen[name] {
			return contractFlags{}, contractError("unknown or duplicate flag")
		}
		seen[name] = true
		if name == "json" || name == "dry-run" || name == "confirm-migration" {
			if name == "json" {
				parsed.json = true
			} else {
				parsed.values[name] = "true"
			}
			continue
		}
		if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") {
			return contractFlags{}, contractError("flag value is required")
		}
		i++
		if !utf8.ValidString(args[i]) {
			return contractFlags{}, contractError("arguments must contain valid UTF-8")
		}
		parsed.values[name] = args[i]
	}
	if !parsed.json {
		return contractFlags{}, contractError("--json is required")
	}
	return parsed, nil
}

func requireFlags(flags contractFlags, names ...string) error {
	for _, name := range names {
		if flags.values[name] == "" {
			return contractError("required flag is missing")
		}
	}
	return nil
}

func safeContractID(value string) bool {
	return utf8.ValidString(value) && safeReviewID(value)
}

func requireSafeIDs(flags contractFlags, names ...string) error {
	for _, name := range names {
		if !safeContractID(flags.values[name]) {
			return contractError("ID is empty or invalid")
		}
	}
	return nil
}

var bareSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func requireBareSHA(value string) error {
	if !bareSHA256Pattern.MatchString(value) {
		return contractError("digest must be 64 lowercase hexadecimal characters")
	}
	return nil
}

func requireDigest(value string) error {
	if !digestPattern.MatchString(value) {
		return contractError("digest must use sha256:<64 lowercase hex>")
	}
	return nil
}

func parsePositiveInt(value string) (int, bool) {
	if value == "" || (len(value) > 1 && value[0] == '0') || value[0] < '1' || value[0] > '9' {
		return 0, false
	}
	for _, c := range value[1:] {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(value, 10, 0)
	return int(n), err == nil && n > 0
}

func requirePositiveInt(value string) (int, error) {
	n, ok := parsePositiveInt(value)
	if !ok {
		return 0, contractError("integer must be a positive decimal number")
	}
	return n, nil
}

func requirePageLimit(value string) (int, error) {
	n, err := requirePositiveInt(value)
	if err != nil || n > MaxInspectPageSize {
		return 0, contractError("limit must be between 1 and 100")
	}
	return n, nil
}

func requireBoundedUTF8(value string, max int, label string) error {
	if !utf8.ValidString(value) || len([]byte(value)) > max {
		return contractError(fmt.Sprintf("%s exceeds its UTF-8 byte limit", label))
	}
	return nil
}

func ParseInspectContract(args []string) (InspectRequest, error) {
	if len(args) == 0 {
		return InspectRequest{}, contractError("inspect subcommand is required")
	}
	switch args[0] {
	case "session-summary":
		return parseSessionSummaryContract(args[1:])
	case "session-events":
		return parseSessionEventsContract(args[1:])
	case "session-search":
		return parseSessionSearchContract(args[1:])
	default:
		return InspectRequest{}, contractError("unknown inspect subcommand")
	}
}

func parseSessionSummaryContract(args []string) (InspectRequest, error) {
	allowed := map[string]bool{"project-id": true, "provider": true, "session-id": true, "expected-generation-id": true, "json": true}
	flags, err := parseContractFlags(args, allowed)
	if err != nil {
		return InspectRequest{}, err
	}
	if err = requireFlags(flags, "project-id", "provider", "session-id", "expected-generation-id"); err != nil {
		return InspectRequest{}, err
	}
	if err = requireSafeIDs(flags, "project-id", "provider", "session-id", "expected-generation-id"); err != nil {
		return InspectRequest{}, err
	}
	return InspectRequest{Command: "session-summary", ProjectID: flags.values["project-id"], Provider: flags.values["provider"], SessionID: flags.values["session-id"], ExpectedGenerationID: flags.values["expected-generation-id"]}, nil
}

func parseSessionEventsContract(args []string) (InspectRequest, error) {
	allowed := map[string]bool{"project-id": true, "provider": true, "session-id": true, "expected-generation-id": true, "cursor": true, "anchor": true, "limit": true, "json": true}
	flags, err := parseContractFlags(args, allowed)
	if err != nil {
		return InspectRequest{}, err
	}
	if err = requireFlags(flags, "project-id", "provider", "session-id", "expected-generation-id", "limit"); err != nil {
		return InspectRequest{}, err
	}
	if err = requireSafeIDs(flags, "project-id", "provider", "session-id", "expected-generation-id"); err != nil {
		return InspectRequest{}, err
	}
	limit, err := requirePageLimit(flags.values["limit"])
	if err != nil {
		return InspectRequest{}, err
	}
	cursor, anchor := flags.values["cursor"], flags.values["anchor"]
	if cursor != "" && anchor != "" {
		return InspectRequest{}, contractError("cursor and anchor are mutually exclusive")
	}
	if cursor != "" {
		if err = requireBoundedUTF8(cursor, MaxOpaqueCursorBytes, "cursor"); err != nil {
			return InspectRequest{}, err
		}
	}
	anchorValue := 0
	if anchor != "" {
		anchorValue, err = requirePositiveInt(anchor)
		if err != nil {
			return InspectRequest{}, err
		}
	}
	return InspectRequest{Command: "session-events", ProjectID: flags.values["project-id"], Provider: flags.values["provider"], SessionID: flags.values["session-id"], ExpectedGenerationID: flags.values["expected-generation-id"], Cursor: cursor, Anchor: anchorValue, Limit: limit}, nil
}

func parseSessionSearchContract(args []string) (InspectRequest, error) {
	allowed := map[string]bool{"project-id": true, "expected-generation-id": true, "query-kind": true, "query": true, "cursor": true, "limit": true, "json": true}
	flags, err := parseContractFlags(args, allowed)
	if err != nil {
		return InspectRequest{}, err
	}
	if err = requireFlags(flags, "project-id", "expected-generation-id", "query-kind", "query", "limit"); err != nil {
		return InspectRequest{}, err
	}
	if err = requireSafeIDs(flags, "project-id", "expected-generation-id"); err != nil {
		return InspectRequest{}, err
	}
	if kind := flags.values["query-kind"]; kind != "branch" && kind != "file" && kind != "error" {
		return InspectRequest{}, contractError("query-kind is invalid")
	}
	if err = requireBoundedUTF8(flags.values["query"], MaxInspectQueryBytes, "query"); err != nil {
		return InspectRequest{}, err
	}
	limit, err := requirePageLimit(flags.values["limit"])
	if err != nil {
		return InspectRequest{}, err
	}
	if cursor := flags.values["cursor"]; cursor != "" {
		if err = requireBoundedUTF8(cursor, MaxOpaqueCursorBytes, "cursor"); err != nil {
			return InspectRequest{}, err
		}
	}
	return InspectRequest{Command: "session-search", ProjectID: flags.values["project-id"], ExpectedGenerationID: flags.values["expected-generation-id"], QueryKind: flags.values["query-kind"], Query: flags.values["query"], Cursor: flags.values["cursor"], Limit: limit}, nil
}

func ParseDecisionContract(args []string) (DecisionRequest, error) {
	if len(args) == 0 {
		return DecisionRequest{}, contractError("decisions command is required")
	}
	if args[0] == "candidates" {
		if len(args) < 2 || args[1] != "list" {
			return DecisionRequest{}, contractError("unknown decisions candidates command")
		}
		return parseCandidateListContract(args[2:])
	}
	switch args[0] {
	case "create":
		return parseDecisionCreateContract(args[1:])
	case "extract":
		return parseDecisionExtractContract(args[1:])
	case "candidate":
		if len(args) < 2 || args[1] != "transition" {
			return DecisionRequest{}, contractError("unknown candidate command")
		}
		return parseCandidateTransitionContract(args[2:])
	default:
		return DecisionRequest{}, contractError("unknown decisions command")
	}
}

func parseCandidateListContract(args []string) (DecisionRequest, error) {
	flags, err := parseContractFlags(args, map[string]bool{"project-id": true, "status": true, "json": true})
	if err != nil {
		return DecisionRequest{}, err
	}
	if err = requireFlags(flags, "project-id"); err != nil {
		return DecisionRequest{}, err
	}
	if err = requireSafeIDs(flags, "project-id"); err != nil {
		return DecisionRequest{}, err
	}
	if status := flags.values["status"]; status != "" {
		switch status {
		case "pending", "confirmed", "ignored", "not_decision", "stale":
		default:
			return DecisionRequest{}, contractError("status is invalid")
		}
	}
	return DecisionRequest{Command: "candidates", Subcommand: "list", ProjectID: flags.values["project-id"], Status: flags.values["status"]}, nil
}

func parseDecisionCreateContract(args []string) (DecisionRequest, error) {
	flags, err := parseContractFlags(args, map[string]bool{"project-id": true, "expected-review-sha256": true, "json": true})
	if err != nil {
		return DecisionRequest{}, err
	}
	if err = requireFlags(flags, "project-id", "expected-review-sha256"); err != nil {
		return DecisionRequest{}, err
	}
	if err = requireSafeIDs(flags, "project-id"); err != nil {
		return DecisionRequest{}, err
	}
	if err = requireBareSHA(flags.values["expected-review-sha256"]); err != nil {
		return DecisionRequest{}, err
	}
	return DecisionRequest{Command: "create", ProjectID: flags.values["project-id"], ExpectedReviewSHA256: flags.values["expected-review-sha256"]}, nil
}

func parseDecisionExtractContract(args []string) (DecisionRequest, error) {
	if len(args) > 0 && args[0] == "status" {
		return parseExtractStatusContract(args[1:])
	}
	if len(args) > 0 && args[0] == "cancel" {
		return parseExtractCancelContract(args[1:])
	}
	flags, err := parseContractFlags(args, map[string]bool{"project-id": true, "expected-generation-id": true, "json": true})
	if err != nil {
		return DecisionRequest{}, err
	}
	if err = requireFlags(flags, "project-id", "expected-generation-id"); err != nil {
		return DecisionRequest{}, err
	}
	if err = requireSafeIDs(flags, "project-id", "expected-generation-id"); err != nil {
		return DecisionRequest{}, err
	}
	return DecisionRequest{Command: "extract", ProjectID: flags.values["project-id"], ExpectedGenerationID: flags.values["expected-generation-id"]}, nil
}

func parseExtractStatusContract(args []string) (DecisionRequest, error) {
	flags, err := parseContractFlags(args, map[string]bool{"job-id": true, "json": true})
	if err != nil {
		return DecisionRequest{}, err
	}
	if err = requireFlags(flags, "job-id"); err != nil {
		return DecisionRequest{}, err
	}
	if err = requireSafeIDs(flags, "job-id"); err != nil {
		return DecisionRequest{}, err
	}
	return DecisionRequest{Command: "extract", Subcommand: "status", JobID: flags.values["job-id"]}, nil
}

func parseExtractCancelContract(args []string) (DecisionRequest, error) {
	flags, err := parseContractFlags(args, map[string]bool{"job-id": true, "expected-revision": true, "json": true})
	if err != nil {
		return DecisionRequest{}, err
	}
	if err = requireFlags(flags, "job-id", "expected-revision"); err != nil {
		return DecisionRequest{}, err
	}
	if err = requireSafeIDs(flags, "job-id"); err != nil {
		return DecisionRequest{}, err
	}
	revision, err := requirePositiveInt(flags.values["expected-revision"])
	if err != nil {
		return DecisionRequest{}, err
	}
	return DecisionRequest{Command: "extract", Subcommand: "cancel", JobID: flags.values["job-id"], ExpectedRevision: revision}, nil
}

func parseCandidateTransitionContract(args []string) (DecisionRequest, error) {
	flags, err := parseContractFlags(args, map[string]bool{"project-id": true, "candidate-id": true, "expected-revision": true, "action": true, "expected-review-sha256": true, "json": true})
	if err != nil {
		return DecisionRequest{}, err
	}
	if err = requireFlags(flags, "project-id", "candidate-id", "expected-revision", "action", "expected-review-sha256"); err != nil {
		return DecisionRequest{}, err
	}
	if err = requireSafeIDs(flags, "project-id", "candidate-id"); err != nil {
		return DecisionRequest{}, err
	}
	revision, err := requirePositiveInt(flags.values["expected-revision"])
	if err != nil {
		return DecisionRequest{}, err
	}
	switch flags.values["action"] {
	case "confirm", "ignore", "not_decision", "restore":
	default:
		return DecisionRequest{}, contractError("action is invalid")
	}
	if err = requireBareSHA(flags.values["expected-review-sha256"]); err != nil {
		return DecisionRequest{}, err
	}
	return DecisionRequest{Command: "candidate", Subcommand: "transition", ProjectID: flags.values["project-id"], CandidateID: flags.values["candidate-id"], ExpectedRevision: revision, Action: flags.values["action"], ExpectedReviewSHA256: flags.values["expected-review-sha256"]}, nil
}

func ParsePricingContract(args []string) (PricingRequest, error) {
	if len(args) == 0 || args[0] != "supplement" {
		return PricingRequest{}, contractError("pricing supplement is required")
	}
	flags, err := parseContractFlags(args[1:], map[string]bool{"project-id": true, "provider": true, "session-id": true, "usage-record-digest": true, "expected-ledger-sha256": true, "json": true})
	if err != nil {
		return PricingRequest{}, err
	}
	if err = requireFlags(flags, "project-id", "provider", "session-id", "usage-record-digest", "expected-ledger-sha256"); err != nil {
		return PricingRequest{}, err
	}
	if err = requireSafeIDs(flags, "project-id", "provider", "session-id"); err != nil {
		return PricingRequest{}, err
	}
	if err = requireDigest(flags.values["usage-record-digest"]); err != nil {
		return PricingRequest{}, err
	}
	if err = requireBareSHA(flags.values["expected-ledger-sha256"]); err != nil {
		return PricingRequest{}, err
	}
	return PricingRequest{Command: "supplement", ProjectID: flags.values["project-id"], Provider: flags.values["provider"], SessionID: flags.values["session-id"], UsageRecordDigest: flags.values["usage-record-digest"], ExpectedLedgerSHA256: flags.values["expected-ledger-sha256"]}, nil
}

func ParseSyncMigrationContract(args []string) (SyncMigrationRequest, error) {
	flags, err := parseContractFlags(args, map[string]bool{"dry-run": true, "confirm-migration": true, "expected-preview-digest": true, "project-id": true, "data-dir": true, "json": true})
	if err != nil {
		return SyncMigrationRequest{}, err
	}
	dryRun, confirm := flags.values["dry-run"] == "true", flags.values["confirm-migration"] == "true"
	// Boolean flags use an internal sentinel set only when the flag is present.
	if !dryRun && !confirm {
		return SyncMigrationRequest{}, contractError("explicit migration mode is required")
	}
	if dryRun && confirm {
		return SyncMigrationRequest{}, contractError("migration modes are mutually exclusive")
	}
	if flags.values["project-id"] != "" {
		if err = requireSafeIDs(flags, "project-id"); err != nil {
			return SyncMigrationRequest{}, err
		}
	}
	if flags.values["data-dir"] != "" && !utf8.ValidString(flags.values["data-dir"]) {
		return SyncMigrationRequest{}, contractError("data-dir must be valid UTF-8")
	}
	if confirm {
		if err = requireFlags(flags, "expected-preview-digest"); err != nil {
			return SyncMigrationRequest{}, err
		}
		if err = requireDigest(flags.values["expected-preview-digest"]); err != nil {
			return SyncMigrationRequest{}, err
		}
	} else if flags.values["expected-preview-digest"] != "" {
		return SyncMigrationRequest{}, contractError("expected preview digest requires confirmation")
	}
	mode := "dry-run"
	if confirm {
		mode = "confirm-migration"
	}
	return SyncMigrationRequest{Mode: mode, ProjectID: flags.values["project-id"], DataDir: flags.values["data-dir"], ExpectedPreviewDigest: flags.values["expected-preview-digest"]}, nil
}
