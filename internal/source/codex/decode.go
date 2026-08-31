package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/session"
	"github.com/neomei/SessionReviewer/internal/source"
)

const (
	adapterID       = "codex-jsonl"
	maxExcerptRunes = 512
	maxExcerptBytes = 1024
	maxMarkerRunes  = 64
	maxMarkerBytes  = 128
	maxDiagnostics  = 4096
)

var (
	toolCallIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	exitCodePattern   = regexp.MustCompile(`^exit code: (-?[0-9]+)$`)
	branchPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,255}$`)
	gitObjectPattern  = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	tagPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+\-]{0,255}$`)
	gitStatusPattern  = regexp.MustCompile(`^[MADRCUT?!]{1,2}[ \t]+[^\r\n]+$`)
	redactionMarker   = regexp.MustCompile(`\[REDACTED:[A-Z0-9_]+\]`)
	patchTarget       = regexp.MustCompile(`^\*\*\* (?:Add|Update|Delete|Move to) File: (.+)$`)
)

type pendingCall struct {
	id                    string
	kind                  string
	workdir               string
	commandSignature      string
	verificationComponent string
	verificationOperation string
	gitOperation          string
	targets               []string
}

type recordDecoder struct {
	ctx          context.Context
	adapter      *adapter
	frozen       frozenSource
	currentCWD   string
	accounting   *accounting.Accumulator
	pending      map[string]pendingCall
	seenCalls    map[string]struct{}
	invalidCalls map[string]struct{}
	observations []memory.ObservationRevision
	projectIDs   map[string]struct{}
	report       source.DecodeReport
}

func (a *adapter) Decode(ctx context.Context, boundary source.Boundary, visit func(memory.ObservationRevision) error) (source.DecodeReport, error) {
	report := source.DecodeReport{TerminalState: boundary.TerminalState}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if visit == nil {
		return report, errors.New("observation visitor is required")
	}
	if boundary.TerminalState != memory.Indexed {
		return report, nil
	}
	a.mu.RLock()
	frozen, found := a.frozen[boundary.Handle]
	a.mu.RUnlock()
	if !found || !sameBoundary(boundary, frozen.boundary) {
		return report, errors.New("boundary was not frozen by this Codex adapter")
	}
	startedAt, err := time.Parse(time.RFC3339Nano, boundary.Candidate.StartedAt)
	if err != nil {
		return report, errors.New("frozen Codex boundary has an invalid start time")
	}
	files, err := a.openFrozenPrefix(ctx, frozen)
	if err != nil {
		return report, err
	}
	defer closeFiles(files)

	decoder := &recordDecoder{
		ctx: ctx, adapter: a, frozen: frozen, currentCWD: boundary.Candidate.InitialCWD,
		accounting: accounting.NewAccumulator(startedAt), pending: make(map[string]pendingCall),
		seenCalls: make(map[string]struct{}), invalidCalls: make(map[string]struct{}),
		projectIDs: make(map[string]struct{}),
		report:     source.DecodeReport{TerminalState: memory.Indexed},
	}
	segmentBytes := make([]int64, len(frozen.segments))
	for index, segment := range frozen.segments {
		segmentBytes[index] = segment.size
	}
	summary, err := session.StreamFiles(files, session.DecodeOptions{
		MaxRecordBytes: maxReadRecordBytes,
		SegmentBytes:   segmentBytes,
	}, decoder.add)
	if err != nil {
		return decoder.report, err
	}
	decoder.report.MalformedLines = summary.MalformedLines
	for range summary.MalformedLines {
		decoder.diagnostic("malformed_jsonl")
	}
	if err := a.verifyExactFrozen(ctx, files, frozen); err != nil {
		return decoder.report, err
	}
	usage := decoder.accounting.Snapshot()
	if err := accounting.ValidateSessionUsage(usage); err != nil {
		return decoder.report, fmt.Errorf("validate decoded Codex accounting: %w", err)
	}
	decoder.report.ProjectIDs = sortedSet(decoder.projectIDs)
	record := memory.SourceRecord{
		SchemaVersion: memory.MemorySchemaVersion, Provider: providerCodex,
		SessionID: boundary.Candidate.SessionID, SourceIdentity: boundary.SourceIdentity,
		StartedAt: usage.StartedAt, EndedAt: usage.EndedAt, FrozenBoundary: boundary.Frozen,
		Availability: memory.SourceAvailable, Usage: *usage, ProjectIDs: append([]string(nil), decoder.report.ProjectIDs...),
	}
	digest, err := a.catalog.UpsertSource(record)
	if err != nil {
		return decoder.report, fmt.Errorf("update Codex source catalog: %w", err)
	}
	decoder.report.CatalogRecordDigest = digest
	for _, observation := range decoder.observations {
		if err := visit(observation); err != nil {
			return decoder.report, err
		}
		decoder.report.EmittedRevisions++
	}
	return decoder.report, nil
}

