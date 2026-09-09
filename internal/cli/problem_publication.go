package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"time"

	"github.com/neomei/SessionReviewer/internal/candidatepublication"
	"github.com/neomei/SessionReviewer/internal/problemmap"
	"github.com/neomei/SessionReviewer/internal/publication"
	"github.com/neomei/SessionReviewer/internal/publicationlock"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

const problemPublicationNamespace = "problem-confirmation"

var problemAfterAcceptedPublication func() error
var problemBeforeMarkdownPublication func() error

func reconcileProblemPublications(ctx context.Context, dataRoot, projectID string) (retErr error) {
	_, mapping, _, err := resolveSyncMapping("", projectID, dataRoot)
	if err != nil {
		return err
	}
	owner, err := publicationlock.Acquire(dataRoot, projectID, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, owner.Release()) }()
	if err := publication.RecoverMarkdownLocked(ctx, publication.Options{ProjectID: projectID, Mapping: mapping, DataRoot: dataRoot, Now: time.Now}, owner); err != nil {
		return err
	}
	return reconcileProblemPublicationsLocked(dataRoot, projectID)
}

func reconcileProblemPublicationsLocked(dataRoot, projectID string) error {
	intents, err := candidatepublication.OpenStore(dataRoot, projectID, problemPublicationNamespace)
	if err != nil {
		return err
	}
	prepared, err := intents.Prepared()
	if err != nil || len(prepared) == 0 {
		return err
	}
	accepted, err := publicationstate.OpenReadOnly(dataRoot, projectID)
	if err != nil {
		return err
	}
	defer accepted.Close()
	candidates, err := problemmap.OpenStore(dataRoot, projectID)
	if err != nil {
		return err
	}
	for _, intent := range prepared {
		if intent.Namespace != problemPublicationNamespace || (intent.TerminalStatus != string(problemmap.CandidateApplied) && intent.TerminalStatus != string(problemmap.CandidateMerged)) {
			return errors.New("problem publication intent is invalid")
		}
		if _, err := accepted.AcceptedMarkdownResult(intent.ResultFingerprint); errors.Is(err, os.ErrNotExist) {
			if _, transitionErr := intents.Transition(intent.OperationID, intent.Revision, candidatepublication.StateAborted, time.Now()); transitionErr != nil {
				return transitionErr
			}
			continue
		} else if err != nil {
			return err
		}
		candidate, err := candidates.Get(intent.CandidateID)
		if err != nil {
			return err
		}
		if problemCandidatePublicationDigest(candidate) != intent.CandidateDigest {
			return errors.New("problem candidate identity changed after publication")
		}
		terminal := problemmap.CandidateStatus(intent.TerminalStatus)
		if candidate.Status != terminal {
			if candidate.Status != problemmap.CandidatePending && candidate.Status != problemmap.CandidateKeptPending && candidate.Status != problemmap.CandidateStale {
				return errors.New("problem candidate has a conflicting terminal state")
			}
			prior := candidate.Revision
			candidate.Status, candidate.Revision = terminal, prior+1
			candidate.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			if err := candidates.CompareAndSwap(candidate, prior); err != nil {
				return err
			}
		}
		if _, err := intents.Transition(intent.OperationID, intent.Revision, candidatepublication.StateCompleted, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

func problemCandidatePublicationDigest(candidate problemmap.Candidate) string {
	identity := struct {
		ProjectID         string                   `json:"project_id"`
		CandidateID       string                   `json:"candidate_id"`
		Question          string                   `json:"question"`
		SourceTurnRefs    []reviewv4.SourceTurnRef `json:"source_turn_refs"`
		DependencyDigests []string                 `json:"dependency_digests"`
	}{
		ProjectID: candidate.ProjectID, CandidateID: candidate.CandidateID, Question: candidate.Question,
		SourceTurnRefs: candidate.SourceTurnRefs, DependencyDigests: candidate.DependencyDigests,
	}
	body, err := strictjson.Encode(identity)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
