package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/source"
)

const (
	conversationRuleVersion      = "visible-turn-v1"
	conversationRedactionVersion = "redaction-v1"
)

func materializeConversation(ctx context.Context, adapter source.Adapter, store MemoryStore, terminal terminalSource, view memory.SessionView, current []memory.ObservationRevision) (*conversationchain.Document, error) {
	if terminal.record.Availability != memory.SourceAvailable {
		return nil, nil
	}
	reader, supported := adapter.(source.VisibleReader)
	if !supported {
		return nil, &source.UnsupportedCapabilityError{Provider: terminal.record.Provider}
	}
	messages, coverage, err := reader.ReadVisiblePrefix(ctx, terminal.record)
	if err != nil {
		return nil, fmt.Errorf("read visible source prefix: %w", err)
	}
	revisions, err := activeConversationRevisions(ctx, store, view, current)
	if err != nil {
		return nil, err
	}
	document, _, err := conversationchain.Materialize(conversationchain.MaterializeInput{
		View: view, Messages: messages, Revisions: revisions, SourceCoverage: coverage,
		RuleVersion: conversationRuleVersion, RedactionVersion: conversationRedactionVersion,
	})
	// Drop full source bodies before the next Session is processed.
	for index := range messages {
		messages[index].Text = ""
	}
	if err != nil {
		return nil, fmt.Errorf("materialize retained conversation: %w", err)
	}
	return &document, nil
}

func activeConversationRevisions(ctx context.Context, store MemoryStore, view memory.SessionView, current []memory.ObservationRevision) ([]memory.ObservationRevision, error) {
	wanted := make(map[string]struct{}, len(view.ActiveRevisionIDs))
	for _, revisionID := range view.ActiveRevisionIDs {
		wanted[revisionID] = struct{}{}
	}
	selected := make(map[string]memory.ObservationRevision, len(wanted))
	accept := func(revision memory.ObservationRevision) error {
		if _, active := wanted[revision.RevisionID]; !active {
			return nil
		}
		if memory.ObservationRevisionID(revision) != revision.RevisionID {
			return errors.New("active observation revision digest mismatch")
		}
		if prior, duplicate := selected[revision.RevisionID]; duplicate && !equalJSON(prior, revision) {
			return errors.New("active observation revision has conflicting bodies")
		}
		selected[revision.RevisionID] = revision
		return nil
	}
	for _, revision := range current {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := accept(revision); err != nil {
			return nil, err
		}
	}
	for _, digest := range view.ObservationChunkDigests {
		if len(selected) == len(wanted) {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, err := store.LoadObject(memorystore.ObjectObservationChunk, digest)
		if err != nil {
			// The planned newest chunk is not in CAS yet; its active records were
			// supplied through current above.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		revisions, err := decodeObservationChunk(body)
		if err != nil {
			return nil, err
		}
		for _, revision := range revisions {
			if err := accept(revision); err != nil {
				return nil, err
			}
		}
	}
	if len(selected) != len(wanted) {
		return nil, errors.New("active conversation revision is absent from SessionView chunks")
	}
	result := make([]memory.ObservationRevision, 0, len(selected))
	for _, revision := range selected {
		result = append(result, revision)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RevisionID < result[j].RevisionID })
	return result, nil
}

func reconcileConversationChains(previous baseline, manifest *memory.GenerationManifest) {
	current := manifest.ConversationChains
	currentTuples := make(map[string]struct{}, len(current))
	currentIdentities := make(map[string]struct{}, len(current))
	for _, dependency := range current {
		currentTuples[sourceKey(dependency.Provider, dependency.SessionID)+"\x00"+dependency.SessionViewDigest] = struct{}{}
		currentIdentities[sourceKey(dependency.Provider, dependency.SessionID)] = struct{}{}
	}
	cumulativeViews := make(map[string]struct{}, len(manifest.SessionViews)+len(manifest.RetainedSessionViews))
	for _, dependency := range append(append([]memory.SessionViewDependency(nil), manifest.SessionViews...), manifest.RetainedSessionViews...) {
		cumulativeViews[sourceKey(dependency.Provider, dependency.SessionID)+"\x00"+dependency.Digest] = struct{}{}
	}
	seen := make(map[string]struct{})
	retained := make([]memory.ConversationChainDependency, 0, len(previous.manifest.ConversationChains)+len(previous.manifest.RetainedConversationChains))
	for _, dependency := range append(append([]memory.ConversationChainDependency(nil), previous.manifest.ConversationChains...), previous.manifest.RetainedConversationChains...) {
		tuple := sourceKey(dependency.Provider, dependency.SessionID) + "\x00" + dependency.SessionViewDigest
		if _, duplicate := currentTuples[tuple]; duplicate {
			continue
		}
		if _, duplicate := seen[tuple]; duplicate {
			continue
		}
		seen[tuple] = struct{}{}
		identity := sourceKey(dependency.Provider, dependency.SessionID)
		if _, cumulative := cumulativeViews[tuple]; cumulative {
			if _, alreadyCurrent := currentIdentities[identity]; !alreadyCurrent {
				current = append(current, dependency)
				currentIdentities[identity] = struct{}{}
			}
			continue
		}
		retained = append(retained, dependency)
	}
	sort.Slice(current, func(i, j int) bool {
		return sourceKey(current[i].Provider, current[i].SessionID) < sourceKey(current[j].Provider, current[j].SessionID)
	})
	sort.Slice(retained, func(i, j int) bool {
		left, right := retained[i], retained[j]
		return sourceKey(left.Provider, left.SessionID)+"\x00"+left.SessionViewDigest < sourceKey(right.Provider, right.SessionID)+"\x00"+right.SessionViewDigest
	})
	manifest.ConversationChains = current
	manifest.RetainedConversationChains = retained
}