func (d *recordDecoder) add(record session.Record) error {
	if err := d.ctx.Err(); err != nil {
		return err
	}
	if !validRecordMetadata(record) {
		d.diagnostic("malformed_observation")
		return nil
	}
	if err := d.accounting.Observe(record); err != nil {
		d.diagnostic("invalid_accounting")
	}
	switch record.Type {
	case "session_meta":
		return d.addSessionMeta(record)
	case "turn_context":
		return d.addTurnContext(record)
	case "response_item":
		return d.addResponseItem(record)
	case "event_msg":
		return d.addEvent(record)
	default:
		d.unsupported()
		return nil
	}
}

func (d *recordDecoder) addSessionMeta(record session.Record) error {
	var payload struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	}
	if json.Unmarshal(record.Payload, &payload) != nil || payload.ID != d.frozen.boundary.Candidate.SessionID || payload.CWD == "" {
		d.malformedPayload()
		return nil
	}
	d.currentCWD = payload.CWD
	return d.observe(record, observedFact{
		kind: "artifact", subject: payload.ID, operation: "session_started", object: payload.CWD, affinityPath: payload.CWD,
	})
}

func (d *recordDecoder) addTurnContext(record session.Record) error {
	var payload struct {
		CWD string `json:"cwd"`
	}
	if json.Unmarshal(record.Payload, &payload) != nil {
		d.malformedPayload()
		return nil
	}
	if payload.CWD == "" || payload.CWD == d.currentCWD {
		return nil
	}
	d.currentCWD = payload.CWD
	return d.observe(record, observedFact{
		kind: "artifact", subject: subjectID(payload.CWD), operation: "cwd_changed", object: payload.CWD, affinityPath: payload.CWD,
	})
}

func (d *recordDecoder) addEvent(record session.Record) error {
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(record.Payload, &header) != nil {
		d.malformedPayload()
		return nil
	}
	if header.Type == "token_count" {
		return nil
	}
	d.unsupported()
	return nil
}

func (d *recordDecoder) addResponseItem(record session.Record) error {
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(record.Payload, &header) != nil {
		d.malformedPayload()
		return nil
	}
	switch header.Type {
	case "message":
		return d.addMessage(record)
	case "custom_tool_call":
		return d.addToolCall(record)
	case "custom_tool_call_output":
		return d.addToolOutput(record)
	default:
		d.unsupported()
		return nil
	}
}

