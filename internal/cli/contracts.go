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
	MaxInspectPageSize             = 100
	MaxInspectQueryBytes           = 256
	MaxDecisionInputBytes          = 64 << 10
	MaxOpaqueCursorBytes           = 4096
	MaxInspectResponseBytes        = 1 << 20
	MaxConversationSourceReadBytes = 64 << 10
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
	TurnUnitID           string
	MessageCursor        string
}

type ConversationSourceCoverage struct {
	SourceBytes   int
	ReturnedBytes int
	Truncated     bool
}

type EvolutionRequest struct {
	Command                   string
	Subcommand                string
	ProjectID                 string
	MilestoneID               string
	Status                    string
	ExpectedGenerationID      string
	CandidateID               string
	ExpectedCandidateRevision int
	ExpectedReviewSHA256      string
	Action                    string
}

type ProblemRequest struct {
	Command                    string
	Subcommand                 string
	ProjectID                  string
	Status                     string
	CandidateID                string
	ExpectedCandidateRevision  int
	ExpectedProblemMapRevision int
	ExpectedReviewSHA256       string
	Action                     string
	TargetProblemID            string
	ProblemID                  string
	NewParentID                string
	ParentID                   string
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
	case "conversation-chain":
		return parseConversationChainContract(args[1:])
	default:
		return InspectRequest{}, contractError("unknown inspect subcommand")
	}
}

func parseConversationChainContract(args []string) (InspectRequest, error) {
	allowed := map[string]bool{"project-id": true, "provider": true, "session-id": true, "expected-generation-id": true, "turn-unit-id": true, "message-cursor": true, "limit": true, "json": true}
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
	if flags.values["turn-unit-id"] != "" {
		if err = requireSafeIDs(flags, "turn-unit-id"); err != nil {
			return InspectRequest{}, err
		}
	}
	if flags.values["message-cursor"] != "" {
		if flags.values["turn-unit-id"] == "" {
			return InspectRequest{}, contractError("message cursor requires a turn unit")
		}
		if err = requireBoundedUTF8(flags.values["message-cursor"], MaxOpaqueCursorBytes, "message cursor"); err != nil {
			return InspectRequest{}, err
		}
	}
	limit, err := requirePositiveInt(flags.values["limit"])
	if err != nil || limit > 64 {
		return InspectRequest{}, contractError("limit must be between 1 and 64")
	}
	return InspectRequest{Command: "conversation-chain", ProjectID: flags.values["project-id"], Provider: flags.values["provider"], SessionID: flags.values["session-id"], ExpectedGenerationID: flags.values["expected-generation-id"], TurnUnitID: flags.values["turn-unit-id"], MessageCursor: flags.values["message-cursor"], Limit: limit}, nil
}

func ValidateConversationSourceCoverage(coverage ConversationSourceCoverage) error {
	if coverage.SourceBytes < 0 || coverage.ReturnedBytes < 0 || coverage.ReturnedBytes > MaxConversationSourceReadBytes || coverage.ReturnedBytes > coverage.SourceBytes || coverage.Truncated != (coverage.ReturnedBytes < coverage.SourceBytes) {
		return contractError("conversation source truncation coverage is inconsistent")
	}
	return nil
}

