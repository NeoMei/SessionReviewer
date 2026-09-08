package source

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
)

type visibleManagerAdapter struct {
	*managerFakeAdapter
	messages []conversationchain.SourceMessage
	coverage conversationchain.VisibleCoverage
	records  []memory.SourceRecord
}

func visibleManagerRecord(provider string) memory.SourceRecord {
	return memory.SourceRecord{
		SchemaVersion: memory.MemorySchemaVersion, Provider: provider, SessionID: "session-1", SourceIdentity: "source-1",
		StartedAt: "2026-09-08T00:00:00Z", EndedAt: "2026-09-08T00:00:00Z",
		FrozenBoundary: memory.FrozenBoundary{Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 1, ByteOffset: 1}}, SourceHash: strings.Repeat("a", 64)},
		Availability:   memory.SourceAvailable, Usage: accounting.SessionUsage{StartedAt: "2026-09-08T00:00:00Z", EndedAt: "2026-09-08T00:00:00Z", Models: []accounting.ModelUsage{}}, ProjectIDs: []string{"project-1"},
	}
}

func (adapter *visibleManagerAdapter) ReadVisiblePrefix(_ context.Context, record memory.SourceRecord) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage, error) {
	adapter.records = append(adapter.records, record)
	return append([]conversationchain.SourceMessage(nil), adapter.messages...), adapter.coverage, nil
}

func TestManagerReadVisiblePrefixDispatchesOnlyToRecordProvider(t *testing.T) {
	codex := &visibleManagerAdapter{managerFakeAdapter: &managerFakeAdapter{}, messages: []conversationchain.SourceMessage{{Role: conversationchain.RoleUser, Text: "question", RecordOrdinal: 1}}}
	claude := &visibleManagerAdapter{managerFakeAdapter: &managerFakeAdapter{}}
	manager, err := NewManager([]NamedAdapter{{Provider: "claude", Adapter: claude}, {Provider: "codex", Adapter: codex}})
	if err != nil {
		t.Fatal(err)
	}
	record := visibleManagerRecord("codex")
	messages, _, err := manager.ReadVisiblePrefix(context.Background(), record)
	if err != nil || len(messages) != 1 || !reflect.DeepEqual(codex.records, []memory.SourceRecord{record}) || len(claude.records) != 0 {
		t.Fatalf("visible dispatch messages=%+v codex=%+v claude=%+v err=%v", messages, codex.records, claude.records, err)
	}
}

func TestManagerReadVisiblePrefixFailsClosedForUnsupportedAndSpoofedProvider(t *testing.T) {
	plain := &managerFakeAdapter{}
	manager, err := NewManager([]NamedAdapter{{Provider: "codex", Adapter: plain}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.ReadVisiblePrefix(context.Background(), visibleManagerRecord("codex")); !errors.Is(err, ErrVisibleReaderUnsupported) {
		t.Fatalf("unsupported capability error=%v", err)
	}
	if _, _, err := manager.ReadVisiblePrefix(context.Background(), visibleManagerRecord("claude")); err == nil || errors.Is(err, ErrVisibleReaderUnsupported) {
		t.Fatalf("unregistered provider did not fail before capability fallback: %v", err)
	}
	if len(plain.readRefs) != 0 || len(plain.abandonedCandidates) != 0 || len(plain.abandonedBoundaries) != 0 {
		t.Fatalf("visible dispatch touched unrelated lifecycle: %+v", plain)
	}
}