func (d *recordDecoder) addMessage(record session.Record) error {
	var payload struct {
		ID      string `json:"id"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(record.Payload, &payload) != nil || payload.Role != "user" || payload.ID == "" {
		d.unsupported()
		return nil
	}
	var parts []string
	for _, block := range payload.Content {
		if block.Type == "input_text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	if len(parts) == 0 {
		d.unsupported()
		return nil
	}
	redacted := d.adapter.redactor.Text(strings.Join(parts, "\n"))
	excerpt := boundedExcerpt(redacted.Text)
	return d.observe(record, observedFact{
		kind: "request", subject: subjectID(payload.ID), operation: "user_request", excerpt: excerpt, affinityPath: d.currentCWD,
	})
}

func (d *recordDecoder) addToolCall(record session.Record) error {
	var payload struct {
		ID     string `json:"id"`
		CallID string `json:"call_id"`
		Name   string `json:"name"`
		Input  string `json:"input"`
	}
	if json.Unmarshal(record.Payload, &payload) != nil {
		d.malformedPayload()
		return nil
	}
	callID := payload.CallID
	if callID == "" {
		callID = payload.ID
	}
	if !toolCallIDPattern.MatchString(callID) {
		d.unsupported()
		return nil
	}
	if _, duplicate := d.seenCalls[callID]; duplicate {
		delete(d.pending, callID)
		d.invalidCalls[callID] = struct{}{}
		d.invalidateToolResults(callID)
		d.diagnostic("duplicate_tool_call_id")
		return nil
	}
	d.seenCalls[callID] = struct{}{}
	switch payload.Name {
	case "exec_command":
		var input struct {
			Cmd     string `json:"cmd"`
			Workdir string `json:"workdir"`
		}
		if json.Unmarshal([]byte(payload.Input), &input) != nil || strings.TrimSpace(input.Cmd) == "" {
			d.malformedPayload()
			return nil
		}
		workdir := rootedPath(input.Workdir, d.currentCWD)
		if workdir == "" {
			d.unsupported()
			return nil
		}
		command := classifyCommand(input.Cmd)
		pending := pendingCall{
			id: callID, kind: "exec", workdir: workdir, commandSignature: command.signature,
			verificationComponent: command.verification, verificationOperation: command.verificationOperation,
			gitOperation: command.gitOperation,
		}
		d.pending[callID] = pending
		return d.observe(record, observedFact{
			kind: "command", subject: callID, operation: "command_started", object: workdir, affinityPath: workdir,
			fields: map[string]string{"command_signature": command.signature, "path": workdir, "tool_id": callID},
		})
	case "apply_patch":
		var input struct {
			Patch   string `json:"patch"`
			Workdir string `json:"workdir"`
		}
		if json.Unmarshal([]byte(payload.Input), &input) != nil {
			d.malformedPayload()
			return nil
		}
		workdir := rootedPath(input.Workdir, d.currentCWD)
		targets := parsePatchTargets(input.Patch, workdir)
		if workdir == "" || len(targets) == 0 {
			d.unsupported()
			return nil
		}
		d.pending[callID] = pendingCall{id: callID, kind: "patch", workdir: workdir, targets: targets}
		return nil
	default:
		d.unsupported()
		return nil
	}
}

func (d *recordDecoder) addToolOutput(record session.Record) error {
	var payload struct {
		CallID string          `json:"call_id"`
		Output json.RawMessage `json:"output"`
	}
	if json.Unmarshal(record.Payload, &payload) != nil || !toolCallIDPattern.MatchString(payload.CallID) {
		d.malformedPayload()
		return nil
	}
	if _, invalid := d.invalidCalls[payload.CallID]; invalid {
		d.diagnostic("ambiguous_tool_call_output")
		return nil
	}
	pending, found := d.pending[payload.CallID]
	if !found {
		d.unsupported()
		return nil
	}
	delete(d.pending, payload.CallID)
	output, err := decodeToolOutput(payload.Output)
	if err != nil {
		d.malformedPayload()
		return nil
	}
	if pending.kind == "patch" {
		outcome, valid := parsePatchOutcome(output)
		if !valid {
			d.unsupported()
			return nil
		}
		for _, target := range pending.targets {
			failed := "false"
			if outcome == "failure" {
				failed = "true"
			}
			if err := d.observe(record, observedFact{
				kind: "file", subject: subjectID(target), operation: "file_change", object: target, outcome: outcome,
				affinityPath: target, fileTarget: true,
				fields: map[string]string{"path": target, "failed": failed, "tool_id": pending.id},
			}); err != nil {
				return err
			}
		}
		return nil
	}
	exitCode, valid := parseExitCode(output)
	if !valid {
		d.unsupported()
		return nil
	}
	outcome := "success"
	if exitCode != 0 {
		outcome = "failure"
	}
	fields := map[string]string{
		"command_signature": pending.commandSignature, "exit_code": strconv.Itoa(exitCode), "tool_id": pending.id,
	}
	if err := d.observe(record, observedFact{
		kind: "command", subject: pending.id, operation: "command_finished", object: pending.workdir,
		outcome: outcome, affinityPath: pending.workdir, fields: fields,
	}); err != nil {
		return err
	}
	if pending.verificationComponent != "" {
		verificationOutcome := "passed"
		verificationFields := map[string]string{
			"component": pending.verificationComponent, "status": pending.verificationOperation,
			"exit_code": strconv.Itoa(exitCode), "passed": "true", "failed": "false", "tool_id": pending.id,
		}
		if exitCode != 0 {
			verificationOutcome = "failed"
			verificationFields["passed"], verificationFields["failed"] = "false", "true"
		}
		if err := d.observe(record, observedFact{
			kind: "verification", subject: pending.id, operation: "verification", object: pending.verificationComponent,
			outcome: verificationOutcome, affinityPath: pending.workdir, fields: verificationFields,
		}); err != nil {
			return err
		}
	}
	if pending.gitOperation != "" && exitCode == 0 {
		gitFields, valid := parseGitOutput(pending.gitOperation, output)
		if valid {
			gitFields["tool_id"] = pending.id
			if err := d.observe(record, observedFact{
				kind: "git_status", subject: pending.id, operation: "git_observation", object: pending.gitOperation,
				outcome: "observed", affinityPath: pending.workdir, fields: gitFields,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

type observedFact struct {
	kind         string
	subject      string
	operation    string
	object       string
	outcome      string
	fields       map[string]string
	excerpt      string
	affinityPath string
	fileTarget   bool
}

func (d *recordDecoder) observe(record session.Record, fact observedFact) error {
	validated, err := d.buildObservation(record, fact, "quarantine")
	if err != nil {
		d.diagnostic("malformed_observation")
		return nil
	}
	projectIDs, reason := d.adapter.classifyAffinity(fact.affinityPath, fact.fileTarget)
	if reason != "" || len(projectIDs) != 1 {
		for _, projectID := range projectIDs {
			d.projectIDs[projectID] = struct{}{}
		}
		d.report.Quarantined = append(d.report.Quarantined, source.QuarantinedRevision{
			Ref: validated.Ref, Timestamp: validated.Timestamp, Kind: validated.Key.Kind, Subject: validated.Key.Subject,
			CandidateProjectIDs: append([]string(nil), projectIDs...), ReasonCode: reason,
		})
		return nil
	}
	projectID := projectIDs[0]
	d.projectIDs[projectID] = struct{}{}
	validated.Key.ProjectID = projectID
	validated.RevisionID = memory.ObservationRevisionID(validated)
	if err := memory.ValidateObservationRevision(validated); err != nil {
		d.diagnostic("malformed_observation")
		return nil
	}
	d.observations = append(d.observations, validated)
	stableKeyDigest, err := memory.Digest(validated.Key)
	if err != nil {
		return fmt.Errorf("digest decoded observation key at line %d: %w", record.Line, err)
	}
	for _, predecessorVersion := range d.adapter.supersedes {
		d.report.Supersessions = append(d.report.Supersessions, source.RevisionSupersession{
			Key: validated.Key, StableKeyDigest: stableKeyDigest,
			SuccessorRevisionID: validated.RevisionID,
			SupersededAdapter:   predecessorVersion, SuccessorAdapter: d.adapter.version,
		})
	}
	return nil
}

func (d *recordDecoder) buildObservation(record session.Record, fact observedFact, projectID string) (memory.ObservationRevision, error) {
	ref := memory.SourceRef{
		Provider: providerCodex, SessionID: d.frozen.boundary.Candidate.SessionID,
		SourceIdentity: d.frozen.boundary.SourceIdentity,
		Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{
			Line: record.Line, ByteOffset: record.ByteOffset,
		}},
		SourceHash: record.SourceHash,
	}
	observation := memory.ObservationRevision{
		SchemaVersion: memory.MemorySchemaVersion,
		Key: memory.ObservationKey{
			Provider: providerCodex, SessionID: d.frozen.boundary.Candidate.SessionID,
			SourceIdentity: d.frozen.boundary.SourceIdentity, Sequence: record.Line,
			ProjectID: projectID, Kind: fact.kind, Subject: fact.subject,
		},
		Ref: ref, Timestamp: record.Timestamp, Operation: fact.operation, Object: fact.object,
		Outcome: fact.outcome, Fields: fact.fields, Excerpt: fact.excerpt,
		AdapterID: adapterID, AdapterVersion: d.adapter.version,
	}
	observation.RevisionID = memory.ObservationRevisionID(observation)
	if err := memory.ValidateObservationRevision(observation); err != nil {
		return memory.ObservationRevision{}, fmt.Errorf("validate decoded observation at line %d: %w", record.Line, err)
	}
	return observation, nil
}

func validRecordMetadata(record session.Record) bool {
	if record.Line < 1 || record.ByteOffset < 0 || !lowercaseSHA256.MatchString(record.SourceHash) || !validStructured(record.Type, 64) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, record.Timestamp)
	return err == nil
}

func (d *recordDecoder) invalidateToolResults(callID string) {
	removed := make(map[string]struct{})
	kept := d.observations[:0]
	for _, observation := range d.observations {
		if observation.Key.Subject == callID && observation.Operation != "command_started" {
			removed[observation.RevisionID] = struct{}{}
			continue
		}
		kept = append(kept, observation)
	}
	d.observations = kept
	lineage := d.report.Supersessions[:0]
	for _, item := range d.report.Supersessions {
		if _, discard := removed[item.SuccessorRevisionID]; !discard {
			lineage = append(lineage, item)
		}
	}
	d.report.Supersessions = lineage
	quarantined := d.report.Quarantined[:0]
	for _, item := range d.report.Quarantined {
		if item.Subject != callID {
			quarantined = append(quarantined, item)
		}
	}
	d.report.Quarantined = quarantined
}

func (a *adapter) classifyAffinity(path string, fileTarget bool) ([]string, string) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, "foreign_project_root"
	}
	target := path
	if fileTarget {
		target = filepath.Dir(path)
	}
	directory, _, err := pathguard.OpenDeepest(target)
	if err != nil {
		return nil, "foreign_project_root"
	}
	defer directory.Close()
	var matches []string
	uncertain := make(map[string]struct{})
	for _, binding := range a.bindings {
		root, err := pathguard.Open(binding.CanonicalRoot)
		if err != nil {
			if lexicallyContains(binding.CanonicalRoot, target) {
				uncertain[binding.ProjectID] = struct{}{}
			}
			continue
		}
		identity, identityErr := root.PhysicalIdentity()
		authenticated := identityErr == nil && identity == binding.RootIdentity
		matched := authenticated && directory.ContainsIdentity(root.Info())
		_ = root.Close()
		if !authenticated && lexicallyContains(binding.CanonicalRoot, target) {
			uncertain[binding.ProjectID] = struct{}{}
		}
		if matched {
			matches = append(matches, binding.ProjectID)
		}
	}
	sort.Strings(matches)
	if len(uncertain) > 0 {
		candidates := make(map[string]struct{}, len(matches)+len(uncertain))
		for _, projectID := range matches {
			candidates[projectID] = struct{}{}
		}
		for projectID := range uncertain {
			candidates[projectID] = struct{}{}
		}
		return sortedSet(candidates), "ambiguous_project_root"
	}
	if len(matches) == 1 {
		return matches, ""
	}
	if len(matches) > 1 {
		return matches, "ambiguous_project_root"
	}
	return nil, "foreign_project_root"
}

func lexicallyContains(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

type commandClass struct {
	signature             string
	verification          string
	verificationOperation string
	gitOperation          string
}

func classifyCommand(command string) commandClass {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return commandClass{signature: "unknown:none"}
	}
	executable := strings.ToLower(filepath.Base(fields[0]))
	argumentClass := "other"
	if len(fields) > 1 {
		argumentClass = normalizedToken(fields[1])
	}
	result := commandClass{signature: executable + ":" + argumentClass}
	switch {
	case executable == "go" && argumentClass == "test":
		result.verification = commandComponent(fields[2:])
		result.verificationOperation = "test"
	case executable == "go" && argumentClass == "build":
		result.verification = commandComponent(fields[2:])
		result.verificationOperation = "build"
	case executable == "go" && argumentClass == "vet":
		result.verification = commandComponent(fields[2:])
		result.verificationOperation = "lint"
	case executable == "npm" && argumentClass == "test":
		result.verification = "npm:test"
		result.verificationOperation = "test"
	case executable == "npm" && argumentClass == "run" && len(fields) > 2 && (fields[2] == "test" || fields[2] == "build" || fields[2] == "lint"):
		result.verification = "npm:" + fields[2]
		result.verificationOperation = fields[2]
	case executable == "git" && argumentClass == "status" && containsAll(fields[2:], "--porcelain=v1", "--branch"):
		result.gitOperation = "status"
		result.signature = "git:status"
	case executable == "git" && argumentClass == "rev-parse" && len(fields) == 3 && fields[2] == "HEAD":
		result.gitOperation = "head"
		result.signature = "git:head"
	case executable == "git" && argumentClass == "branch" && len(fields) == 3 && fields[2] == "--show-current":
		result.gitOperation = "branch"
		result.signature = "git:branch"
	case executable == "git" && argumentClass == "describe" && containsAll(fields[2:], "--tags", "--exact-match"):
		result.gitOperation = "tag"
		result.signature = "git:tag"
	}
	return result
}

func commandComponent(arguments []string) string {
	for _, argument := range arguments {
		if !strings.HasPrefix(argument, "-") {
			return "package"
		}
	}
	return "."
}

func normalizedToken(value string) string {
	value = strings.ToLower(value)
	if !adapterIdentityPattern.MatchString(value) {
		return "other"
	}
	return value
}

func containsAll(values []string, wanted ...string) bool {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	for _, value := range wanted {
		if _, found := set[value]; !found {
			return false
		}
	}
	return true
}

func decodeToolOutput(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return "", errors.New("unsupported tool output envelope")
	}
	var parts []string
	for _, block := range blocks {
		if (block.Type == "text" || block.Type == "input_text" || block.Type == "output_text") && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

func parseExitCode(output string) (int, bool) {
	for _, line := range normalizedLines(output) {
		match := exitCodePattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		value, err := strconv.Atoi(match[1])
		return value, err == nil
	}
	return 0, false
}

func parsePatchOutcome(output string) (string, bool) {
	switch strings.TrimSpace(output) {
	case "Done!":
		return "success", true
	case "Failed!":
		return "failure", true
	default:
		if exitCode, valid := parseExitCode(output); valid {
			if exitCode == 0 {
				return "success", true
			}
			return "failure", true
		}
		return "", false
	}
}

func parseGitOutput(operation, output string) (map[string]string, bool) {
	lines := normalizedLines(output)
	filtered := lines[:0]
	for _, line := range lines {
		if exitCodePattern.MatchString(line) || line == "" {
			continue
		}
		filtered = append(filtered, line)
	}
	switch operation {
	case "status":
		if len(filtered) == 0 || !strings.HasPrefix(filtered[0], "## ") {
			return nil, false
		}
		branch := strings.TrimPrefix(filtered[0], "## ")
		if index := strings.Index(branch, "..."); index >= 0 {
			branch = branch[:index]
		} else if index := strings.IndexByte(branch, ' '); index >= 0 {
			branch = branch[:index]
		}
		if !validBranch(branch) {
			return nil, false
		}
		for _, entry := range filtered[1:] {
			if !gitStatusPattern.MatchString(entry) {
				return nil, false
			}
		}
		status := "clean"
		if len(filtered) > 1 {
			status = "dirty"
		}
		return map[string]string{"branch": branch, "status": status}, true
	case "head":
		if len(filtered) == 1 && gitObjectPattern.MatchString(filtered[0]) {
			return map[string]string{"git_head": filtered[0]}, true
		}
	case "branch":
		if len(filtered) == 1 && validBranch(filtered[0]) {
			return map[string]string{"branch": filtered[0]}, true
		}
	case "tag":
		if len(filtered) == 1 && tagPattern.MatchString(filtered[0]) {
			return map[string]string{"tag": filtered[0]}, true
		}
	}
	return nil, false
}

func validBranch(value string) bool {
	return branchPattern.MatchString(value) && !strings.Contains(value, "..") && !strings.HasSuffix(value, ".lock")
}

func normalizedLines(value string) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = strings.TrimSpace(lines[index])
	}
	return lines
}

func parsePatchTargets(patch, workdir string) []string {
	seen := make(map[string]struct{})
	for _, line := range strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n") {
		match := patchTarget.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		target := rootedPath(match[1], workdir)
		if target == "" {
			continue
		}
		seen[target] = struct{}{}
	}
	return sortedSet(seen)
}

func rootedPath(value, base string) string {
	if value == "" {
		value = base
	}
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return ""
	}
	if !filepath.IsAbs(value) {
		if base == "" || !filepath.IsAbs(base) {
			return ""
		}
		value = filepath.Join(base, value)
	}
	value = filepath.Clean(value)
	if !filepath.IsAbs(value) {
		return ""
	}
	return value
}

func boundedExcerpt(value string) string {
	if value == "" {
		return ""
	}
	if utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxExcerptRunes && len(value) <= maxExcerptBytes {
		return value
	}
	marker := ""
	if found := redactionMarker.FindString(value); found != "" {
		if utf8.RuneCountInString(found) <= maxMarkerRunes && len(found) <= maxMarkerBytes {
			marker = " " + found
		}
	}
	suffix := "…" + marker
	suffixRunes := utf8.RuneCountInString(suffix)
	suffixBytes := len(suffix)
	end := 0
	copiedRunes := 0
	for end < len(value) {
		_, runeBytes := utf8.DecodeRuneInString(value[end:])
		if end+runeBytes >= len(value) {
			break
		}
		if copiedRunes+1+suffixRunes > maxExcerptRunes || end+runeBytes+suffixBytes > maxExcerptBytes {
			break
		}
		end += runeBytes
		copiedRunes++
	}
	return value[:end] + suffix
}

func subjectID(value string) string {
	if validStructured(value, 256) {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return "subject-" + hex.EncodeToString(sum[:])
}

func validStructured(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len([]byte(value)) <= maximum &&
		!strings.ContainsAny(value, "\x00\r\n") && strings.TrimSpace(value) == value
}

func (d *recordDecoder) unsupported() {
	d.report.UnsupportedRecords++
	d.diagnostic("unsupported_record")
}

func (d *recordDecoder) malformedPayload() {
	d.diagnostic("malformed_payload")
}

func (d *recordDecoder) diagnostic(code string) {
	if len(d.report.Diagnostics) < maxDiagnostics {
		d.report.Diagnostics = append(d.report.Diagnostics, memory.Diagnostic{Code: code})
	}
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
