package publication

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

// VerifyPrivateChainBindings authenticates every public chain dependency
// against an immutable current or retained root in one selected manifest.
func VerifyPrivateChainBindings(ctx context.Context, store *memorystore.Store, manifest memory.GenerationManifest, accepted reviewv4.Accepted) error {
	if ctx == nil || store == nil {
		return errors.New("private conversation chain store is required")
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	roots := make(map[string]memory.ConversationChainDependency, len(manifest.ConversationChains)+len(manifest.RetainedConversationChains))
	for _, root := range append(append([]memory.ConversationChainDependency(nil), manifest.ConversationChains...), manifest.RetainedConversationChains...) {
		roots[privateChainKey(root.Provider, root.SessionID, root.SessionViewDigest)] = root
	}
	for _, dependency := range accepted.Review.ChainDependencies {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		root, exists := roots[privateChainKey(dependency.Provider, dependency.SessionID, dependency.SessionViewDigest)]
		if !exists {
			return errors.New("public conversation chain dependency is absent from the selected private manifest roots")
		}
		body, err := store.LoadObjectContext(ctx, memorystore.ObjectConversationChain, root.Digest)
		if err != nil {
			return fmt.Errorf("load rooted private conversation chain: %w", err)
		}
		chain, err := conversationchain.Parse(body)
		if err != nil {
			return fmt.Errorf("parse rooted private conversation chain: %w", err)
		}
		turnIDs := make([]string, len(chain.TurnUnits))
		for index, turn := range chain.TurnUnits {
			turnIDs[index] = turn.TurnUnitID
		}
		if chain.ProjectID != manifest.ProjectID || chain.Provider != dependency.Provider || chain.SessionID != dependency.SessionID || chain.SessionViewDigest != dependency.SessionViewDigest || chain.DependencyDigest != dependency.DependencyDigest || !slices.Equal(turnIDs, dependency.TurnUnitIDs) {
			return errors.New("public conversation chain dependency differs from its rooted private chain")
		}
	}
	return nil
}

func privateChainKey(provider, sessionID, viewDigest string) string {
	return provider + "\x00" + sessionID + "\x00" + viewDigest
}
