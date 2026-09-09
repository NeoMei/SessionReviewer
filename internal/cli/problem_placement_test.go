package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/agent"
	"github.com/neomei/SessionReviewer/internal/problemmap"
)

const cliPlacementToken = "0123456789abcdef0123456789abcdef"

type fakePlacementAgent struct {
	proposal []byte
	starts   int
	cancels  int
	request  agent.Request
}

func (fake *fakePlacementAgent) GenerateProposal(_ context.Context, request agent.Request) (agent.Result, error) {
	fake.starts++
	fake.request = request
	return agent.Result{Proposal: append([]byte(nil), fake.proposal...)}, nil
}
func (fake *fakePlacementAgent) Cancel(context.Context) error { fake.cancels++; return nil }

func TestProblemPlacementRequestRunStatusAndCacheWithoutFormalWrite(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	accepted, err := readProblemProjection(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	candidate := problemmap.NewHumanCandidate(fixture.projectID, "Where does this question belong?", time.Now().UTC())
	store, _ := problemmap.OpenStore(fixture.data, fixture.projectID)
	if err := store.CompareAndSwap(candidate, 0); err != nil {
		t.Fatal(err)
	}
	agentFake := &fakePlacementAgent{proposal: []byte(`{"schema_version":1,"contract":"problem-placement-proposal-v1","recommended_relation":"keep_pending","recommended_target_id":null,"alternate_target_ids":[],"related_node_ids":[],"grounds":[{"explanation":"No reliable hierarchy is present.","matched_problem_ids":[]}],"confidence":"low"}`)}
	originalNow, originalLoad, originalLaunch, originalExecutable := problemPlacementNow, problemPlacementLoadAgent, problemPlacementLaunch, problemPlacementExecutable
	t.Cleanup(func() {
		problemPlacementNow, problemPlacementLoadAgent, problemPlacementLaunch, problemPlacementExecutable = originalNow, originalLoad, originalLaunch, originalExecutable
	})
	clock := time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC)
	problemPlacementNow = func() time.Time { clock = clock.Add(time.Second); return clock }
	problemPlacementLoadAgent = func(context.Context, string) (placementAgent, error) { return agentFake, nil }
	problemPlacementExecutable = func() (string, error) { return "/session-reviewer", nil }
	problemPlacementLaunch = func(request problemPlacementLaunchRequest) error {
		return executeProblemPlacement(context.Background(), request.DataRoot, request.ProjectID, request.JobID, request.Token, agentFake, nil)
	}
	request := ProblemRequest{Command: "placement", Subcommand: "request", DataDir: fixture.data, ProjectID: fixture.projectID, CandidateID: candidate.CandidateID, ExpectedCandidateRevision: candidate.Revision, ExpectedProblemMapRevision: accepted.Review.ProblemMapRevision, ExpectedGenerationID: accepted.Review.GenerationID}
	first, err := runProblemPlacement(request)
	if err != nil || first.State != problemmap.PlacementCompleted || agentFake.starts != 1 {
		t.Fatalf("first=%+v starts=%d err=%v", first, agentFake.starts, err)
	}
	if agentFake.request.ProposalContract != agent.ProposalContractGenericJSON {
		t.Fatalf("proposal contract=%q", agentFake.request.ProposalContract)
	}
	repeat, err := runProblemPlacement(request)
	if err != nil || repeat.JobID != first.JobID || agentFake.starts != 1 {
		t.Fatalf("repeat=%+v starts=%d err=%v", repeat, agentFake.starts, err)
	}
	after, err := readProblemProjection(fixture.project)
	if err != nil || after.Review.ProblemMapRevision != accepted.Review.ProblemMapRevision || len(after.Review.ProblemNodes) != len(accepted.Review.ProblemNodes) {
		t.Fatalf("formal graph changed: %+v err=%v", after.Review.ProblemNodes, err)
	}
	result, err := store.Get(candidate.CandidateID)
	if err != nil || result.AnalysisMode != problemmap.AnalysisAgentRequested || result.AgentRunID == nil || *result.AgentRunID != first.JobID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestProblemPlacementDisabledAndCancelAreActionable(t *testing.T) {
	fixture := newCLIAuthenticatedMarkdownFixture(t)
	accepted, _ := readProblemProjection(fixture.project)
	candidate := problemmap.NewHumanCandidate(fixture.projectID, "Ambiguous question?", time.Now().UTC())
	store, _ := problemmap.OpenStore(fixture.data, fixture.projectID)
	_ = store.CompareAndSwap(candidate, 0)
	originalLoad := problemPlacementLoadAgent
	t.Cleanup(func() { problemPlacementLoadAgent = originalLoad })
	problemPlacementLoadAgent = func(context.Context, string) (placementAgent, error) {
		return nil, agent.NewError(agent.CodeUnconfigured, errors.New("missing"))
	}
	_, err := runProblemPlacement(ProblemRequest{Command: "placement", Subcommand: "request", DataDir: fixture.data, ProjectID: fixture.projectID, CandidateID: candidate.CandidateID, ExpectedCandidateRevision: 1, ExpectedProblemMapRevision: accepted.Review.ProblemMapRevision, ExpectedGenerationID: accepted.Review.GenerationID})
	if code, ok := agent.CodeOf(err); !ok || code != agent.CodeUnconfigured {
		t.Fatalf("disabled err=%v", err)
	}
	jobs, _ := problemmap.OpenPlacementJobStore(fixture.data, fixture.projectID)
	job, _, err := jobs.Reserve(candidate, 1, accepted.Review.ProblemMapRevision, accepted.Review.GenerationID, cliPlacementToken, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	status, err := runProblemPlacement(ProblemRequest{Command: "placement", Subcommand: "cancel", DataDir: fixture.data, ProjectID: fixture.projectID, JobID: job.JobID, ExpectedRevision: job.Revision})
	if err != nil || status.State != problemmap.PlacementCancelled || status.CanCancel {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestProblemPlacementCandidateStatusReturnsNullWhenNoActiveJob(t *testing.T) {
	dataRoot := t.TempDir()
	var stdout, stderr bytes.Buffer
	exit := runProblems([]string{"placement", "status", "--project-id", "project-a", "--candidate-id", "candidate-a", "--data-dir", dataRoot, "--json"}, bytes.NewReader(nil), &stdout, &stderr)
	if exit != 0 || stdout.String() != "null\n" || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}
