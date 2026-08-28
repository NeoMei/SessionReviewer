package reviewjob

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/agent"
	"github.com/neomei/SessionReviewer/internal/apply"
	"github.com/neomei/SessionReviewer/internal/evidence"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

func TestAgentHandleHasNoExportedForgeableState(t *testing.T) {
	typeOfHandle := reflect.TypeOf(AgentHandle{})
	for index := 0; index < typeOfHandle.NumField(); index++ {
		field := typeOfHandle.Field(index)
		if field.IsExported() {
			t.Fatalf("AgentHandle field %q is exported and structurally forgeable", field.Name)
		}
	}
	if _, err := (&AgentHandle{}).VerifiedAgent(); err == nil {
		t.Fatal("zero caller-constructible AgentHandle was accepted")
	}
}

func TestVerifyAgentCodex0147CannotProduceHandle(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "codex")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	command := exec.Command("go", "build", "-o", executable, "../agent/codex/testdata/fake-agent.go")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fixture Codex: %v: %s", err, output)
	}
	t.Setenv("SESSIONREVIEWER_FAKE_MODE", "success")
	handle, err := VerifyAgent(t.Context(), "codex", executable)
	if handle != nil {
		t.Fatal("Codex 0.147 produced a production AgentHandle")
	}
	code, ok := agent.CodeOf(err)
	if !ok || code != agent.CodeIncompatible {
		t.Fatalf("VerifyAgent() error=%v code=%q found=%v", err, code, ok)
	}
}

func TestWorkerRechecksAgentExecutableImmediatelyBeforeGenerate(t *testing.T) {
	hash := strings.Repeat("3", 64)
	fixture := newWorkerFixture(t, []FrozenSession{{
		SessionID: "session-s1", StartedAt: fixtureTime(7),
		Upper: evidence.CursorBoundary{Line: 1, SourceHash: hash},
	}})
	packet := workerPacket(fixture.job.ProjectID, "session-s1", 1, 1, "", hash, false, "ev-000000000018")
	packet.CWD = "/repo"
	accepted := workerInitialAccepted(fixture.job.ProjectID)
	adapter := &verifiedWorkerAgent{
		capability: workerCapability("fixture", "1.0.0"),
		generate:   func(agent.Request) (agent.Result, error) { panic("replacement executable reached GenerateProposal") },
	}
	applyCalls, syncCalls := 0, 0
	options := workerRunOptions(
		fixture,
		func(context.Context, PrepareRequest) (Prepared, error) {
			if err := os.Rename(fixture.agent, fixture.agent+".original"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.agent, []byte("replacement fixture Agent\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			return workerPrepared(t, packet, accepted.snapshot(t)), nil
		},
		adapter,
		func(context.Context, ApplyRequest) (apply.Result, error) {
			applyCalls++
			return apply.Result{}, errors.New("unexpected apply")
		},
		func(context.Context, syncproject.Options) (syncengine.Report, error) {
			syncCalls++
			return syncengine.Report{}, errors.New("unexpected sync")
		},
		nil,
	)
	if err := Run(t.Context(), options); err == nil {
		t.Fatal("Run() accepted executable replacement after handle validation")
	}
	job, _, found, err := fixture.store.Load(fixture.job.ID)
	if err != nil || !found || job.Error.Code != AgentIncompatible || adapter.generateCalls != 0 || applyCalls != 0 || syncCalls != 0 {
		t.Fatalf("replacement job=%#v found=%v err=%v generate=%d apply=%d sync=%d", job, found, err, adapter.generateCalls, applyCalls, syncCalls)
	}
}
