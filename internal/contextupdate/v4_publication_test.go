package contextupdate

import (
	"testing"

	"github.com/neomei/SessionReviewer/internal/memory"
)

func TestSameV4ConversationGraphRequiresExactCurrentAndRetainedBindings(t *testing.T) {
	current := memory.ConversationChainDependency{
		Provider: "codex", SessionID: "session-current", SessionViewDigest: digestForV4PublicationTest("1"), Digest: digestForV4PublicationTest("2"),
	}
	second := memory.ConversationChainDependency{
		Provider: "claude", SessionID: "session-second", SessionViewDigest: digestForV4PublicationTest("3"), Digest: digestForV4PublicationTest("4"),
	}
	retained := memory.ConversationChainDependency{
		Provider: "codex", SessionID: "session-retained", SessionViewDigest: digestForV4PublicationTest("5"), Digest: digestForV4PublicationTest("6"),
	}
	base := memory.GenerationManifest{
		ConversationChains:         []memory.ConversationChainDependency{current, second},
		RetainedConversationChains: []memory.ConversationChainDependency{retained},
	}

	reordered := base
	reordered.ConversationChains = []memory.ConversationChainDependency{second, current}
	if !sameV4ConversationGraph(base, reordered) {
		t.Fatal("equivalent dependency ordering should remain eligible for no-op publication")
	}

	changedCurrent := base
	changedCurrent.ConversationChains = append([]memory.ConversationChainDependency(nil), base.ConversationChains...)
	changedCurrent.ConversationChains[0].Digest = digestForV4PublicationTest("7")
	if sameV4ConversationGraph(base, changedCurrent) {
		t.Fatal("changed current conversation chain digest was treated as unchanged")
	}

	changedRetained := base
	changedRetained.RetainedConversationChains = append([]memory.ConversationChainDependency(nil), base.RetainedConversationChains...)
	changedRetained.RetainedConversationChains[0].SessionViewDigest = digestForV4PublicationTest("8")
	if sameV4ConversationGraph(base, changedRetained) {
		t.Fatal("changed retained SessionView binding was treated as unchanged")
	}

	movedToRetained := base
	movedToRetained.ConversationChains = []memory.ConversationChainDependency{second}
	movedToRetained.RetainedConversationChains = []memory.ConversationChainDependency{retained, current}
	if sameV4ConversationGraph(base, movedToRetained) {
		t.Fatal("moving a current dependency into retained history was treated as unchanged")
	}
}

func digestForV4PublicationTest(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}
