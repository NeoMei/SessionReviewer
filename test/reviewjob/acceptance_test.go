package reviewjob

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/prepare"
	"github.com/neomei/SessionReviewer/internal/proposal"
	reviewjob "github.com/neomei/SessionReviewer/internal/reviewjob"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

var (
	cliBin     string
	fakeBin    string
	fakeDigest string
)

func TestMain(m *testing.M) {
	binDir, err := os.MkdirTemp("", "reviewjob-acceptance-bin")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create binary directory: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(binDir)
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	cliBin = filepath.Join(binDir, "session-reviewer"+suffix)
	build := exec.Command("go", "build", "-o", cliBin, "./cmd/session-reviewer")
	build.Dir = "../.."
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		fmt.Fprintf(os.Stderr, "build session-reviewer: %v\n%s\n", buildErr, output)
		os.Exit(1)
	}
	fakeBin = filepath.Join(binDir, "fake-codex"+suffix)
	build = exec.Command("go", "build", "-o", fakeBin, "./testdata/fake-codex.go")
	build.Dir = "."
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		fmt.Fprintf(os.Stderr, "build fake codex: %v\n%s\n", buildErr, output)
		os.Exit(1)
	}
	digestBytes, err := os.ReadFile(fakeBin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read fake codex digest: %v\n", err)
		os.Exit(1)
	}
	fakeDigest = fmt.Sprintf("%x", sha256.Sum256(digestBytes))
	code := m.Run()
	if runtime.GOOS != "windows" {
		_ = exec.Command("pkill", "-f", fakeBin).Run()
	}
	os.Exit(code)
}

type reviewEnv struct {
	base      string
	home      string
	appdata   string
	project   string
	vault     string
	sessions  string
	proposals string
	dataDir   string
	projectID string
	fakeEnv   map[string]string
}

func newReviewEnv(t *testing.T) *reviewEnv {
	t.Helper()
	switch runtime.GOOS {
	case "darwin", "windows":
	default:
		t.Skip("session-reviewer data directories are only supported on darwin and windows")
	}
	base, err := os.MkdirTemp("", "reviewjob-acceptance")
	if err != nil {
		t.Fatalf("create acceptance base: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	env := &reviewEnv{
		base:      base,
		project:   filepath.Join(base, "project"),
		vault:     filepath.Join(base, "vault"),
		sessions:  filepath.Join(base, "sessions"),
		proposals: filepath.Join(base, "proposals"),
	}
	switch runtime.GOOS {
	case "windows":
		env.appdata = filepath.Join(base, "appdata")
		env.dataDir = filepath.Join(env.appdata, "SessionReviewer")
	default:
		env.home = filepath.Join(base, "home")
		env.dataDir = filepath.Join(env.home, ".local", "share", "session-reviewer")
	}
	for _, directory := range []string{env.project, env.vault, env.sessions, env.proposals, env.dataDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create %s: %v", directory, err)
		}
	}
	output := env.run(t, 2*time.Minute, "init", "--project", env.project, "--vault", env.vault, "--write")
	env.projectID = parseProjectID(t, output)
	return env
}

func parseProjectID(t *testing.T, output string) string {
	t.Helper()
	marker := "project_id: "
	index := strings.LastIndex(output, marker)
	if index < 0 {
		t.Fatalf("init output does not contain a project id:\n%s", output)
	}
	rest := output[index+len(marker):]
	if end := strings.IndexByte(rest, '\n'); end >= 0 {
		rest = rest[:end]
	}
	projectID := strings.TrimSpace(rest)
	if projectID == "" {
		t.Fatalf("init output contains an empty project id:\n%s", output)
	}
	return projectID
}

func (e *reviewEnv) childEnv(extra map[string]string) []string {
	skip := map[string]bool{
		"HOME":                                   true,
		"USERPROFILE":                            true,
		"LOCALAPPDATA":                           true,
		"SESSION_REVIEWER_SESSIONS_ROOT":         true,
		"SESSIONREVIEWER_CODEX_HERMETIC_DIGESTS": true,
	}
	result := make([]string, 0, len(os.Environ())+len(extra)+4)
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if !found || skip[name] || strings.HasPrefix(name, "CODEX_") || strings.HasPrefix(name, "SESSIONREVIEWER_FAKE_") {
			continue
		}
		result = append(result, entry)
	}
	if runtime.GOOS == "windows" {
		result = append(result, "LOCALAPPDATA="+e.appdata, "USERPROFILE="+e.home)
	} else {
		result = append(result, "HOME="+e.home)
	}
	result = append(result, "SESSION_REVIEWER_SESSIONS_ROOT="+e.sessions)
	result = append(result, "SESSIONREVIEWER_CODEX_HERMETIC_DIGESTS="+fakeDigest)
	for name, value := range extra {
		result = append(result, name+"="+value)
	}
	return result
}

