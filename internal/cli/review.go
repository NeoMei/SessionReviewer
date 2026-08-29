package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/agent"
	applyengine "github.com/neomei/SessionReviewer/internal/apply"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/prepare"
	"github.com/neomei/SessionReviewer/internal/reviewjob"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

const reviewHelp = `Control durable proposal-only Agent review jobs.

Usage:
  session-reviewer review agent verify --executable ABSOLUTE_PATH --json
  session-reviewer review start --project-id ID --agent-executable ABSOLUTE_PATH --json
  session-reviewer review status --project-id ID --json
  session-reviewer review cancel --job-id ID --json
  session-reviewer review retry --job-id ID --agent-executable ABSOLUTE_PATH --expected-attempt N --expected-revision N --json
`

const maxReviewPublicJSONBytes = 4096

type reviewVerifyResponse struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Compatible    bool   `json:"compatible"`
	Version       string `json:"version,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
}

type exactReviewFlags struct {
	values map[string]string
	json   bool
}

type reviewVerifiedAgent struct {
	Handle *reviewjob.AgentHandle
	Agent  reviewjob.VerifiedAgent
}

type reviewLaunchRequest struct {
	Binary string
	JobID  string
	Token  string
}

type detachedReviewInheritance struct {
	noInheritHandles  bool
	additionalHandles []uintptr
}

func detachedReviewInheritancePolicy(handshake uintptr) (detachedReviewInheritance, error) {
	if handshake == 0 {
		return detachedReviewInheritance{}, errors.New("review handshake handle is unavailable")
	}
	return detachedReviewInheritance{additionalHandles: []uintptr{handshake}}, nil
}

var (
	reviewNow               = func() time.Time { return time.Now().UTC() }
	reviewFreeze            = reviewjob.FreezePending
	reviewLaunch            = launchDetachedReviewWorker
	reviewCreate            = func(store reviewjob.Store, job reviewjob.Job) (int, error) { return store.Create(job) }
	reviewAuthority         = newReviewLaunchAuthority
	reviewCurrentExecutable = currentReviewExecutable
	reviewVerify            = func(ctx context.Context, executable string) (reviewVerifiedAgent, error) {
		handle, err := reviewjob.VerifyAgent(ctx, "codex", executable)
		if err != nil {
			return reviewVerifiedAgent{}, err
		}
		verified, err := handle.VerifiedAgent()
		if err != nil {
			return reviewVerifiedAgent{}, err
		}
		return reviewVerifiedAgent{Handle: handle, Agent: verified}, nil
	}
)

func resetReviewCLISeams() {
	reviewNow = func() time.Time { return time.Now().UTC() }
	reviewFreeze = reviewjob.FreezePending
	reviewLaunch = launchDetachedReviewWorker
	reviewCreate = func(store reviewjob.Store, job reviewjob.Job) (int, error) { return store.Create(job) }
	reviewAuthority = newReviewLaunchAuthority
	reviewCurrentExecutable = currentReviewExecutable
	reviewVerify = func(ctx context.Context, executable string) (reviewVerifiedAgent, error) {
		handle, err := reviewjob.VerifyAgent(ctx, "codex", executable)
		if err != nil {
			return reviewVerifiedAgent{}, err
		}
		verified, err := handle.VerifiedAgent()
		if err != nil {
			return reviewVerifiedAgent{}, err
		}
		return reviewVerifiedAgent{Handle: handle, Agent: verified}, nil
	}
}

func runReview(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && isHelpToken(args[0]) {
		fmt.Fprint(stdout, reviewHelp)
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "review requires a command")
		return 2
	}
	if args[0] == "worker" {
		return runPrivateReviewWorker(args[1:])
	}
	if args[0] == "agent" {
		if len(args) < 2 || args[1] != "verify" {
			fmt.Fprintln(stderr, "review agent requires verify")
			return 2
		}
		flags, ok := parseReviewFlags(args[2:], []string{"executable"})
		if !ok || !flags.json || !filepath.IsAbs(flags.values["executable"]) {
			fmt.Fprintln(stderr, "review agent verify requires one absolute --executable and --json")
			return 2
		}
		return runReviewVerify(flags.values["executable"], stdout)
	}

	command := args[0]
	var required []string
	switch command {
	case "start":
		required = []string{"project-id", "agent-executable"}
	case "status":
		required = []string{"project-id"}
	case "cancel":
		required = []string{"job-id"}
	case "retry":
		required = []string{"job-id", "agent-executable", "expected-attempt", "expected-revision"}
	default:
		fmt.Fprintln(stderr, "unknown review command")
		return 2
	}
	flags, ok := parseReviewFlags(args[1:], required)
	if !ok || !flags.json {
		fmt.Fprintln(stderr, "review command has invalid or incomplete flags")
		return 2
	}
	if projectID := flags.values["project-id"]; projectID != "" && !safeReviewID(projectID) {
		fmt.Fprintln(stderr, "review project ID is invalid")
		return 2
	}
	if jobID := flags.values["job-id"]; jobID != "" && !safeReviewID(jobID) {
		fmt.Fprintln(stderr, "review job ID is invalid")
		return 2
	}
	if executable := flags.values["agent-executable"]; executable != "" && !filepath.IsAbs(executable) {
		fmt.Fprintln(stderr, "review Agent executable must be absolute")
		return 2
	}
	if command == "retry" {
		for _, name := range []string{"expected-attempt", "expected-revision"} {
			if _, ok := parseReviewPositiveInteger(flags.values[name]); !ok {
				fmt.Fprintln(stderr, "review retry expectations must be positive integers")
				return 2
			}
		}
	}
	return runReviewJobCommand(command, flags.values, stdout)
}

func parseReviewPositiveInteger(value string) (int, bool) {
	if value == "" || value[0] < '1' || value[0] > '9' {
		return 0, false
	}
	for _, character := range value[1:] {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 53)
	if err != nil || parsed > 1<<53-1 {
		return 0, false
	}
	return int(parsed), true
}

func parseReviewFlags(args []string, required []string) (exactReviewFlags, bool) {
	allowed := make(map[string]bool, len(required)+1)
	for _, name := range required {
		allowed[name] = true
	}
	allowed["json"] = true
	parsed := exactReviewFlags{values: make(map[string]string)}
	seen := make(map[string]bool)
	for index := 0; index < len(args); index++ {
		token := args[index]
		if !strings.HasPrefix(token, "--") || strings.Contains(token, "=") || token == "--" {
			return exactReviewFlags{}, false
		}
		name := strings.TrimPrefix(token, "--")
		if !allowed[name] || seen[name] {
			return exactReviewFlags{}, false
		}
		seen[name] = true
		if name == "json" {
			parsed.json = true
			continue
		}
		index++
		if index >= len(args) || args[index] == "" || strings.HasPrefix(args[index], "--") {
			return exactReviewFlags{}, false
		}
		parsed.values[name] = args[index]
	}
	for _, name := range required {
		if parsed.values[name] == "" {
			return exactReviewFlags{}, false
		}
	}
	return parsed, true
}

func safeReviewID(value string) bool {
	if len(value) < 1 || len(value) > 128 || !((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= '0' && value[0] <= '9')) {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func runReviewVerify(executable string, stdout io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	verified, err := reviewVerify(ctx, executable)
	response := reviewVerifyResponse{SchemaVersion: reviewjob.PublicStatusSchemaVersion, Kind: "codex"}
	if err == nil {
		response.Compatible = true
		response.Version = verified.Agent.Version
	}
	code := 0
	if err != nil {
		code = 1
		response.ErrorCode = string(agent.CodeUnconfigured)
		if safe, ok := agent.CodeOf(err); ok {
			response.ErrorCode = string(safe)
		}
	}
	if !writeReviewJSON(stdout, response) {
		return 1
	}
	return code
}

func runReviewJobCommand(command string, values map[string]string, stdout io.Writer) int {
	dataDir, err := resolveReviewDataDir()
	if err != nil {
		return writeReviewOperational(stdout, values["project-id"], reviewjob.ApplyRecovery)
	}
	switch command {
	case "start":
		return runReviewStart(dataDir, values["project-id"], values["agent-executable"], stdout)
	case "status":
		return runReviewStatus(dataDir, values["project-id"], stdout)
	case "cancel":
		return runReviewCancel(dataDir, values["job-id"], stdout)
	case "retry":
		attempt, _ := parseReviewPositiveInteger(values["expected-attempt"])
		revision, _ := parseReviewPositiveInteger(values["expected-revision"])
		return runReviewRetry(dataDir, values["job-id"], values["agent-executable"], attempt, revision, stdout)
	default:
		return writeReviewOperational(stdout, values["project-id"], reviewjob.ApplyRecovery)
	}
}

type authenticatedReviewMapping struct {
	projectID       string
	projectRoot     string
	vaultRoot       string
	projectIdentity pathguard.IdentityToken
}

func resolveReviewDataDir() (string, error) {
	dataDir, err := platform.DataDir(currentEnv())
	if err != nil {
		return "", err
	}
	return filepath.Abs(dataDir)
}

func authenticateReviewMapping(dataDir, projectID string) (_ authenticatedReviewMapping, retErr error) {
	data, err := pathguard.Open(dataDir)
	if err != nil {
		return authenticatedReviewMapping{}, err
	}
	defer func() { retErr = errors.Join(retErr, data.Close()) }()
	cfg, err := config.LoadRoot(data.Root, "config.toml")
	if err != nil {
		return authenticatedReviewMapping{}, err
	}
	mapping, found := cfg.ProjectByID(projectID)
	if !found {
		return authenticatedReviewMapping{}, errors.New("configured project was not found")
	}
	project, err := pathguard.Open(mapping.Root)
	if err != nil {
		return authenticatedReviewMapping{}, err
	}
	defer func() { retErr = errors.Join(retErr, project.Close()) }()
	vault, err := pathguard.Open(mapping.VaultRoot)
	if err != nil {
		return authenticatedReviewMapping{}, err
	}
	defer func() { retErr = errors.Join(retErr, vault.Close()) }()
	projectIdentity, err := project.PhysicalIdentity()
	if err != nil {
		return authenticatedReviewMapping{}, err
	}
	return authenticatedReviewMapping{
		projectID: projectID, projectRoot: project.Path, vaultRoot: vault.Path, projectIdentity: projectIdentity,
	}, nil
}

func authenticateStoredReviewJob(dataDir string, job reviewjob.Job) (authenticatedReviewMapping, error) {
	mapping, err := authenticateStoredReviewProject(dataDir, job)
	if err != nil {
		return authenticatedReviewMapping{}, err
	}
	file, err := os.Open(job.Agent.Executable)
	if err != nil {
		return authenticatedReviewMapping{}, errors.New("stored Agent identity is unavailable")
	}
	identity, identityErr := pathguard.PhysicalFileIdentity(file)
	info, statErr := file.Stat()
	closeErr := file.Close()
	if identityErr != nil || statErr != nil || closeErr != nil || !info.Mode().IsRegular() || identity != job.Agent.Identity {
		return authenticatedReviewMapping{}, errors.New("stored Agent identity changed")
	}
	return mapping, nil
}

func authenticateStoredReviewProject(dataDir string, job reviewjob.Job) (authenticatedReviewMapping, error) {
	mapping, err := authenticateReviewMapping(dataDir, job.ProjectID)
	if err != nil || mapping.projectIdentity != job.ProjectIdentity {
		return authenticatedReviewMapping{}, errors.New("stored review project identity changed")
	}
	return mapping, nil
}

func runReviewStart(dataDir, projectID, executable string, stdout io.Writer) int {
	mapping, err := authenticateReviewMapping(dataDir, projectID)
	if err != nil {
		return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	verified, err := reviewVerify(ctx, executable)
	cancel()
	if err != nil {
		return writeReviewOperational(stdout, projectID, reviewAgentError(err))
	}
	store := reviewjob.Store{Root: dataDir, RejectActiveProject: true}
	if current, revision, found, loadErr := store.LatestForProjectAuthenticated(projectID, mapping.projectIdentity); loadErr != nil {
		return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
	} else if found && reviewStateActive(current.State) {
		if _, authErr := authenticateStoredReviewProject(dataDir, current); authErr != nil {
			return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
		}
		current, revision, loadErr = recoverReviewJob(store, current, revision)
		if loadErr != nil {
			return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
		}
		if reviewStateActive(current.State) {
			if _, authErr := authenticateStoredReviewJob(dataDir, current); authErr != nil {
				return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
			}
			return writeReviewStatus(stdout, &current, projectID, revision, 0)
		}
	}
	sessions, err := platform.ResolveSessionsRoot("", currentEnv())
	if err != nil {
		return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
	}
	frozen, err := reviewFreeze(reviewjob.FreezeOptions{
		SessionsRoot: sessions.Path, DataRoot: dataDir, ProjectID: projectID, ProjectIdentity: mapping.projectIdentity,
	})
	if err != nil {
		return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
	}
	binary, err := reviewCurrentExecutable()
	if err != nil {
		return writeReviewOperational(stdout, projectID, reviewjob.AgentUnconfigured)
	}
	jobID, token, ownerErr := reviewAuthority()
	if ownerErr != nil {
		return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
	}
	now := canonicalReviewNow()
	job := reviewjob.Job{
		SchemaVersion: reviewjob.PublicStatusSchemaVersion,
		ID:            jobID, ProjectID: projectID, ProjectIdentity: mapping.projectIdentity, Agent: verified.Agent,
		State: reviewjob.Queued, Phase: reviewjob.Preflight, Attempt: 1, FrozenSessions: frozen,
		CreatedAt: now, UpdatedAt: now, LaunchTokenDigest: digestReviewToken(token), LaunchIntentAt: now,
	}
	if _, err := reviewCreate(store, job); err != nil {
		_, _, persistedFound, persistedErr := store.Load(job.ID)
		if persistedErr != nil {
			return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
		}
		if persistedFound {
			current, _, found, repairErr := store.LatestForProject(projectID)
			if repairErr != nil || !found || current.ID != job.ID {
				terminalizeReviewLaunch(store, job.ID, reviewjob.ApplyRecovery, canonicalReviewNow())
				return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
			}
		} else if current, revision, found, loadErr := store.LatestForProject(projectID); loadErr == nil && found && reviewStateActive(current.State) {
			return writeReviewStatus(stdout, &current, projectID, revision, 0)
		} else {
			return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
		}
	}
	if err := reviewLaunch(reviewLaunchRequest{Binary: binary, JobID: job.ID, Token: token}); err != nil {
		failed, revision := terminalizeReviewLaunch(store, job.ID, reviewLaunchError(err), canonicalReviewNow())
		return writeReviewStatus(stdout, &failed, projectID, revision, 1)
	}
	current, revision, found, err := store.Load(job.ID)
	if err != nil || !found || (reviewStateActive(current.State) && current.Owner.ID == "") {
		failed, failedRevision := terminalizeReviewLaunch(store, job.ID, reviewjob.ApplyRecovery, canonicalReviewNow())
		return writeReviewStatus(stdout, &failed, projectID, failedRevision, 1)
	}
	return writeReviewStatus(stdout, &current, projectID, revision, 0)
}

func runReviewStatus(dataDir, projectID string, stdout io.Writer) int {
	mapping, err := authenticateReviewMapping(dataDir, projectID)
	if err != nil {
		return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
	}
	store := reviewjob.Store{Root: dataDir}
	job, revision, found, err := store.LatestForProjectAuthenticated(projectID, mapping.projectIdentity)
	if err != nil {
		return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
	}
	if !found {
		return writeReviewStatus(stdout, nil, projectID, 0, 0)
	}
	if _, err := authenticateStoredReviewProject(dataDir, job); err != nil {
		return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
	}
	job, revision, err = recoverReviewJob(store, job, revision)
	if err != nil {
		return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
	}
	if _, err := authenticateStoredReviewJob(dataDir, job); err != nil {
		return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
	}
	return writeReviewStatus(stdout, &job, projectID, revision, 0)
}

func recoverReviewJob(store reviewjob.Store, job reviewjob.Job, revision int) (reviewjob.Job, int, error) {
	if !reviewStateActive(job.State) {
		return job, revision, nil
	}
	recovered, recoveredRevision, _, err := store.RecoverInterrupted(job.ID)
	if err != nil {
		return reviewjob.Job{}, 0, err
	}
	return recovered, recoveredRevision, nil
}

func runReviewCancel(dataDir, jobID string, stdout io.Writer) int {
	store := reviewjob.Store{Root: dataDir}
	job, revision, found, err := store.Load(jobID)
	if err != nil || !found {
		return writeReviewOperational(stdout, "unknown", reviewjob.ApplyRecovery)
	}
	if _, err := authenticateStoredReviewProject(dataDir, job); err != nil {
		return writeReviewOperational(stdout, job.ProjectID, reviewjob.ApplyRecovery)
	}
	job, revision, err = recoverReviewJob(store, job, revision)
	if err != nil {
		return writeReviewOperational(stdout, job.ProjectID, reviewjob.ApplyRecovery)
	}
	if _, err := authenticateStoredReviewJob(dataDir, job); err != nil {
		return writeReviewOperational(stdout, job.ProjectID, reviewjob.ApplyRecovery)
	}
	job, revision, err = reviewjob.RequestCancel(store, jobID, canonicalReviewNow())
	if err != nil {
		if current, currentRevision, currentFound, loadErr := store.Load(jobID); loadErr == nil && currentFound {
			return writeReviewStatus(stdout, &current, current.ProjectID, currentRevision, 0)
		}
		return writeReviewOperational(stdout, job.ProjectID, reviewjob.ApplyRecovery)
	}
	return writeReviewStatus(stdout, &job, job.ProjectID, revision, 0)
}

func runReviewRetry(dataDir, jobID, executable string, expectedAttempt, expectedRevision int, stdout io.Writer) int {
	store := reviewjob.Store{Root: dataDir}
	before, beforeRevision, found, err := store.Load(jobID)
	if err != nil || !found {
		return writeReviewOperational(stdout, "unknown", reviewjob.ApplyRecovery)
	}
	requestID := deterministicRetryRequestID(jobID, expectedAttempt, expectedRevision)
	if before.RetryRequestID == requestID {
		if before.RetryAttempt != expectedAttempt || before.RetryRevision != expectedRevision {
			return writeReviewOperational(stdout, before.ProjectID, reviewjob.ApplyRecovery)
		}
		if _, err := authenticateStoredReviewProject(dataDir, before); err != nil {
			return writeReviewOperational(stdout, before.ProjectID, reviewjob.ApplyRecovery)
		}
		before, beforeRevision, err = recoverReviewJob(store, before, beforeRevision)
		if err != nil {
			return writeReviewOperational(stdout, before.ProjectID, reviewjob.ApplyRecovery)
		}
		return writeReviewStatus(stdout, &before, before.ProjectID, beforeRevision, 0)
	}
	if _, err := authenticateStoredReviewProject(dataDir, before); err != nil {
		return writeReviewOperational(stdout, before.ProjectID, reviewjob.ApplyRecovery)
	}
	before, beforeRevision, err = recoverReviewJob(store, before, beforeRevision)
	if err != nil {
		return writeReviewOperational(stdout, before.ProjectID, reviewjob.ApplyRecovery)
	}
	if before.State != reviewjob.Failed || before.Attempt != expectedAttempt || beforeRevision != expectedRevision {
		return writeReviewStatus(stdout, &before, before.ProjectID, beforeRevision, 1)
	}
	binary, err := reviewCurrentExecutable()
	if err != nil {
		return writeReviewOperational(stdout, before.ProjectID, reviewjob.AgentUnconfigured)
	}
	if _, err := authenticateStoredReviewJob(dataDir, before); err != nil {
		return writeReviewOperational(stdout, before.ProjectID, reviewjob.ApplyRecovery)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	verified, err := reviewVerify(ctx, executable)
	cancel()
	if err != nil {
		return writeReviewOperational(stdout, before.ProjectID, reviewAgentError(err))
	}
	if verified.Agent != before.Agent {
		return writeReviewOperational(stdout, before.ProjectID, reviewjob.AgentIncompatible)
	}
	_, token, err := reviewAuthority()
	if err != nil {
		return writeReviewOperational(stdout, before.ProjectID, reviewjob.ApplyRecovery)
	}
	launchAt := canonicalReviewNow()
	launchDigest := digestReviewToken(token)
	job, revision, err := reviewjob.RequestRetry(store, reviewjob.RetryRequest{
		JobID: jobID, ExpectedAttempt: expectedAttempt, ExpectedRevision: expectedRevision,
		RequestID: requestID, At: launchAt, LaunchTokenDigest: launchDigest, LaunchIntentAt: launchAt,
	})
	if err != nil {
		if current, currentRevision, currentFound, loadErr := store.Load(jobID); loadErr == nil && currentFound {
			return writeReviewStatus(stdout, &current, current.ProjectID, currentRevision, 1)
		}
		return writeReviewOperational(stdout, before.ProjectID, reviewjob.ApplyRecovery)
	}
	if job.LaunchTokenDigest != launchDigest || (job.State != reviewjob.Retrying && job.State != reviewjob.CancelRequested) || job.Owner.ID != "" {
		return writeReviewStatus(stdout, &job, job.ProjectID, revision, 0)
	}
	if err := reviewLaunch(reviewLaunchRequest{Binary: binary, JobID: jobID, Token: token}); err != nil {
		failed, failedRevision := terminalizeReviewLaunch(store, jobID, reviewLaunchError(err), canonicalReviewNow())
		return writeReviewStatus(stdout, &failed, failed.ProjectID, failedRevision, 1)
	}
	current, currentRevision, found, err := store.Load(jobID)
	if err != nil || !found || (reviewStateActive(current.State) && current.Owner.ID == "") {
		failed, failedRevision := terminalizeReviewLaunch(store, jobID, reviewjob.ApplyRecovery, canonicalReviewNow())
		return writeReviewStatus(stdout, &failed, before.ProjectID, failedRevision, 1)
	}
	return writeReviewStatus(stdout, &current, current.ProjectID, currentRevision, 0)
}

func reviewStateActive(state reviewjob.State) bool {
	return state == reviewjob.Queued || state == reviewjob.Running || state == reviewjob.CancelRequested || state == reviewjob.Retrying
}

func canonicalReviewNow() time.Time { return reviewNow().UTC().Round(0) }

func newReviewLaunchAuthority() (string, string, error) {
	jobBytes := make([]byte, 16)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(jobBytes); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", err
	}
	return fmt.Sprintf("job-%x", jobBytes), base64.RawURLEncoding.EncodeToString(tokenBytes), nil
}

func digestReviewToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return fmt.Sprintf("sha256:%x", digest[:])
}

func deterministicRetryRequestID(jobID string, attempt, revision int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", jobID, attempt, revision)))
	return fmt.Sprintf("retry-%x", digest[:16])
}

func currentReviewExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil || !filepath.IsAbs(executable) {
		return "", errors.New("current executable is unavailable")
	}
	physical, err := filepath.EvalSymlinks(executable)
	if err != nil || !filepath.IsAbs(physical) {
		return "", errors.New("current executable cannot be resolved")
	}
	info, err := os.Stat(physical)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("current executable is unsafe")
	}
	return physical, nil
}

func reviewAgentError(err error) reviewjob.ErrorCode {
	if code, ok := agent.CodeOf(err); ok {
		return reviewjob.ErrorCode(code)
	}
	return reviewjob.AgentUnconfigured
}

func reviewLaunchError(err error) reviewjob.ErrorCode {
	if errors.Is(err, reviewjob.ErrAgentBusy) {
		return reviewjob.AgentBusy
	}
	return reviewjob.ApplyRecovery
}

func terminalizeReviewLaunch(store reviewjob.Store, jobID string, code reviewjob.ErrorCode, at time.Time) (reviewjob.Job, int) {
	for attempts := 0; attempts < 64; attempts++ {
		job, revision, found, err := store.Load(jobID)
		if err != nil || !found {
			return reviewjob.Job{}, 0
		}
		if !reviewStateActive(job.State) {
			return job, revision
		}
		next, nextRevision, err := store.Update(jobID, revision, func(next *reviewjob.Job) error {
			if !reviewStateActive(next.State) {
				return reviewjob.ErrStaleRevision
			}
			next.State = reviewjob.Failed
			next.Phase = ""
			next.Owner = reviewjob.Owner{}
			next.CompletedAt = at
			next.UpdatedAt = at
			next.Error = reviewjob.SafeError{Code: code}
			next.PrivateError = "detached review worker did not establish durable ownership"
			next.LaunchTokenDigest = ""
			next.LaunchIntentAt = time.Time{}
			return nil
		})
		if errors.Is(err, reviewjob.ErrStaleRevision) {
			continue
		}
		if err == nil {
			return next, nextRevision
		}
		return job, revision
	}
	job, revision, _, _ := store.Load(jobID)
	return job, revision
}

func writeReviewOperational(stdout io.Writer, projectID string, code reviewjob.ErrorCode) int {
	if !safeReviewID(projectID) {
		projectID = "unknown"
	}
	status := reviewjob.PublicStatus{
		SchemaVersion: reviewjob.PublicStatusSchemaVersion, ProjectID: projectID,
		State: reviewjob.Idle, ErrorCode: string(code),
	}
	return writeReviewStatusValue(stdout, status, 1)
}

func writeReviewStatus(stdout io.Writer, job *reviewjob.Job, projectID string, revision, code int) int {
	var status reviewjob.PublicStatus
	var err error
	if job != nil && job.State == reviewjob.Failed {
		status, err = reviewjob.ProjectStatusAtRevision(job, projectID, revision)
	} else {
		status, err = reviewjob.ProjectStatus(job, projectID)
	}
	if err != nil {
		return writeReviewOperational(stdout, projectID, reviewjob.ApplyRecovery)
	}
	return writeReviewStatusValue(stdout, status, code)
}

func writeReviewStatusValue(stdout io.Writer, status reviewjob.PublicStatus, code int) int {
	if err := reviewjob.ValidatePublicStatus(status); err != nil || !writeReviewJSON(stdout, status) {
		return 1
	}
	return code
}

func runPrivateReviewWorker(args []string) int {
	values, ok := parsePrivateReviewWorkerFlags(args)
	if !ok {
		return 2
	}
	handshake, err := inheritedReviewHandshake(values[privateReviewHandshakeFlag()])
	if err != nil {
		return 1
	}
	defer handshake.Close()
	dataDir, err := resolveReviewDataDir()
	if err != nil {
		_, _ = handshake.Write([]byte{0})
		return 1
	}
	err = executePrivateReviewWorker(dataDir, values["job-id"], values["launch-token"], handshake)
	if err != nil {
		response := byte(0)
		if errors.Is(err, reviewjob.ErrAgentBusy) {
			response = 2
		}
		_, _ = handshake.Write([]byte{response})
		return 1
	}
	return 0
}

func parsePrivateReviewWorkerFlags(args []string) (map[string]string, bool) {
	required := []string{"job-id", "launch-token", privateReviewHandshakeFlag()}
	parsed, ok := parseReviewFlags(args, required)
	if !ok || parsed.json || !safeReviewID(parsed.values["job-id"]) || len(parsed.values["launch-token"]) < 32 {
		return nil, false
	}
	return parsed.values, true
}

func executePrivateReviewWorker(dataDir, jobID, token string, handshake *os.File) error {
	store := reviewjob.Store{Root: dataDir}
	if err := reviewjob.VerifyLaunchAuthority(store, jobID, token); err != nil {
		return err
	}
	job, _, found, err := store.Load(jobID)
	if err != nil || !found {
		return os.ErrNotExist
	}
	mapping, err := authenticateStoredReviewJob(dataDir, job)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	verified, err := reviewVerify(ctx, job.Agent.Executable)
	cancel()
	if err != nil || verified.Agent != job.Agent || verified.Handle == nil {
		return errors.New("stored Agent verification failed")
	}
	sessions, err := platform.ResolveSessionsRoot("", currentEnv())
	if err != nil {
		return err
	}
	ownerBytes := make([]byte, 16)
	if _, err := rand.Read(ownerBytes); err != nil {
		return err
	}
	owned := false
	return reviewjob.Run(context.Background(), reviewjob.RunOptions{
		Store: store, JobID: jobID, OwnerID: fmt.Sprintf("owner-%x", ownerBytes),
		LeaseTimeout: 0, ProjectRoot: mapping.projectRoot, VaultRoot: mapping.vaultRoot, DataDir: dataDir,
		GOOS: runtime.GOOS, AgentTimeout: 5 * time.Minute, Now: canonicalReviewNow,
		LaunchToken: token,
		OwnershipReady: func() error {
			if owned {
				return errors.New("duplicate ownership handshake")
			}
			owned = true
			_, err := handshake.Write([]byte{1})
			if err == nil {
				err = handshake.Close()
			}
			return err
		},
		Prepare: func(_ context.Context, request reviewjob.PrepareRequest) (reviewjob.Prepared, error) {
			project, err := pathguard.Open(request.ProjectRoot)
			if err != nil {
				return reviewjob.Prepared{}, err
			}
			defer project.Close()
			accepted, err := reviewv2.LoadExpected(request.ProjectRoot, project.Info())
			if err != nil {
				return reviewjob.Prepared{}, err
			}
			result, err := prepare.Run(prepare.Options{
				Mode: "review", SessionsRoot: sessions.Path, SessionID: request.SessionID,
				CWD: request.ProjectRoot, DataDir: request.DataDir, GOOS: runtime.GOOS,
				Now: canonicalReviewNow(), AmbiguityWindow: time.Second, UpperBoundary: &request.UpperBoundary,
			})
			if err != nil {
				return reviewjob.Prepared{}, err
			}
			return reviewjob.Prepared{Packet: result.Packet, PacketBytes: result.Canonical, Accepted: accepted}, nil
		},
		Agent: verified.Handle,
		Apply: func(_ context.Context, request reviewjob.ApplyRequest) (applyengine.Result, error) {
			return applyengine.Run(applyengine.Options{
				ProposalPath: request.ProposalPath, EvidencePath: request.EvidencePath,
				ProjectRoot: request.ProjectRoot, DataDir: request.DataDir, Now: canonicalReviewNow,
			})
		},
		Sync: syncproject.Run, Pricing: reviewjob.ProductionPricingCatalog(),
	})
}

func launchDetachedReviewWorker(request reviewLaunchRequest) error {
	if !filepath.IsAbs(request.Binary) || !safeReviewID(request.JobID) || len(request.Token) < 32 {
		return errors.New("detached review launch request is invalid")
	}
	parent, child, err := os.Pipe()
	if err != nil {
		return err
	}
	defer parent.Close()
	handshakeValue, err := detachedReviewHandshakeValue(child)
	if err != nil {
		_ = child.Close()
		return err
	}
	args := []string{
		"review", "worker", "--job-id", request.JobID, "--launch-token", request.Token,
		"--" + privateReviewHandshakeFlag(), handshakeValue,
	}
	command := exec.Command(request.Binary, args...)
	cleanup, err := configureDetachedReviewCommand(command, child)
	if err != nil {
		_ = child.Close()
		return err
	}
	if err := command.Start(); err != nil {
		_ = child.Close()
		_ = cleanup()
		return err
	}
	_ = child.Close()
	_ = cleanup()
	if err := parent.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		terminateDetachedReviewProcess(command)
		return err
	}
	var response [1]byte
	n, readErr := parent.Read(response[:])
	if readErr != nil || n != 1 || response[0] != 1 {
		terminateDetachedReviewProcess(command)
		if readErr != nil {
			return readErr
		}
		if response[0] == 2 {
			return reviewjob.ErrAgentBusy
		}
		return errors.New("detached review worker rejected launch")
	}
	go func() { _ = command.Wait() }()
	return nil
}

func terminateDetachedReviewProcess(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Kill()
	_ = command.Wait()
}

func writeReviewJSON(writer io.Writer, value any) bool {
	body, err := json.Marshal(value)
	if err != nil || len(body)+1 > maxReviewPublicJSONBytes {
		return false
	}
	body = append(body, '\n')
	_, err = writer.Write(body)
	return err == nil
}
