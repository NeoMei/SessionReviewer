package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/agent"
	"github.com/neomei/SessionReviewer/internal/proposal"
)

const (
	maxPromptBytes          = 4 << 20
	maxOutputSchemaBytes    = 1 << 20
	maxRunStdoutBytes       = 8 << 20
	maxRunStderrBytes       = 256 << 10
	maxJSONDepth            = 128
	processTerminationGrace = 200 * time.Millisecond
	processExitWait         = 2 * time.Second
)

var (
	errProcessIdentityChanged = errors.New("process start identity changed")
	errProcessExitTimeout     = errors.New("process did not exit after native termination")
	errInvalidStream          = errors.New("invalid Codex JSONL stream")
	errToolEvent              = errors.New("Codex emitted a forbidden tool event")
	errJSONDepthExceeded      = errors.New("JSON nesting exceeds reviewed bound")
)

const outputSchemaName = "proposal-schema.json"

const codexTransportSchema = `{"type":"object","properties":{"proposal":{"type":"string"}},"required":["proposal"],"additionalProperties":false}`

const codexTransportInstructions = `

CODEX TRANSPORT ENVELOPE
- Return exactly one JSON object with the single required string field "proposal".
- The decoded UTF-8 contents of "proposal" must be exactly the proposal JSON object required by AGENT_DRAFT_JSON_SCHEMA_V1 above.
- Do not put Markdown fences, commentary, or any other bytes inside or outside "proposal".`

type activeRun struct {
	mu         sync.Mutex
	process    *managedProcess
	ready      chan struct{}
	done       chan struct{}
	readyOnce  sync.Once
	doneOnce   sync.Once
	stopOnce   sync.Once
	signalOnce sync.Once
	stopErr    error
	cancelled  atomic.Bool
	stopSignal chan struct{}
}

func newActiveRun() *activeRun {
	return &activeRun{ready: make(chan struct{}), done: make(chan struct{}), stopSignal: make(chan struct{})}
}

func (run *activeRun) setProcess(process *managedProcess) {
	run.mu.Lock()
	run.process = process
	run.mu.Unlock()
	run.readyOnce.Do(func() { close(run.ready) })
}

func (run *activeRun) finish() {
	run.readyOnce.Do(func() { close(run.ready) })
	run.doneOnce.Do(func() { close(run.done) })
}

func (run *activeRun) stop() error {
	run.cancelled.Store(true)
	run.signalOnce.Do(func() { close(run.stopSignal) })
	<-run.ready
	run.stopOnce.Do(func() {
		run.mu.Lock()
		process := run.process
		run.mu.Unlock()
		if process != nil {
			run.stopErr = terminateManagedProcess(process, processTerminationGrace)
		}
	})
	return run.stopErr
}

