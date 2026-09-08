package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
)

const (
	conversationBodySource   = "source_full"
	conversationBodyRetained = "retained_excerpt"
	conversationEvidenceMax  = 64
)

type retainedConversation struct {
	document  conversationchain.Document
	view      memory.SessionView
	revisions []memory.ObservationRevision
}

func selectRetainedConversation(ctx context.Context, authenticated authenticatedSession, source memory.SourceRecord) (*retainedConversation, error) {
	var exact *memory.ConversationChainDependency
	for index := range authenticated.manifest.ConversationChains {
		dependency := &authenticated.manifest.ConversationChains[index]
		if dependency.Provider == authenticated.view.Provider && dependency.SessionID == authenticated.view.SessionID && dependency.SessionViewDigest == authenticated.view.Digest {
			if exact != nil {
				return nil, errors.New("duplicate current conversation chain")
			}
			exact = dependency
		}
	}
	if exact != nil {
		selected, err := loadRetainedConversation(ctx, authenticated, *exact, &authenticated.view, authenticated.revisions)
		if err != nil {
			return nil, err
		}
		return &selected, nil
	}
	if source.Availability == memory.SourceAvailable {
		return nil, nil
	}
	restored := source
	restored.Availability = memory.SourceAvailable
	restoredDigest, err := memory.Digest(restored)
	if err != nil {
		return nil, err
	}
	var match *retainedConversation
	for _, dependency := range authenticated.manifest.RetainedConversationChains {
		if dependency.Provider != authenticated.view.Provider || dependency.SessionID != authenticated.view.SessionID {
			continue
		}
		candidate, err := loadRetainedConversation(ctx, authenticated, dependency, nil, nil)
		if err != nil {
			return nil, err
		}
		if !retainedViewCompatible(authenticated.view, candidate.view, restoredDigest) {
			continue
		}
		if err := retainUniqueCandidate(&match, candidate); err != nil {
			return nil, err
		}
	}
	return match, nil
}

func retainedViewCompatible(current, candidate memory.SessionView, restoredSourceDigest string) bool {
	return candidate.Provider == current.Provider && candidate.SessionID == current.SessionID && candidate.SourceIdentity == current.SourceIdentity &&
		candidate.SourceRecordDigest == restoredSourceDigest && reflect.DeepEqual(candidate.ActiveRevisionIDs, current.ActiveRevisionIDs)
}

func retainUniqueCandidate(selected **retainedConversation, candidate retainedConversation) error {
	if *selected != nil {
		return publicError("retained_evidence_ambiguous", "retained conversation evidence is ambiguous")
	}
	copy := candidate
	*selected = &copy
	return nil
}

func loadRetainedConversation(ctx context.Context, authenticated authenticatedSession, dependency memory.ConversationChainDependency, knownView *memory.SessionView, knownRevisions []memory.ObservationRevision) (retainedConversation, error) {
	if err := inspectionCheckpoint(ctx, "conversation_chain"); err != nil {
		return retainedConversation{}, err
	}
	body, err := authenticated.store.LoadObjectContext(ctx, memorystore.ObjectConversationChain, dependency.Digest)
	if err != nil {
		return retainedConversation{}, err
	}
	document, err := conversationchain.Parse(body)
	if err != nil || document.Digest != dependency.Digest || document.ProjectID != authenticated.view.ProjectID || document.Provider != dependency.Provider || document.SessionID != dependency.SessionID || document.SessionViewDigest != dependency.SessionViewDigest {
		return retainedConversation{}, errors.New("conversation chain identity is invalid")
	}
	var view memory.SessionView
	var revisions []memory.ObservationRevision
	if knownView != nil {
		view = *knownView
		revisions = append([]memory.ObservationRevision(nil), knownRevisions...)
	} else {
		viewBody, err := authenticated.store.LoadObjectContext(ctx, memorystore.ObjectSessionView, dependency.SessionViewDigest)
		if err != nil {
			return retainedConversation{}, err
		}
		if err := json.Unmarshal(viewBody, &view); err != nil || memory.ValidateSessionView(view) != nil || view.Digest != dependency.SessionViewDigest || view.ProjectID != authenticated.view.ProjectID || view.Provider != dependency.Provider || view.SessionID != dependency.SessionID {
			return retainedConversation{}, errors.New("conversation evidence view is invalid")
		}
		revisions, err = loadSelectedRevisions(ctx, authenticated.store, view)
		if err != nil {
			return retainedConversation{}, err
		}
	}
	if err := conversationchain.ValidateRetainedEvidence(document, view, revisions); err != nil {
		return retainedConversation{}, err
	}
	return retainedConversation{document: document, view: view, revisions: revisions}, nil
}