func ParseEvolutionContract(args []string) (EvolutionRequest, error) {
	if len(args) == 0 {
		return EvolutionRequest{}, contractError("evolution command is required")
	}
	switch args[0] {
	case "summary-candidates":
		if len(args) < 2 || args[1] != "list" {
			return EvolutionRequest{}, contractError("unknown summary-candidates command")
		}
		flags, err := parseContractFlags(args[2:], map[string]bool{"project-id": true, "milestone-id": true, "status": true, "json": true})
		if err != nil {
			return EvolutionRequest{}, err
		}
		if err = requireFlags(flags, "project-id", "milestone-id"); err != nil {
			return EvolutionRequest{}, err
		}
		if err = requireSafeIDs(flags, "project-id", "milestone-id"); err != nil {
			return EvolutionRequest{}, err
		}
		if status := flags.values["status"]; status != "" {
			switch status {
			case "pending", "confirmed", "ignored", "not_decision", "stale":
			default:
				return EvolutionRequest{}, contractError("status is invalid")
			}
		}
		return EvolutionRequest{Command: "summary-candidates", Subcommand: "list", ProjectID: flags.values["project-id"], MilestoneID: flags.values["milestone-id"], Status: flags.values["status"]}, nil
	case "summarize":
		flags, err := parseContractFlags(args[1:], map[string]bool{"project-id": true, "milestone-id": true, "expected-generation-id": true, "json": true})
		if err != nil {
			return EvolutionRequest{}, err
		}
		if err = requireFlags(flags, "project-id", "milestone-id", "expected-generation-id"); err != nil {
			return EvolutionRequest{}, err
		}
		if err = requireSafeIDs(flags, "project-id", "milestone-id", "expected-generation-id"); err != nil {
			return EvolutionRequest{}, err
		}
		return EvolutionRequest{Command: "summarize", ProjectID: flags.values["project-id"], MilestoneID: flags.values["milestone-id"], ExpectedGenerationID: flags.values["expected-generation-id"]}, nil
	case "summary-candidate":
		if len(args) < 2 || args[1] != "transition" {
			return EvolutionRequest{}, contractError("unknown summary-candidate command")
		}
		flags, err := parseContractFlags(args[2:], map[string]bool{"project-id": true, "milestone-id": true, "candidate-id": true, "expected-candidate-revision": true, "expected-review-sha256": true, "action": true, "json": true})
		if err != nil {
			return EvolutionRequest{}, err
		}
		if err = requireFlags(flags, "project-id", "milestone-id", "candidate-id", "expected-candidate-revision", "expected-review-sha256", "action"); err != nil {
			return EvolutionRequest{}, err
		}
		if err = requireSafeIDs(flags, "project-id", "milestone-id", "candidate-id"); err != nil {
			return EvolutionRequest{}, err
		}
		revision, err := requirePositiveInt(flags.values["expected-candidate-revision"])
		if err != nil {
			return EvolutionRequest{}, err
		}
		if err = requireBareSHA(flags.values["expected-review-sha256"]); err != nil {
			return EvolutionRequest{}, err
		}
		switch flags.values["action"] {
		case "confirm", "ignore", "restore":
		default:
			return EvolutionRequest{}, contractError("action is invalid")
		}
		return EvolutionRequest{Command: "summary-candidate", Subcommand: "transition", ProjectID: flags.values["project-id"], MilestoneID: flags.values["milestone-id"], CandidateID: flags.values["candidate-id"], ExpectedCandidateRevision: revision, ExpectedReviewSHA256: flags.values["expected-review-sha256"], Action: flags.values["action"]}, nil
	default:
		return EvolutionRequest{}, contractError("unknown evolution command")
	}
}

func ParseProblemContract(args []string) (ProblemRequest, error) {
	if len(args) == 0 {
		return ProblemRequest{}, contractError("problems command is required")
	}
	switch args[0] {
	case "candidates":
		if len(args) < 2 || args[1] != "list" {
			return ProblemRequest{}, contractError("unknown problems candidates command")
		}
		flags, err := parseContractFlags(args[2:], map[string]bool{"project-id": true, "status": true, "json": true})
		if err != nil {
			return ProblemRequest{}, err
		}
		if err = requireFlags(flags, "project-id"); err != nil {
			return ProblemRequest{}, err
		}
		if err = requireSafeIDs(flags, "project-id"); err != nil {
			return ProblemRequest{}, err
		}
		if status := flags.values["status"]; status != "" {
			switch status {
			case "pending", "applied", "merged", "kept_pending", "stale", "dismissed":
			default:
				return ProblemRequest{}, contractError("status is invalid")
			}
		}
		return ProblemRequest{Command: "candidates", Subcommand: "list", ProjectID: flags.values["project-id"], Status: flags.values["status"]}, nil
	case "candidate":
		return parseProblemTransition(args[1:])
	case "move":
		return parseProblemMove(args[1:])
	case "reorder":
		return parseProblemReorder(args[1:])
	default:
		return ProblemRequest{}, contractError("unknown problems command")
	}
}