// GenerateProposal runs the pinned executable once with the fixed restricted
// read-only invocation. Prompt bytes are supplied only on stdin and every
// returned byte remains untrusted until the strict decoders accept it.
func (adapter *Adapter) GenerateProposal(ctx context.Context, request agent.Request) (result agent.Result, resultErr error) {
	if adapter == nil {
		return agent.Result{}, agent.NewError(agent.CodeUnconfigured, errors.New("nil Codex adapter"))
	}
	if err := validateRequest(request); err != nil {
		return agent.Result{}, agent.NewError(agent.CodeUnconfigured, err)
	}
	run := newActiveRun()
	adapter.mu.Lock()
	if adapter.verifying {
		adapter.mu.Unlock()
		return agent.Result{}, agent.NewError(agent.CodeBusy, errors.New("Codex adapter is verifying capabilities"))
	}
	if adapter.executable == "" || adapter.executableIdentity == nil {
		adapter.mu.Unlock()
		return agent.Result{}, agent.NewError(agent.CodeUnconfigured, errors.New("Codex executable is not verified"))
	}
	if adapter.active != nil {
		adapter.mu.Unlock()
		return agent.Result{}, agent.NewError(agent.CodeBusy, errors.New("Codex adapter is active"))
	}
	executable := adapter.executable
	executableIdentity := adapter.executableIdentity
	adapter.active = run
	adapter.mu.Unlock()
	defer func() {
		run.finish()
		adapter.mu.Lock()
		if adapter.active == run {
			adapter.active = nil
		}
		adapter.mu.Unlock()
	}()

	if err := recheckExecutable(executable, executableIdentity); err != nil {
		return agent.Result{}, agent.NewError(agent.CodeIncompatible, err)
	}
	if run.cancelled.Load() {
		return agent.Result{}, agent.NewError(agent.CodeCancelled, errors.New("Codex run cancelled before start"))
	}
	workingRoot, err := openPrivateRoot(request.WorkingDirectory)
	if err != nil {
		return agent.Result{}, agent.NewError(agent.CodeUnconfigured, err)
	}
	defer workingRoot.close()
	forbiddenRoots, err := openForbiddenRoots(request.ForbiddenRoots)
	if err != nil {
		return agent.Result{}, agent.NewError(agent.CodeUnconfigured, err)
	}
	defer forbiddenRoots.close()
	if err := forbiddenRoots.recheckDisjoint(workingRoot); err != nil {
		return agent.Result{}, agent.NewError(agent.CodeUnconfigured, err)
	}
	runDirectory, err := workingRoot.createDirectory(".session-reviewer-codex-")
	if err != nil {
		return agent.Result{}, agent.NewError(agent.CodeUnconfigured, err)
	}
	defer func() {
		if cleanupErr := runDirectory.cleanup(); cleanupErr != nil && resultErr == nil {
			result = agent.Result{}
			resultErr = agent.NewError(agent.CodeIncompatible, cleanupErr)
		}
	}()
	if err := runDirectory.writePrivateFile(outputSchemaName, []byte(codexTransportSchema)); err != nil {
		return agent.Result{}, agent.NewError(agent.CodeUnconfigured, err)
	}
	if err := recheckExecutable(executable, executableIdentity); err != nil {
		return agent.Result{}, agent.NewError(agent.CodeIncompatible, err)
	}

	args := fixedInvocation(outputSchemaName)
	stdout := newBoundedBuffer(maxRunStdoutBytes)
	stderr := newBoundedBuffer(maxRunStderrBytes)
	command := exec.Command(executable, args...)
	if err := runDirectory.configureCommandDirectory(command); err != nil {
		run.setProcess(nil)
		return agent.Result{}, agent.NewError(agent.CodeIncompatible, err)
	}
	transportPrompt, err := codexTransportPrompt(request.Prompt)
	if err != nil {
		run.setProcess(nil)
		return agent.Result{}, agent.NewError(agent.CodeUnconfigured, err)
	}
	pipes, err := attachCommandIO(command, transportPrompt, stdout, stderr)
	if err != nil {
		run.setProcess(nil)
		return agent.Result{}, agent.NewError(agent.CodeIncompatible, err)
	}
	process, err := startManagedProcess(command, func() error {
		if err := runDirectory.recheckForStart(); err != nil {
			return err
		}
		return forbiddenRoots.recheckDisjoint(workingRoot)
	})
	if err != nil {
		pipes.abort()
		run.setProcess(nil)
		return agent.Result{}, agent.NewError(agent.CodeIncompatible, err)
	}
	pipes.started()
	run.setProcess(process)
	if run.cancelled.Load() {
		_ = run.stop()
	}

	runContext, cancel := context.WithDeadline(ctx, request.Deadline)
	defer cancel()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	var waitErr error
	var contextErr error
	var stopErr error
	var exitErr error
	select {
	case waitErr = <-wait:
	case <-run.stopSignal:
		stopErr = run.stop()
		waitErr, exitErr = waitForProcessExit(wait, processExitWait)
	case <-runContext.Done():
		contextErr = runContext.Err()
		stopErr = run.stop()
		waitErr, exitErr = waitForProcessExit(wait, processExitWait)
	}
	// A successfully exited parent can still leave descendants in its process
	// group/job. Always close the native tree boundary before parsing output.
	cleanupErr := terminateManagedProcess(process, 0)
	drainErr := pipes.finish(processExitWait)
	releaseErr := releaseManagedProcess(process)
	lifecycleErr := errors.Join(stopErr, exitErr, cleanupErr, drainErr, releaseErr)

	if run.cancelled.Load() && contextErr == nil {
		return agent.Result{}, agent.NewError(agent.CodeCancelled, errors.Join(waitErr, lifecycleErr))
	}
	if errors.Is(contextErr, context.DeadlineExceeded) {
		return agent.Result{}, agent.NewError(agent.CodeTimeout, errors.Join(waitErr, lifecycleErr, contextErr))
	}
	if errors.Is(contextErr, context.Canceled) {
		return agent.Result{}, agent.NewError(agent.CodeCancelled, errors.Join(waitErr, lifecycleErr, contextErr))
	}
	if lifecycleErr != nil {
		return agent.Result{}, agent.NewError(agent.CodeIncompatible, lifecycleErr)
	}
	if stdout.Exceeded() || stderr.Exceeded() {
		return agent.Result{}, agent.NewError(agent.CodeIncompatible, errors.New("Codex output exceeded a reviewed bound"))
	}

	parsed, parseErr := parseJSONL(stdout.Bytes())
	if errors.Is(parseErr, errToolEvent) {
		return agent.Result{}, agent.NewError(agent.CodeToolForbidden, parseErr)
	}
	if parsed.failure != "" && isAuthFailure(parsed.failure) {
		return agent.Result{}, agent.NewError(agent.CodeAuth, errors.New(parsed.failure))
	}
	if parseErr != nil {
		return agent.Result{}, agent.NewError(agent.CodeIncompatible, parseErr)
	}
	if waitErr != nil {
		return agent.Result{}, agent.NewError(agent.CodeIncompatible, errors.Join(waitErr, errors.New(parsed.failure)))
	}
	if parsed.failure != "" {
		return agent.Result{}, agent.NewError(agent.CodeIncompatible, errors.New(parsed.failure))
	}
	if run.cancelled.Load() {
		return agent.Result{}, agent.NewError(agent.CodeCancelled, errors.New("Codex run cancelled before result handoff"))
	}
	return agent.Result{
		Proposal: append([]byte(nil), parsed.proposal...),
		// The reviewed Codex JSONL contract does not expose provider model provenance
		// in exec JSONL. Empty is authoritative and must not be guessed.
		Model: "",
		Usage: parsed.usage,
	}, nil
}