func retainedVisibleConversation(selected retainedConversation) ([]conversationchain.VisibleTurn, conversationchain.VisibleCoverage, error) {
	coverage := conversationchain.VisibleCoverage{Complete: false}
	if source := selected.document.MaterializationCoverageV1; source != nil {
		available := true
		coverage = conversationchain.VisibleCoverage{
			SourceRecords: source.SourceRecords, VisibleMessages: source.VisibleMessages, CapturedMessages: source.CapturedMessages,
			TruncatedMessages: source.TruncatedMessages, TruncatedBodies: source.TruncatedBodies, ContextMessages: source.ContextMessages,
			OrphanMessages: source.OrphanMessages, OversizedRecords: source.OversizedRecords, MalformedRecords: source.MalformedRecords,
			Complete: source.Complete, DiagnosticsAvailable: &available,
		}
	} else {
		available := false
		coverage.DiagnosticsAvailable = &available
		coverage.VisibleMessages = selected.document.Coverage.SourceMessages
		coverage.CapturedMessages = selected.document.Coverage.CapturedMessages
		coverage.SourceRecords = selected.document.Coverage.SourceMessages
	}
	turns := make([]conversationchain.VisibleTurn, 0, len(selected.document.TurnUnits))
	for _, retained := range selected.document.TurnUnits {
		user := retainedVisibleMessage(retained.UserMessage)
		preview := user
		preview.Text = nil
		messages := []conversationchain.VisibleMessage{user}
		for _, message := range retained.AssistantMessages {
			messages = append(messages, retainedVisibleMessage(message))
		}
		turns = append(turns, conversationchain.VisibleTurn{
			TurnUnitID: retained.TurnUnitID, Ordinal: retained.Ordinal, StartedAt: retained.StartedAt, EndedAt: retained.EndedAt,
			UserMessage: preview, AnswerState: retained.AnswerState, AssistantMessageCount: uint64(len(retained.AssistantMessages)), Messages: messages,
			ActionCount: uint64(len(retained.Actions)), ResultCount: uint64(len(retained.Results)), Actions: append([]conversationchain.Action(nil), retained.Actions...), Results: append([]conversationchain.Result(nil), retained.Results...),
		})
	}
	return turns, coverage, nil
}

func mergeRetainedEvidence(turns []conversationchain.VisibleTurn, document conversationchain.Document) error {
	if len(turns) != len(document.TurnUnits) {
		return errors.New("source and retained turn counts differ")
	}
	for index := range turns {
		retained := document.TurnUnits[index]
		if turns[index].TurnUnitID != retained.TurnUnitID || turns[index].UserMessage.RevisionID != retained.UserMessage.RevisionID || turns[index].AssistantMessageCount != uint64(len(retained.AssistantMessages)) {
			return errors.New("source and retained turns differ")
		}
		for messageIndex, message := range retained.AssistantMessages {
			if turns[index].Messages[messageIndex+1].RevisionID != message.RevisionID {
				return errors.New("source and retained messages differ")
			}
		}
		turns[index].Actions = append([]conversationchain.Action(nil), retained.Actions...)
		turns[index].Results = append([]conversationchain.Result(nil), retained.Results...)
		turns[index].ActionCount = uint64(len(retained.Actions))
		turns[index].ResultCount = uint64(len(retained.Results))
	}
	return nil
}

func retainedVisibleMessage(message conversationchain.Message) conversationchain.VisibleMessage {
	return conversationchain.VisibleMessage{
		Role: message.Role, Phase: nil, RevisionID: message.RevisionID, SourceRef: message.SourceRef, OccurredAt: message.OccurredAt,
		VisibleExcerpt: message.VisibleExcerpt, Truncated: message.Truncated, Text: nil, TextTruncated: false,
	}
}

func validateConversationEvidenceItems(actions []conversationchain.Action, results []conversationchain.Result, provider, sessionID, sourceIdentity string) error {
	if len(actions) > conversationEvidenceMax || len(results) > conversationEvidenceMax {
		return publicError(CodeInvalidArgument, "conversation evidence exceeds its item limit")
	}
	seen := make(map[string]bool, len(actions)+len(results))
	for _, action := range actions {
		if !digestRE.MatchString(action.RevisionID) || !validID(action.Kind) || action.ToolName != nil && !validID(*action.ToolName) || len(action.Excerpt) > 4096 || !utf8.ValidString(action.Excerpt) || !validConversationSourceRef(action.SourceRef, provider, sessionID, sourceIdentity) {
			return publicError(CodeInvalidArgument, "conversation action is invalid")
		}
		if seen[action.RevisionID] {
			return publicError(CodeInvalidArgument, "conversation evidence revision is duplicated")
		}
		seen[action.RevisionID] = true
	}
	for _, result := range results {
		if !digestRE.MatchString(result.RevisionID) || !validID(result.Kind) || len(result.Excerpt) > 4096 || !utf8.ValidString(result.Excerpt) || !validConversationSourceRef(result.SourceRef, provider, sessionID, sourceIdentity) {
			return publicError(CodeInvalidArgument, "conversation result is invalid")
		}
		switch result.VerificationState {
		case "unknown", "passed", "failed", "partial":
		default:
			return publicError(CodeInvalidArgument, "conversation result verification state is invalid")
		}
		if seen[result.RevisionID] {
			return publicError(CodeInvalidArgument, "conversation evidence revision is duplicated")
		}
		seen[result.RevisionID] = true
	}
	return nil
}

func validConversationSourceRef(ref conversationchain.SourceRef, provider, sessionID, sourceIdentity string) bool {
	return ref.Provider == provider && ref.SessionID == sessionID && ref.SourceIdentity == sourceIdentity && validID(ref.SourceIdentity) && ref.RecordOrdinal > 0 && ref.RecordOrdinal <= maxWireInteger && digestRE.MatchString("sha256:"+ref.SourceHash)
}
