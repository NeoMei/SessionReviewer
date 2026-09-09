package problemmap

import (
	"context"
	"errors"
	"time"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

// ReconcileManifestCandidates derives private candidates from the exact
// conversation roots of a generation that has just been published.
func ReconcileManifestCandidates(ctx context.Context, dataRoot, projectID string, store *memorystore.Store, manifest memory.GenerationManifest, graph []reviewv4.ProblemNode, now time.Time) error {
	if ctx == nil || store == nil || manifest.ProjectID != projectID {
		return errors.New("problem candidate generation identity is invalid")
	}
	chains := make([]ChainInput, 0, len(manifest.ConversationChains))
	for _, dependency := range manifest.ConversationChains {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		body, err := store.LoadObjectContext(ctx, memorystore.ObjectConversationChain, dependency.Digest)
		if err != nil {
			return err
		}
		document, err := conversationchain.Parse(body)
		if err != nil {
			return err
		}
		if document.ProjectID != projectID || document.Provider != dependency.Provider || document.SessionID != dependency.SessionID || document.SessionViewDigest != dependency.SessionViewDigest || document.Digest != dependency.Digest {
			return errors.New("conversation chain dependency binding mismatch")
		}
		chains = append(chains, ChainInput{Document: document, Digest: dependency.Digest})
	}
	candidates := DiscoverCandidates(projectID, chains, graph, now)
	candidateStore, err := OpenStore(dataRoot, projectID)
	if err != nil {
		return err
	}
	return candidateStore.ReconcileDeterministic(candidates, now)
}
