package contextupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/presentation"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

func loadScanMilestones(ctx context.Context, store *memorystore.Store, manifest memory.GenerationManifest) (presentation.MilestoneProjection, error) {
	empty := presentation.MilestoneProjection{Timeline: []reviewv4.Timeline{}, ChainDependencies: []reviewv4.ChainDependency{}}
	if ctx == nil || store == nil {
		return empty, errors.New("milestone loader requires context and private store")
	}
	if cause := context.Cause(ctx); cause != nil {
		return empty, cause
	}
	views := make(map[string]memory.SessionViewDependency, len(manifest.SessionViews)+len(manifest.RetainedSessionViews))
	for _, dependency := range append(append([]memory.SessionViewDependency(nil), manifest.SessionViews...), manifest.RetainedSessionViews...) {
		views[dependency.Provider+"\x00"+dependency.SessionID+"\x00"+dependency.Digest] = dependency
	}
	sessions := make([]presentation.MilestoneSessionInput, 0, len(manifest.ConversationChains))
	for _, root := range manifest.ConversationChains {
		if cause := context.Cause(ctx); cause != nil {
			return empty, cause
		}
		key := root.Provider + "\x00" + root.SessionID + "\x00" + root.SessionViewDigest
		if _, exists := views[key]; !exists {
			return empty, errors.New("current conversation chain has no exact SessionView root")
		}
		viewBody, err := store.LoadObjectContext(ctx, memorystore.ObjectSessionView, root.SessionViewDigest)
		if err != nil {
			return empty, fmt.Errorf("load milestone SessionView %s/%s: %w", root.Provider, root.SessionID, err)
		}
		var view memory.SessionView
		if err := json.Unmarshal(viewBody, &view); err != nil {
			return empty, fmt.Errorf("decode milestone SessionView %s/%s: %w", root.Provider, root.SessionID, err)
		}
		if view.Provider != root.Provider || view.SessionID != root.SessionID || view.Digest != root.SessionViewDigest {
			return empty, errors.New("milestone SessionView differs from manifest root")
		}
		chainBody, err := store.LoadObjectContext(ctx, memorystore.ObjectConversationChain, root.Digest)
		if err != nil {
			return empty, fmt.Errorf("load milestone conversation chain %s/%s: %w", root.Provider, root.SessionID, err)
		}
		chain, err := conversationchain.Parse(chainBody)
		if err != nil {
			return empty, fmt.Errorf("decode milestone conversation chain %s/%s: %w", root.Provider, root.SessionID, err)
		}
		if chain.Provider != root.Provider || chain.SessionID != root.SessionID || chain.SessionViewDigest != root.SessionViewDigest || chain.Digest != root.Digest {
			return empty, errors.New("milestone conversation chain differs from manifest root")
		}
		revisions, err := loadMilestoneRevisions(ctx, store, view)
		if err != nil {
			return empty, fmt.Errorf("load milestone revisions %s/%s: %w", root.Provider, root.SessionID, err)
		}
		sessions = append(sessions, presentation.MilestoneSessionInput{View: view, Chain: chain, Revisions: revisions})
	}
	return presentation.ProjectMilestones(presentation.MilestoneInput{ProjectID: manifest.ProjectID, GenerationID: manifest.GenerationID, ProjectViewDigest: manifest.ProjectViewDigest, Sessions: sessions})
}

func loadMilestoneRevisions(ctx context.Context, store *memorystore.Store, view memory.SessionView) ([]memory.ObservationRevision, error) {
	wanted := make(map[string]struct{}, len(view.ActiveRevisionIDs))
	for _, revisionID := range view.ActiveRevisionIDs {
		wanted[revisionID] = struct{}{}
	}
	found := make(map[string]memory.ObservationRevision, len(wanted))
	for _, digest := range view.ObservationChunkDigests {
		revisions, err := store.LoadObservationChunkContext(ctx, digest)
		if err != nil {
			return nil, err
		}
		for _, revision := range revisions {
			if _, active := wanted[revision.RevisionID]; !active {
				continue
			}
			if previous, duplicate := found[revision.RevisionID]; duplicate {
				if !reflect.DeepEqual(previous, revision) {
					return nil, errors.New("active milestone revision has conflicting immutable bodies")
				}
				continue
			}
			found[revision.RevisionID] = revision
		}
	}
	if len(found) != len(wanted) {
		return nil, errors.New("active milestone revision is absent from SessionView chunks")
	}
	result := make([]memory.ObservationRevision, 0, len(found))
	for _, revision := range found {
		result = append(result, revision)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RevisionID < result[j].RevisionID })
	return result, nil
}