func (e *reviewEnv) run(t *testing.T, timeout time.Duration, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, cliBin, args...)
	command.Env = e.childEnv(e.fakeEnv)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("session-reviewer %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func (e *reviewEnv) runStatusCommand(t *testing.T, timeout time.Duration, args ...string) reviewjob.PublicStatus {
	t.Helper()
	output := e.run(t, timeout, args...)
	status := parsePublicStatus(t, output)
	if err := reviewjob.ValidatePublicStatus(status); err != nil {
		t.Fatalf("public review status is invalid: %v\n%s", err, output)
	}
	return status
}

func parsePublicStatus(t *testing.T, output string) reviewjob.PublicStatus {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var status reviewjob.PublicStatus
		if err := json.Unmarshal([]byte(line), &status); err == nil {
			return status
		}
	}
	t.Fatalf("no public review status JSON in output:\n%s", output)
	return reviewjob.PublicStatus{}
}

func (e *reviewEnv) status(t *testing.T) reviewjob.PublicStatus {
	t.Helper()
	return e.runStatusCommand(t, 30*time.Second, "review", "status", "--project-id", e.projectID, "--json")
}

func (e *reviewEnv) waitForStatus(t *testing.T, timeout time.Duration, description string, want func(reviewjob.PublicStatus) bool) reviewjob.PublicStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last reviewjob.PublicStatus
	for {
		last = e.status(t)
		if want(last) {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last status: %s", description, statusJSON(last))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitForStableStatus waits until want(status) holds for two consecutive
// identical polls. A worker failure performs a second post-failure revision
// bump (payload cleanup completion), so the first satisfying snapshot can
// still race the retry expectations that the caller is about to present.
func (e *reviewEnv) waitForStableStatus(t *testing.T, timeout time.Duration, description string, want func(reviewjob.PublicStatus) bool) reviewjob.PublicStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last reviewjob.PublicStatus
	for {
		last = e.status(t)
		if want(last) {
			time.Sleep(200 * time.Millisecond)
			if next := e.status(t); statusJSON(next) == statusJSON(last) {
				return next
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last status: %s", description, statusJSON(last))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func statusJSON(status reviewjob.PublicStatus) string {
	body, err := json.Marshal(status)
	if err != nil {
		return fmt.Sprintf("%+v", status)
	}
	return string(body)
}

func (e *reviewEnv) writeSession(t *testing.T, index int) string {
	t.Helper()
	sessionID := fmt.Sprintf("session-acceptance-%d", index)
	start := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC).Add(time.Duration(30*index) * time.Minute)
	meta := map[string]string{"id": sessionID, "cwd": filepath.ToSlash(e.project), "source": "vscode"}
	user := map[string]any{
		"type": "message",
		"role": "user",
		"content": []map[string]string{{
			"type": "input_text",
			"text": fmt.Sprintf("Acceptance user request %d", index),
		}},
	}
	assistant := map[string]any{
		"type": "message",
		"role": "assistant",
		"content": []map[string]string{{
			"type": "output_text",
			"text": fmt.Sprintf("Acceptance assistant summary %d", index),
		}},
	}
	var builder strings.Builder
	for _, record := range []struct {
		offset  time.Duration
		envType string
		payload any
	}{
		{offset: 0, envType: "session_meta", payload: meta},
		{offset: time.Minute, envType: "response_item", payload: user},
		{offset: 2 * time.Minute, envType: "response_item", payload: assistant},
	} {
		body, err := json.Marshal(map[string]any{
			"timestamp": start.Add(record.offset).Format(time.RFC3339Nano),
			"type":      record.envType,
			"payload":   record.payload,
		})
		if err != nil {
			t.Fatalf("encode session %s record: %v", sessionID, err)
		}
		builder.Write(body)
		builder.WriteByte('\n')
	}
	path := filepath.Join(e.sessions, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatalf("write session %s: %v", sessionID, err)
	}
	return sessionID
}

func (e *reviewEnv) prepareFixtures(t *testing.T, count int) []string {
	t.Helper()
	project, err := pathguard.Open(e.project)
	if err != nil {
		t.Fatalf("open project root: %v", err)
	}
	defer project.Close()
	identity, err := project.PhysicalIdentity()
	if err != nil {
		t.Fatalf("identify project root: %v", err)
	}
	frozen, err := reviewjob.FreezePending(reviewjob.FreezeOptions{
		SessionsRoot:    e.sessions,
		DataRoot:        e.dataDir,
		ProjectID:       e.projectID,
		ProjectIdentity: identity,
	})
	if err != nil {
		t.Fatalf("freeze pending sessions: %v", err)
	}
	if len(frozen) != count {
		t.Fatalf("frozen session count = %d, want %d", len(frozen), count)
	}
	loaded, err := reviewv2.LoadExpected(e.project, project.Info())
	if err != nil {
		t.Fatalf("load accepted review ledger: %v", err)
	}
	mirror := loaded.Legacy
	if mirror.Decisions == nil {
		mirror.Decisions = make(map[string]ledger.Decision)
	}
	if mirror.Sessions == nil {
		mirror.Sessions = make(map[string]ledger.SessionReport)
	}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	paths := make([]string, 0, len(frozen))
	for index, item := range frozen {
		body := e.buildProposalFixture(t, item, index, now, &mirror)
		path := filepath.Join(e.proposals, fmt.Sprintf("session-acceptance-%d.json", index))
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatalf("write proposal fixture %d: %v", index, err)
		}
		paths = append(paths, path)
	}
	return paths
}

func (e *reviewEnv) buildProposalFixture(t *testing.T, item reviewjob.FrozenSession, index int, now time.Time, mirror *ledger.State) []byte {
	t.Helper()
	prepared, err := prepare.Run(prepare.Options{
		Mode:            "review",
		SessionsRoot:    e.sessions,
		SessionID:       item.SessionID,
		CWD:             e.project,
		DataDir:         e.dataDir,
		GOOS:            runtime.GOOS,
		Now:             now,
		AmbiguityWindow: time.Second,
		UpperBoundary:   &item.Upper,
	})
	if err != nil {
		t.Fatalf("prepare fixture packet %d: %v", index, err)
	}
	packet := prepared.Packet
	digest, err := evidence.Digest(packet)
	if err != nil {
		t.Fatalf("digest fixture packet %d: %v", index, err)
	}
	if len(packet.Events) != 2 {
		t.Fatalf("fixture packet %d has %d events, want 2", index, len(packet.Events))
	}
	refs := make([]ledger.EvidenceRef, 0, len(packet.Events))
	for _, event := range packet.Events {
		refs = append(refs, ledger.EvidenceRef{
			EvidenceID: event.ID,
			SessionID:  packet.SessionID,
			JSONLLine:  event.JSONLLine,
			SourceHash: event.SourceHash,
			Summary:    event.Summary,
		})
	}
	decisionID := fmt.Sprintf("decision-acceptance-%d", index)
	timelineID := fmt.Sprintf("timeline-acceptance-%d", index)
	reportID := fmt.Sprintf("report-acceptance-%d", index)
	previous := ""
	if index > 0 {
		previous = fmt.Sprintf("session-acceptance-%d", index-1)
	}
	draft := proposal.Proposal{
		SchemaVersion:        1,
		ProjectID:            packet.ProjectID,
		SessionID:            packet.SessionID,
		FromCursor:           packet.FromCursor,
		ToCursor:             packet.ToCursor,
		EvidencePacketSHA256: digest,
		NewDecisions: []ledger.Decision{{
			ID:             decisionID,
			ProjectID:      packet.ProjectID,
			Title:          fmt.Sprintf("Acceptance decision %d", index),
			Status:         "accepted",
			Revision:       1,
			Tags:           []string{},
			Supersedes:     []string{},
			SourceSessions: []string{packet.SessionID},
			Evidence:       []ledger.EvidenceRef{refs[0]},
			Context:        fmt.Sprintf("Acceptance context %d", index),
			Rationale:      fmt.Sprintf("Acceptance rationale %d", index),
			Consequences:   fmt.Sprintf("Acceptance consequences %d", index),
			Alternatives:   []string{},
			RejectedPaths:  []string{},
		}},
		UpdatedDecisions: []proposal.DecisionPatch{},
		OpenLoops:        []proposal.OpenLoopChange{},
		TimelineEvents: []ledger.TimelineEvent{{
			ID:          timelineID,
			OccurredAt:  item.StartedAt.Format(time.RFC3339Nano),
			Revision:    1,
			Class:       ledger.Verified,
			Title:       fmt.Sprintf("Acceptance timeline %d", index),
			Summary:     fmt.Sprintf("Acceptance timeline summary %d", index),
			Evidence:    []ledger.EvidenceRef{refs[1]},
			DecisionIDs: []string{decisionID},
			OpenLoopIDs: []string{},
		}},
		CurrentStatePatch: proposal.CurrentStatePatch{
			ExpectedRevision: mirror.CurrentState.Revision,
			Goal:             stringPointer(fmt.Sprintf("Acceptance goal %d", index)),
			SourceSessions:   stringSlicePointer([]string{packet.SessionID}),
			Evidence:         evidenceSlicePointer([]ledger.EvidenceRef{refs[0]}),
		},
		SessionReport: ledger.SessionReport{
			ID:          reportID,
			ProjectID:   packet.ProjectID,
			SessionID:   packet.SessionID,
			Revision:    1,
			InitialGoal: fmt.Sprintf("Acceptance initial goal %d", index),
			GoalChanges: []string{},
			Phases: []ledger.SessionPhase{{
				Title:    "Acceptance phase",
				Summary:  fmt.Sprintf("Acceptance phase summary %d", index),
				Evidence: []ledger.EvidenceRef{},
			}},
			Files:             []string{},
			Commits:           []string{},
			Verification:      []string{},
			DecisionsAdded:    []string{decisionID},
			DecisionsRevised:  []string{},
			OpenLoopsCreated:  []string{},
			OpenLoopsClosed:   []string{},
			PreviousSessionID: previous,
			NextSessionID:     "",
			Evidence:          []ledger.EvidenceRef{refs[1]},
		},
		EvidenceLinks: []proposal.EvidenceLink{
			{EntityID: decisionID, EvidenceID: refs[0].EvidenceID, Relation: "supports"},
			{EntityID: timelineID, EvidenceID: refs[1].EvidenceID, Relation: "verifies"},
			{EntityID: "current-state", EvidenceID: refs[0].EvidenceID, Relation: "supports"},
			{EntityID: reportID, EvidenceID: refs[1].EvidenceID, Relation: "supports"},
		},
	}
	// The fake emulates the real agent, whose draft must never carry
	// host-owned accounting; the service enriches it from packet usage before
	// validation. Validate that enriched shape on a copy while emitting the
	// un-enriched draft the agent boundary is allowed to produce.
	enriched := draft
	enriched.SessionReport.Accounting = sessionAccountingFromUsage(t, packet.SessionUsage)
	change, err := proposal.Validate(enriched, packet, *mirror)
	if err != nil {
		t.Fatalf("fixture proposal %d is invalid: %v", index, err)
	}
	body, err := json.Marshal(draft)
	if err != nil {
		t.Fatalf("encode fixture proposal %d: %v", index, err)
	}
	if _, err := proposal.Decode(strings.NewReader(string(body))); err != nil {
		t.Fatalf("fixture proposal %d round trip: %v", index, err)
	}
	mirror.CurrentState = *change.Current
	mirror.Timeline = append(mirror.Timeline, change.Timeline...)
	for _, decision := range change.Decisions {
		mirror.Decisions[decision.ID] = decision
	}
	for _, report := range change.Sessions {
		mirror.Sessions[report.ID] = report
	}
	return body
}

func stringPointer(value string) *string { return &value }

func sessionAccountingFromUsage(t *testing.T, usage *accounting.SessionUsage) *accounting.SessionAccounting {
	t.Helper()
	if usage == nil {
		return nil
	}
	pricing := accounting.Pricing{
		Currency:                  "USD",
		InputPerMillion:           1,
		CachedInputPerMillion:     1,
		CacheWriteInputPerMillion: 1,
		OutputPerMillion:          1,
		Source:                    "https://example.com/pricing",
		AsOf:                      "2026-08-29",
	}
	report := &accounting.SessionAccounting{
		StartedAt:   usage.StartedAt,
		EndedAt:     usage.EndedAt,
		DurationMS:  usage.DurationMS,
		Models:      make([]accounting.ModelAccounting, 0, len(usage.Models)),
		TotalTokens: usage.TotalTokens,
	}
	for _, model := range usage.Models {
		cost, err := accounting.PriceUsage(model.TokenUsage, pricing)
		if err != nil {
			t.Fatalf("price fixture usage model %q: %v", model.Model, err)
		}
		report.Models = append(report.Models, accounting.ModelAccounting{ModelUsage: model, Pricing: pricing, CostUSD: cost})
		report.TotalCostUSD += cost
	}
	return report
}

func stringSlicePointer(values []string) *[]string { return &values }

func evidenceSlicePointer(refs []ledger.EvidenceRef) *[]ledger.EvidenceRef { return &refs }

func (e *reviewEnv) useFake(t *testing.T, modes []string, proposalPaths []string, readyPath string) {
	t.Helper()
	extra := map[string]string{
		"SESSIONREVIEWER_FAKE_COUNTER_PATH": filepath.Join(e.base, "fake-counter"),
		"SESSIONREVIEWER_FAKE_PROPOSALS":    e.writeFakeJSONFile(t, "fake-proposals.json", proposalPaths),
	}
	if len(modes) > 0 {
		extra["SESSIONREVIEWER_FAKE_MODES"] = e.writeFakeJSONFile(t, "fake-modes.json", modes)
	}
	if readyPath != "" {
		extra["SESSIONREVIEWER_FAKE_READY_PATH"] = readyPath
	}
	e.fakeEnv = extra
}

func (e *reviewEnv) writeFakeJSONFile(t *testing.T, name string, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	path := filepath.Join(e.base, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func (e *reviewEnv) counter(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.base, "fake-counter"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read fake counter: %v", err)
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse fake counter: %v", err)
	}
	return value
}

func (e *reviewEnv) cursorExists(t *testing.T, sessionID string) bool {
	t.Helper()
	path := filepath.Join(e.dataDir, "projects", e.projectID, "cursors", sessionID+".json")
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	t.Fatalf("stat cursor %s: %v", sessionID, err)
	return false
}

func (e *reviewEnv) vaultReviewDocuments(t *testing.T) map[string]string {
	t.Helper()
	data, err := pathguard.Open(e.dataDir)
	if err != nil {
		t.Fatalf("open data root: %v", err)
	}
	defer data.Close()
	cfg, err := config.LoadRoot(data.Root, "config.toml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	mapping, found := cfg.ProjectByID(e.projectID)
	if !found {
		t.Fatalf("config does not contain project %s", e.projectID)
	}
	reviewRoot := filepath.Join(mapping.VaultRoot, filepath.FromSlash(mapping.VaultReviewPath))
	documents := make(map[string]string)
	err = filepath.WalkDir(reviewRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(reviewRoot, path)
		if err != nil {
			return err
		}
		documents[relative] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("walk vault review documents: %v", err)
	}
	return documents
}

func waitForReady(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func killReviewWorker(t *testing.T, jobID string) {
	t.Helper()
	output, err := exec.Command("ps", "-axo", "pid=,command=").CombinedOutput()
	if err != nil {
		t.Fatalf("list processes: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.Contains(line, cliBin) || !strings.Contains(line, "review worker") || !strings.Contains(line, jobID) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if _, err := strconv.Atoi(fields[0]); err != nil {
			continue
		}
		if err := exec.Command("kill", "-9", fields[0]).Run(); err != nil {
			t.Fatalf("kill review worker %s: %v", fields[0], err)
		}
		return
	}
	t.Fatalf("review worker for job %s was not found", jobID)
}

func TestReviewJobAcceptanceHappyPath(t *testing.T) {
	env := newReviewEnv(t)
	for index := 0; index < 3; index++ {
		env.writeSession(t, index)
	}
	proposalPaths := env.prepareFixtures(t, 3)
	env.useFake(t, nil, proposalPaths, "")
	first := env.runStatusCommand(t, 2*time.Minute, "review", "start", "--project-id", env.projectID, "--agent-executable", fakeBin, "--json")
	if first.JobID == "" {
		t.Fatalf("review start returned an empty job id: %s", statusJSON(first))
	}
	status := env.waitForStatus(t, 2*time.Minute, "completion of the first review job", func(last reviewjob.PublicStatus) bool {
		return string(last.State) == "completed" && last.JobID == first.JobID
	})
	if status.SessionCount != 3 || status.AcceptedSessions != 3 {
		t.Fatalf("completed status counts = (%d sessions, %d accepted), want (3, 3): %s", status.SessionCount, status.AcceptedSessions, statusJSON(status))
	}
	if got := env.counter(t); got != 3 {
		t.Fatalf("fake exec counter = %d, want 3", got)
	}
	if status.ReviewUsage == nil || status.ReviewUsage.TotalTokens <= 0 {
		t.Fatalf("completed status lacks positive review usage: %s", statusJSON(status))
	}
	if status.ReviewUsage.PricingComplete || status.ReviewUsage.TotalCostUSD != nil {
		t.Fatalf("review usage must be unpriced without model data: %s", statusJSON(status))
	}
	for index := 0; index < 3; index++ {
		if !env.cursorExists(t, fmt.Sprintf("session-acceptance-%d", index)) {
			t.Fatalf("accepted cursor for session %d is missing", index)
		}
	}
	documents := env.vaultReviewDocuments(t)
	synced := false
	for _, body := range documents {
		if strings.Contains(body, "Acceptance decision 0") {
			synced = true
		}
	}
	if !synced {
		t.Fatalf("vault review documents do not contain the accepted decision (%d documents)", len(documents))
	}
	second := env.runStatusCommand(t, 2*time.Minute, "review", "start", "--project-id", env.projectID, "--agent-executable", fakeBin, "--json")
	if second.JobID == "" || second.JobID == first.JobID {
		t.Fatalf("second review start must create a new job: %s", statusJSON(second))
	}
	empty := env.waitForStatus(t, 2*time.Minute, "completion of the empty second review job", func(last reviewjob.PublicStatus) bool {
		return string(last.State) == "completed" && last.JobID == second.JobID
	})
	if empty.SessionCount != 0 || empty.AcceptedSessions != 0 {
		t.Fatalf("second job must not reprocess sessions: %s", statusJSON(empty))
	}
	if got := env.counter(t); got != 3 {
		t.Fatalf("fake exec counter after second job = %d, want 3", got)
	}
}

func TestReviewJobAcceptanceFailureRetry(t *testing.T) {
	env := newReviewEnv(t)
	for index := 0; index < 2; index++ {
		env.writeSession(t, index)
	}
	proposalPaths := env.prepareFixtures(t, 2)
	env.useFake(t, []string{"success", "tool-call"}, []string{proposalPaths[0], proposalPaths[1], proposalPaths[1]}, "")
	started := env.runStatusCommand(t, 2*time.Minute, "review", "start", "--project-id", env.projectID, "--agent-executable", fakeBin, "--json")
	failed := env.waitForStableStatus(t, 2*time.Minute, "failure of the first attempt", func(last reviewjob.PublicStatus) bool {
		return string(last.State) == "failed" && last.JobID == started.JobID
	})
	if failed.ErrorCode != "E_AGENT_TOOL_FORBIDDEN" {
		t.Fatalf("failed status error code = %q, want E_AGENT_TOOL_FORBIDDEN: %s", failed.ErrorCode, statusJSON(failed))
	}
	if failed.Attempt != 1 || failed.AcceptedPackets != 1 || failed.AcceptedSessions != 1 {
		t.Fatalf("failed status progress = (attempt %d, packets %d, sessions %d), want (1, 1, 1): %s", failed.Attempt, failed.AcceptedPackets, failed.AcceptedSessions, statusJSON(failed))
	}
	if !failed.CanRetry || failed.RetryExpectedAttempt != 1 || failed.RetryExpectedRevision <= 0 {
		t.Fatalf("failed status retry controls are invalid: %s", statusJSON(failed))
	}
	if !env.cursorExists(t, "session-acceptance-0") {
		t.Fatalf("cursor for the accepted session is missing")
	}
	if env.cursorExists(t, "session-acceptance-1") {
		t.Fatalf("cursor for the failed session must not exist")
	}
	if got := env.counter(t); got != 2 {
		t.Fatalf("fake exec counter after failure = %d, want 2", got)
	}
	retry := env.runStatusCommand(t, 2*time.Minute, "review", "retry", "--job-id", started.JobID, "--agent-executable", fakeBin, "--expected-attempt", "1", "--expected-revision", strconv.Itoa(failed.RetryExpectedRevision), "--json")
	if retry.JobID != started.JobID {
		t.Fatalf("retry must resume the same job: %s", statusJSON(retry))
	}
	completed := env.waitForStatus(t, 2*time.Minute, "completion of the retried review job", func(last reviewjob.PublicStatus) bool {
		return string(last.State) == "completed" && last.JobID == started.JobID
	})
	if completed.Attempt != 2 || completed.AcceptedSessions != 2 {
		t.Fatalf("retried status = (attempt %d, accepted %d), want (2, 2): %s", completed.Attempt, completed.AcceptedSessions, statusJSON(completed))
	}
	if got := env.counter(t); got != 3 {
		t.Fatalf("fake exec counter after retry = %d, want 3", got)
	}
	if !env.cursorExists(t, "session-acceptance-1") {
		t.Fatalf("cursor for the retried session is missing")
	}
}

func TestReviewJobAcceptanceCancel(t *testing.T) {
	env := newReviewEnv(t)
	env.writeSession(t, 0)
	proposalPaths := env.prepareFixtures(t, 1)
	readyPath := filepath.Join(env.base, "fake-ready")
	env.useFake(t, []string{"timeout"}, proposalPaths, readyPath)
	started := env.runStatusCommand(t, 2*time.Minute, "review", "start", "--project-id", env.projectID, "--agent-executable", fakeBin, "--json")
	waitForReady(t, readyPath, 2*time.Minute)
	env.runStatusCommand(t, 30*time.Second, "review", "cancel", "--job-id", started.JobID, "--json")
	cancelled := env.waitForStatus(t, 2*time.Minute, "cancellation of the review job", func(last reviewjob.PublicStatus) bool {
		return string(last.State) == "cancelled" && last.JobID == started.JobID
	})
	if cancelled.CanRetry {
		t.Fatalf("cancelled job must not be retryable: %s", statusJSON(cancelled))
	}
	if got := env.counter(t); got != 1 {
		t.Fatalf("fake exec counter after cancel = %d, want 1", got)
	}
	if env.cursorExists(t, "session-acceptance-0") {
		t.Fatalf("cancelled job must not accept a session cursor")
	}
}

func TestReviewJobAcceptanceRestartRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("worker kill-based recovery requires POSIX signals")
	}
	env := newReviewEnv(t)
	for index := 0; index < 2; index++ {
		env.writeSession(t, index)
	}
	proposalPaths := env.prepareFixtures(t, 2)
	readyPath := filepath.Join(env.base, "fake-ready")
	env.useFake(t, []string{"timeout"}, []string{proposalPaths[0], proposalPaths[0], proposalPaths[1]}, readyPath)
	started := env.runStatusCommand(t, 2*time.Minute, "review", "start", "--project-id", env.projectID, "--agent-executable", fakeBin, "--json")
	waitForReady(t, readyPath, 2*time.Minute)
	killReviewWorker(t, started.JobID)
	failed := env.waitForStatus(t, 2*time.Minute, "restart recovery of the interrupted review job", func(last reviewjob.PublicStatus) bool {
		return string(last.State) == "failed" && last.JobID == started.JobID
	})
	if failed.ErrorCode != "E_APPLY_RECOVERY" {
		t.Fatalf("recovered status error code = %q, want E_APPLY_RECOVERY: %s", failed.ErrorCode, statusJSON(failed))
	}
	if !failed.CanRetry || failed.RetryExpectedAttempt != failed.Attempt || failed.RetryExpectedRevision <= 0 {
		t.Fatalf("recovered status retry controls are invalid: %s", statusJSON(failed))
	}
	retry := env.runStatusCommand(t, 2*time.Minute, "review", "retry", "--job-id", started.JobID, "--agent-executable", fakeBin, "--expected-attempt", strconv.Itoa(failed.RetryExpectedAttempt), "--expected-revision", strconv.Itoa(failed.RetryExpectedRevision), "--json")
	completed := env.waitForStatus(t, 2*time.Minute, "completion of the recovered review job", func(last reviewjob.PublicStatus) bool {
		return string(last.State) == "completed" && last.JobID == retry.JobID
	})
	if completed.Attempt != 2 || completed.AcceptedSessions != 2 {
		t.Fatalf("recovered status = (attempt %d, accepted %d), want (2, 2): %s", completed.Attempt, completed.AcceptedSessions, statusJSON(completed))
	}
	if got := env.counter(t); got != 3 {
		t.Fatalf("fake exec counter after recovery = %d, want 3", got)
	}
}