// Cancel requests native process-tree termination. Repeated calls are safe;
// they either observe the same active run or a completed no-op.
func (adapter *Adapter) Cancel(ctx context.Context) error {
	if adapter == nil {
		return nil
	}
	adapter.mu.Lock()
	run := adapter.active
	adapter.mu.Unlock()
	if run == nil {
		return nil
	}
	stopped := make(chan error, 1)
	go func() { stopped <- run.stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			return agent.NewError(agent.CodeIncompatible, err)
		}
	case <-ctx.Done():
		return agent.NewError(agent.CodeCancelled, ctx.Err())
	}
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
		return agent.NewError(agent.CodeCancelled, ctx.Err())
	}
}

func validateRequest(request agent.Request) error {
	if len(request.Prompt) == 0 || len(request.Prompt) > maxPromptBytes ||
		len(request.OutputSchema) == 0 || len(request.OutputSchema) > maxOutputSchemaBytes ||
		!json.Valid(request.OutputSchema) || request.Deadline.IsZero() || !request.Deadline.After(time.Now()) {
		return errors.New("invalid Codex proposal request")
	}
	if err := validateJSONNoDuplicates(request.OutputSchema); err != nil {
		return errors.New("invalid Codex output schema")
	}
	if request.WorkingDirectory == "" || !filepath.IsAbs(request.WorkingDirectory) {
		return errors.New("private Codex working directory must be absolute")
	}
	if len(request.ForbiddenRoots) != 2 {
		return errors.New("canonical Project and Vault roots are required")
	}
	seen := make(map[agent.ForbiddenRootKind]struct{}, 2)
	for _, root := range request.ForbiddenRoots {
		if root.Kind != agent.ForbiddenRootProject && root.Kind != agent.ForbiddenRootVault {
			return errors.New("invalid forbidden root kind")
		}
		if _, duplicate := seen[root.Kind]; duplicate {
			return errors.New("duplicate forbidden root kind")
		}
		seen[root.Kind] = struct{}{}
		if root.CanonicalPath == "" || !filepath.IsAbs(root.CanonicalPath) {
			return errors.New("forbidden root must be absolute")
		}
	}
	return nil
}