func parseProblemTransition(args []string) (ProblemRequest, error) {
	if len(args) == 0 || args[0] != "transition" {
		return ProblemRequest{}, contractError("unknown problem candidate command")
	}
	flags, err := parseContractFlags(args[1:], map[string]bool{"project-id": true, "candidate-id": true, "expected-candidate-revision": true, "expected-problem-map-revision": true, "expected-review-sha256": true, "action": true, "target-problem-id": true, "json": true})
	if err != nil {
		return ProblemRequest{}, err
	}
	if err = requireFlags(flags, "project-id", "candidate-id", "expected-candidate-revision", "expected-problem-map-revision", "expected-review-sha256", "action"); err != nil {
		return ProblemRequest{}, err
	}
	if err = requireSafeIDs(flags, "project-id", "candidate-id"); err != nil {
		return ProblemRequest{}, err
	}
	if flags.values["target-problem-id"] != "" {
		if err = requireSafeIDs(flags, "target-problem-id"); err != nil {
			return ProblemRequest{}, err
		}
	}
	candidateRevision, err := requirePositiveInt(flags.values["expected-candidate-revision"])
	if err != nil {
		return ProblemRequest{}, err
	}
	mapRevision, err := requirePositiveInt(flags.values["expected-problem-map-revision"])
	if err != nil {
		return ProblemRequest{}, err
	}
	if err = requireBareSHA(flags.values["expected-review-sha256"]); err != nil {
		return ProblemRequest{}, err
	}
	target := flags.values["target-problem-id"]
	switch flags.values["action"] {
	case "apply_child", "apply_sibling", "merge":
		if target == "" {
			return ProblemRequest{}, contractError("target problem ID is required for apply or merge")
		}
	case "keep_pending", "dismiss", "restore":
		if target != "" {
			return ProblemRequest{}, contractError("target problem ID is forbidden for this action")
		}
	default:
		return ProblemRequest{}, contractError("action is invalid")
	}
	return ProblemRequest{Command: "candidate", Subcommand: "transition", ProjectID: flags.values["project-id"], CandidateID: flags.values["candidate-id"], ExpectedCandidateRevision: candidateRevision, ExpectedProblemMapRevision: mapRevision, ExpectedReviewSHA256: flags.values["expected-review-sha256"], Action: flags.values["action"], TargetProblemID: target}, nil
}

func parseProblemMove(args []string) (ProblemRequest, error) {
	flags, err := parseContractFlags(args, map[string]bool{"project-id": true, "problem-id": true, "new-parent-id": true, "expected-problem-map-revision": true, "expected-review-sha256": true, "json": true})
	if err != nil {
		return ProblemRequest{}, err
	}
	if err = requireFlags(flags, "project-id", "problem-id", "new-parent-id", "expected-problem-map-revision", "expected-review-sha256"); err != nil {
		return ProblemRequest{}, err
	}
	if err = requireSafeIDs(flags, "project-id", "problem-id", "new-parent-id"); err != nil {
		return ProblemRequest{}, err
	}
	revision, err := requirePositiveInt(flags.values["expected-problem-map-revision"])
	if err != nil {
		return ProblemRequest{}, err
	}
	if err = requireBareSHA(flags.values["expected-review-sha256"]); err != nil {
		return ProblemRequest{}, err
	}
	return ProblemRequest{Command: "move", ProjectID: flags.values["project-id"], ProblemID: flags.values["problem-id"], NewParentID: flags.values["new-parent-id"], ExpectedProblemMapRevision: revision, ExpectedReviewSHA256: flags.values["expected-review-sha256"]}, nil
}

func parseProblemReorder(args []string) (ProblemRequest, error) {
	flags, err := parseContractFlags(args, map[string]bool{"project-id": true, "parent-id": true, "expected-problem-map-revision": true, "expected-review-sha256": true, "json": true})
	if err != nil {
		return ProblemRequest{}, err
	}
	if err = requireFlags(flags, "project-id", "parent-id", "expected-problem-map-revision", "expected-review-sha256"); err != nil {
		return ProblemRequest{}, err
	}
	if err = requireSafeIDs(flags, "project-id", "parent-id"); err != nil {
		return ProblemRequest{}, err
	}
	revision, err := requirePositiveInt(flags.values["expected-problem-map-revision"])
	if err != nil {
		return ProblemRequest{}, err
	}
	if err = requireBareSHA(flags.values["expected-review-sha256"]); err != nil {
		return ProblemRequest{}, err
	}
	return ProblemRequest{Command: "reorder", ProjectID: flags.values["project-id"], ParentID: flags.values["parent-id"], ExpectedProblemMapRevision: revision, ExpectedReviewSHA256: flags.values["expected-review-sha256"]}, nil
}

func ValidateCompleteSiblingOrder(current, ordered []string) error {
	if len(current) != len(ordered) {
		return contractError("sibling order must include every direct child exactly once")
	}
	expected := map[string]bool{}
	for _, id := range current {
		if !safeContractID(id) || expected[id] {
			return contractError("current sibling set is invalid")
		}
		expected[id] = true
	}
	seen := map[string]bool{}
	for _, id := range ordered {
		if !safeContractID(id) || !expected[id] || seen[id] {
			return contractError("sibling order contains a missing, duplicate, or foreign child")
		}
		seen[id] = true
	}
	return nil
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