func fixedInvocation(schemaPath string) []string {
	args := []string{
		"exec", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--sandbox", "read-only", "--json", "--color", "never",
		"--skip-git-repo-check", "--output-schema", schemaPath,
	}
	return append(restrictionArgs(args), "-")
}

type parsedStream struct {
	proposal []byte
	usage    accounting.TokenUsage
	failure  string
}

func parseJSONL(output []byte) (parsedStream, error) {
	if len(output) == 0 || !utf8.Valid(output) || output[len(output)-1] != '\n' {
		return parsedStream{}, errInvalidStream
	}
	lines := bytes.Split(output[:len(output)-1], []byte{'\n'})
	if len(lines) < 3 {
		return parsedStream{}, errInvalidStream
	}
	state := 0
	terminal := false
	finalMessages := 0
	var result parsedStream
	for _, line := range lines {
		if len(line) == 0 || terminal {
			return parsedStream{}, errInvalidStream
		}
		if err := validateJSONNoDuplicates(line); err != nil {
			return parsedStream{}, fmt.Errorf("%w: malformed event", errInvalidStream)
		}
		var header struct {
			Type string          `json:"type"`
			Item json.RawMessage `json:"item"`
		}
		if err := json.Unmarshal(line, &header); err != nil || header.Type == "" {
			return parsedStream{}, errInvalidStream
		}
		if isToolKind(header.Type) {
			return parsedStream{}, errToolEvent
		}
		if len(header.Item) != 0 {
			var itemHeader struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(header.Item, &itemHeader); err != nil {
				return parsedStream{}, errInvalidStream
			}
			if isToolKind(itemHeader.Type) {
				return parsedStream{}, errToolEvent
			}
		}

		switch header.Type {
		case "thread.started":
			var event struct {
				Type     string `json:"type"`
				ThreadID string `json:"thread_id"`
			}
			if state != 0 || decodeStrict(line, &event) != nil || event.ThreadID == "" {
				return parsedStream{}, errInvalidStream
			}
			state = 1
		case "turn.started":
			var event struct {
				Type string `json:"type"`
			}
			if state != 1 || decodeStrict(line, &event) != nil {
				return parsedStream{}, errInvalidStream
			}
			state = 2
		case "item.started", "item.updated", "item.completed":
			if state != 2 {
				return parsedStream{}, errInvalidStream
			}
			message, completed, err := parseAllowedItem(line, header.Type)
			if err != nil {
				return parsedStream{}, err
			}
			if completed {
				finalMessages++
				result.proposal = []byte(message)
			}
		case "turn.completed":
			if state != 2 || finalMessages != 1 {
				return parsedStream{}, errInvalidStream
			}
			usage, err := parseUsageEvent(line)
			if err != nil {
				return parsedStream{}, err
			}
			result.usage = usage
			terminal = true
		case "turn.failed", "error":
			if state < 1 {
				return parsedStream{}, errInvalidStream
			}
			message, err := parseFailureEvent(line, header.Type)
			if err != nil {
				return parsedStream{}, err
			}
			result.failure = message
			terminal = true
		default:
			return parsedStream{}, errInvalidStream
		}
	}
	if !terminal {
		return parsedStream{}, errInvalidStream
	}
	if result.failure != "" {
		return result, nil
	}
	proposalBytes, err := decodeCodexTransportProposal(result.proposal)
	if err != nil {
		return parsedStream{}, fmt.Errorf("%w: invalid final proposal", errors.Join(errInvalidStream, err))
	}
	result.proposal = proposalBytes
	return result, nil
}

func codexTransportPrompt(prompt []byte) ([]byte, error) {
	if len(prompt)+len(codexTransportInstructions) > maxPromptBytes {
		return nil, errors.New("Codex transport prompt exceeds reviewed bound")
	}
	result := make([]byte, 0, len(prompt)+len(codexTransportInstructions))
	result = append(result, prompt...)
	result = append(result, codexTransportInstructions...)
	return result, nil
}

func decodeCodexTransportProposal(data []byte) ([]byte, error) {
	var envelope struct {
		Proposal string `json:"proposal"`
	}
	if err := decodeStrict(data, &envelope); err != nil || envelope.Proposal == "" {
		return nil, errInvalidStream
	}
	proposalBytes := []byte(envelope.Proposal)
	if err := validateProposal(proposalBytes); err != nil {
		return nil, err
	}
	return proposalBytes, nil
}

func parseAllowedItem(line []byte, eventType string) (string, bool, error) {
	var event struct {
		Type string `json:"type"`
		Item struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	if err := decodeStrict(line, &event); err != nil || event.Item.ID == "" || !utf8.ValidString(event.Item.Text) {
		return "", false, errInvalidStream
	}
	switch event.Item.Type {
	case "reasoning":
		return "", false, nil
	case "agent_message":
		return event.Item.Text, eventType == "item.completed", nil
	default:
		return "", false, errInvalidStream
	}
}

func parseUsageEvent(line []byte) (accounting.TokenUsage, error) {
	var event struct {
		Type  string `json:"type"`
		Usage struct {
			InputTokens           *int64 `json:"input_tokens"`
			CachedInputTokens     *int64 `json:"cached_input_tokens"`
			CacheWriteInputTokens *int64 `json:"cache_write_input_tokens"`
			OutputTokens          *int64 `json:"output_tokens"`
			ReasoningOutputTokens *int64 `json:"reasoning_output_tokens"`
		} `json:"usage"`
	}
	if err := decodeStrict(line, &event); err != nil || event.Usage.InputTokens == nil ||
		event.Usage.CachedInputTokens == nil || event.Usage.CacheWriteInputTokens == nil ||
		event.Usage.OutputTokens == nil || event.Usage.ReasoningOutputTokens == nil {
		return accounting.TokenUsage{}, errInvalidStream
	}
	usage := accounting.TokenUsage{
		InputTokens:           *event.Usage.InputTokens,
		CachedInputTokens:     *event.Usage.CachedInputTokens,
		CacheWriteInputTokens: *event.Usage.CacheWriteInputTokens,
		OutputTokens:          *event.Usage.OutputTokens,
		ReasoningOutputTokens: *event.Usage.ReasoningOutputTokens,
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	if err := accounting.ValidateTokenUsage(usage); err != nil {
		return accounting.TokenUsage{}, errInvalidStream
	}
	return usage, nil
}

func parseFailureEvent(line []byte, eventType string) (string, error) {
	var event struct {
		Type    string `json:"type"`
		Message string `json:"message,omitempty"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := decodeStrict(line, &event); err != nil {
		return "", errInvalidStream
	}
	message := event.Message
	if eventType == "turn.failed" {
		if event.Error == nil || event.Message != "" {
			return "", errInvalidStream
		}
		message = event.Error.Message
	} else if event.Error != nil {
		return "", errInvalidStream
	}
	if strings.TrimSpace(message) == "" {
		return "", errInvalidStream
	}
	return message, nil
}

func validateProposal(data []byte) error {
	if len(data) == 0 {
		return errInvalidStream
	}
	if err := validateJSONNoDuplicates(data); err != nil {
		return err
	}
	decoded, err := proposal.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	if decoded.SessionReport.Accounting != nil {
		return errors.New("Agent draft supplied host-owned accounting")
	}
	return nil
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func validateJSONNoDuplicates(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("JSON input is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := inspectJSONValue(decoder, 0); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func inspectJSONValue(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil || token == nil {
		return errors.New("invalid JSON value")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	depth++
	if depth > maxJSONDepth {
		return errJSONDepthExceeded
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("invalid JSON object name")
			}
			if _, duplicate := seen[name]; duplicate {
				return errors.New("duplicate JSON object name")
			}
			seen[name] = struct{}{}
			if err := inspectJSONValue(decoder, depth); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := inspectJSONValue(decoder, depth); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func isToolKind(value string) bool {
	normalized := normalizeKind(value)
	switch normalized {
	case "tool_call", "tool_request", "custom_tool_call", "function_call",
		"command_execution", "file_change", "mcp_tool_call", "collab_tool_call",
		"web_search", "todo_list", "computer_use", "browser_use", "image_generation",
		"workspace_dependencies", "skill_search", "remote_plugin", "shell_tool", "apps":
		return true
	}
	compact := strings.ReplaceAll(normalized, "_", "")
	switch compact {
	case "functioncall", "commandexecution", "filechange", "websearch", "todolist",
		"computeruse", "browseruse", "imagegeneration", "workspacedependencies",
		"skillsearch", "remoteplugin", "shelltool":
		return true
	}
	return strings.HasSuffix(compact, "toolcall") || strings.HasSuffix(compact, "toolrequest") ||
		strings.HasSuffix(compact, "functioncall") || strings.HasSuffix(compact, "functionrequest")
}

func normalizeKind(value string) string {
	var result strings.Builder
	underscore := false
	for _, current := range strings.TrimSpace(strings.ToLower(value)) {
		if unicode.IsLetter(current) || unicode.IsDigit(current) {
			result.WriteRune(current)
			underscore = false
		} else if !underscore && result.Len() != 0 {
			result.WriteByte('_')
			underscore = true
		}
	}
	return strings.TrimSuffix(result.String(), "_")
}

func isAuthFailure(message string) bool {
	normalized := strings.ToLower(message)
	for _, marker := range []string{"401", "403", "unauthorized", "authentication", "not logged in", "login required"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

type boundedBuffer struct {
	mu       sync.Mutex
	limit    int
	data     []byte
	exceeded bool
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit, data: make([]byte, 0, min(limit, 4096))}
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	remaining := buffer.limit - len(buffer.data)
	if remaining > 0 {
		copyBytes := len(data)
		if copyBytes > remaining {
			copyBytes = remaining
		}
		buffer.data = append(buffer.data, data[:copyBytes]...)
	}
	if len(data) > remaining {
		buffer.exceeded = true
	}
	return len(data), nil
}

func (buffer *boundedBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.data...)
}

func (buffer *boundedBuffer) Exceeded() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.exceeded
}

type managedProcess struct {
	mu         sync.Mutex
	startToken string
	platform   platformProcess
}

func startManagedProcess(command *exec.Cmd, startChecks ...func() error) (*managedProcess, error) {
	var startCheck func() error
	if len(startChecks) > 0 {
		startCheck = startChecks[0]
	}
	platform, token, err := startPlatformProcess(command, startCheck)
	if err != nil {
		return nil, err
	}
	return &managedProcess{startToken: token, platform: platform}, nil
}

func terminateManagedProcess(process *managedProcess, grace time.Duration) error {
	if process == nil {
		return nil
	}
	return terminateManagedProcessWithToken(process, process.startToken, grace)
}

func terminateManagedProcessWithToken(process *managedProcess, token string, grace time.Duration) error {
	process.mu.Lock()
	defer process.mu.Unlock()
	return terminatePlatformProcess(&process.platform, token, grace)
}

func releaseManagedProcess(process *managedProcess) error {
	if process == nil {
		return nil
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	return releasePlatformProcess(&process.platform)
}

func waitForProcessExit(wait <-chan error, timeout time.Duration) (error, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-wait:
		return err, nil
	case <-timer.C:
		return nil, errProcessExitTimeout
	}
}
