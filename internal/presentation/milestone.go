package presentation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
)

const (
	milestoneGeneratorFamily = "qualified-turn-v1"
	maxProjectedMilestones   = 65536
)

var (
	milestoneIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	milestoneDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type MilestoneSessionInput struct {
	View      memory.SessionView
	Chain     conversationchain.Document
	Revisions []memory.ObservationRevision
}

type MilestoneInput struct {
	ProjectID         string
	GenerationID      string
	ProjectViewDigest string
	Sessions          []MilestoneSessionInput
}

type MilestoneProjection struct {
	Timeline                  []reviewv4.Timeline
	ChainDependencies         []reviewv4.ChainDependency
	QualifyingFacts           uint64
	UnassignedQualifyingFacts uint64
}

type qualifiedMilestoneFact struct {
	category string
	priority int
	revision memory.ObservationRevision
}

type projectedMilestone struct {
	timeline   reviewv4.Timeline
	dependency reviewv4.ChainDependency
}

func ProjectMilestones(input MilestoneInput) (MilestoneProjection, error) {
	empty := MilestoneProjection{Timeline: []reviewv4.Timeline{}, ChainDependencies: []reviewv4.ChainDependency{}}
	if err := validateMilestoneMetadata(input); err != nil {
		return empty, err
	}
	if len(input.Sessions) > maxProjectedMilestones {
		return empty, errors.New("milestone input exceeds Session limit")
	}
	seenSessions := make(map[string]struct{}, len(input.Sessions))
	projected := make([]projectedMilestone, 0)
	var qualifyingFacts, unassignedFacts uint64
	for sessionIndex, session := range input.Sessions {
		sessionKey := session.View.Provider + "\x00" + session.View.SessionID
		if _, duplicate := seenSessions[sessionKey]; duplicate {
			return empty, errors.New("duplicate selected Session snapshot")
		}
		seenSessions[sessionKey] = struct{}{}
		if err := validateMilestoneSession(input.ProjectID, session); err != nil {
			return empty, fmt.Errorf("milestone Session %d: %w", sessionIndex, err)
		}
		byTurn, unassigned, facts, err := qualifyingFactsByTurn(session)
		if err != nil {
			return empty, fmt.Errorf("milestone Session %d: %w", sessionIndex, err)
		}
		if qualifyingFacts > ^uint64(0)-facts || unassignedFacts > ^uint64(0)-unassigned {
			return empty, errors.New("milestone fact count overflow")
		}
		qualifyingFacts += facts
		unassignedFacts += unassigned
		if len(projected)+len(byTurn) > maxProjectedMilestones {
			return empty, errors.New("milestone projection exceeds array limit")
		}
		if len(byTurn) == 0 {
			continue
		}
		dependency := milestoneChainDependency(session)
		revisionsByID := milestoneRevisionsByID(session.Revisions)
		for turnIndex, facts := range byTurn {
			timeline, err := projectMilestoneTurn(input, session, revisionsByID, turnIndex, facts)
			if err != nil {
				return empty, fmt.Errorf("project milestone turn %d: %w", turnIndex, err)
			}
			projected = append(projected, projectedMilestone{timeline: timeline, dependency: dependency})
		}
	}
	sort.Slice(projected, func(i, j int) bool {
		leftTime, _ := time.Parse(time.RFC3339Nano, projected[i].timeline.OccurredAt)
		rightTime, _ := time.Parse(time.RFC3339Nano, projected[j].timeline.OccurredAt)
		if !leftTime.Equal(rightTime) {
			return leftTime.Before(rightTime)
		}
		return projected[i].timeline.ID < projected[j].timeline.ID
	})
	result := MilestoneProjection{Timeline: make([]reviewv4.Timeline, 0, len(projected)), ChainDependencies: []reviewv4.ChainDependency{}, QualifyingFacts: qualifyingFacts, UnassignedQualifyingFacts: unassignedFacts}
	dependencies := make(map[string]reviewv4.ChainDependency)
	for _, item := range projected {
		result.Timeline = append(result.Timeline, item.timeline)
		key := item.dependency.Provider + "\x00" + item.dependency.SessionID + "\x00" + item.dependency.SessionViewDigest
		dependencies[key] = item.dependency
	}
	keys := make([]string, 0, len(dependencies))
	for key := range dependencies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result.ChainDependencies = append(result.ChainDependencies, dependencies[key])
	}
	if err := validateMilestoneProjection(input, result); err != nil {
		return empty, fmt.Errorf("validate milestone projection: %w", err)
	}
	return result, nil
}

func validateMilestoneMetadata(input MilestoneInput) error {
	if !validMilestoneID(input.ProjectID) || !validMilestoneID(input.GenerationID) || !milestoneDigestPattern.MatchString(input.ProjectViewDigest) {
		return errors.New("invalid milestone projection metadata")
	}
	return nil
}

func validMilestoneID(value string) bool {
	return len(value) <= 256 && milestoneIDPattern.MatchString(value)
}

func validateMilestoneSession(projectID string, session MilestoneSessionInput) error {
	if session.View.ProjectID != projectID || session.Chain.ProjectID != projectID {
		return errors.New("milestone Session belongs to another project")
	}
	if session.Chain.Digest != conversationchain.CanonicalDigest(session.Chain) {
		return errors.New("conversation chain self digest does not match canonical content")
	}
	if err := conversationchain.ValidateRetainedEvidence(session.Chain, session.View, session.Revisions); err != nil {
		return fmt.Errorf("authenticate retained evidence: %w", err)
	}
	return nil
}

func qualifyingFactsByTurn(session MilestoneSessionInput) (map[int][]qualifiedMilestoneFact, uint64, uint64, error) {
	retained := make(map[string]int)
	for turnIndex, turn := range session.Chain.TurnUnits {
		for _, action := range turn.Actions {
			if _, duplicate := retained[action.RevisionID]; duplicate {
				return nil, 0, 0, errors.New("duplicate retained fact revision")
			}
			retained[action.RevisionID] = turnIndex
		}
		for _, result := range turn.Results {
			if _, duplicate := retained[result.RevisionID]; duplicate {
				return nil, 0, 0, errors.New("duplicate retained fact revision")
			}
			retained[result.RevisionID] = turnIndex
		}
	}
	byTurn := make(map[int][]qualifiedMilestoneFact)
	var unassigned, total uint64
	for _, revision := range session.Revisions {
		fact, qualifies := qualifyMilestoneRevision(revision)
		if !qualifies {
			continue
		}
		total++
		turnIndex := milestoneTurnForOrdinal(session.Chain.TurnUnits, uint64(revision.Ref.Location.JSONL.Line))
		if turnIndex < 0 {
			if _, incorrectlyRetained := retained[revision.RevisionID]; incorrectlyRetained {
				return nil, 0, 0, errors.New("pre-user qualifying fact was attached to a turn")
			}
			unassigned++
			continue
		}
		retainedTurn, present := retained[revision.RevisionID]
		if !present || retainedTurn != turnIndex {
			return nil, 0, 0, errors.New("qualifying active revision is absent from its retained turn")
		}
		byTurn[turnIndex] = append(byTurn[turnIndex], fact)
	}
	return byTurn, unassigned, total, nil
}

func milestoneTurnForOrdinal(turns []conversationchain.TurnUnit, ordinal uint64) int {
	index := sort.Search(len(turns), func(index int) bool { return turns[index].UserMessage.SourceRef.RecordOrdinal > ordinal })
	return index - 1
}

func qualifyMilestoneRevision(revision memory.ObservationRevision) (qualifiedMilestoneFact, bool) {
	if failedMilestoneChange(revision.Outcome) {
		return qualifiedMilestoneFact{}, false
	}
	switch {
	case revision.Key.Kind == "verification" && revision.Operation == "verification" && milestoneVerificationPassed(revision):
		return qualifiedMilestoneFact{category: "verification", priority: 10, revision: revision}, true
	case revision.Key.Kind == "commit" && (revision.Operation == "commit" || revision.Operation == "commit_created"):
		return qualifiedMilestoneFact{category: "commit", priority: 20, revision: revision}, true
	case revision.Key.Kind == "version" && revision.Operation == "version":
		return qualifiedMilestoneFact{category: "version", priority: 30, revision: revision}, true
	case revision.Key.Kind == "deployment" && revision.Operation == "deployment":
		return qualifiedMilestoneFact{category: "deployment", priority: 40, revision: revision}, true
	case revision.Key.Kind == "release" && (revision.Operation == "release" || revision.Operation == "release_created" || revision.Operation == "release_published"):
		return qualifiedMilestoneFact{category: "release", priority: 50, revision: revision}, true
	default:
		return qualifiedMilestoneFact{}, false
	}
}

func failedMilestoneChange(outcome string) bool {
	switch strings.ToLower(strings.TrimSpace(outcome)) {
	case "failed", "failure", "error":
		return true
	default:
		return false
	}
}

func milestoneVerificationPassed(revision memory.ObservationRevision) bool {
	switch strings.ToLower(strings.TrimSpace(revision.Outcome)) {
	case "passed", "success":
	default:
		return false
	}
	if value, present := revision.Fields["exit_code"]; present {
		exitCode, err := strconv.ParseInt(value, 10, 64)
		if err != nil || strconv.FormatInt(exitCode, 10) != value || exitCode != 0 {
			return false
		}
	}
	if value, present := revision.Fields["failed"]; present && value != "0" && value != "false" {
		return false
	}
	if value, present := revision.Fields["passed"]; present && !milestonePositivePassedValue(value) {
		return false
	}
	return true
}

func milestonePositivePassedValue(value string) bool {
	if value == "true" {
		return true
	}
	count, err := strconv.ParseUint(value, 10, 64)
	return err == nil && count > 0 && strconv.FormatUint(count, 10) == value
}

func projectMilestoneTurn(input MilestoneInput, session MilestoneSessionInput, revisionsByID map[string]memory.ObservationRevision, turnIndex int, facts []qualifiedMilestoneFact) (reviewv4.Timeline, error) {
	if turnIndex < 0 || turnIndex >= len(session.Chain.TurnUnits) || len(facts) == 0 {
		return reviewv4.Timeline{}, errors.New("invalid qualifying turn")
	}
	strongest := facts[0]
	for _, fact := range facts[1:] {
		if fact.priority > strongest.priority || (fact.priority == strongest.priority && laterMilestoneFact(fact.revision, strongest.revision)) {
			strongest = fact
		}
	}
	turn := session.Chain.TurnUnits[turnIndex]
	ref := reviewv4.SourceTurnRef{Provider: session.View.Provider, SessionID: session.View.SessionID, TurnUnitID: turn.TurnUnitID, SessionViewDigest: session.View.Digest}
	return reviewv4.Timeline{ID: milestoneStableID(input.ProjectID, ref), GenerationID: input.GenerationID, OccurredAt: strongest.revision.Timestamp, Kind: "machine_" + strongest.category, Title: "Machine-observed " + strongest.category, Summary: renderMilestoneFact(strongest), DecisionIDs: []string{}, ClosedLoop: milestoneClosure(session, revisionsByID, turn, ref)}, nil
}

func milestoneRevisionsByID(revisions []memory.ObservationRevision) map[string]memory.ObservationRevision {
	byID := make(map[string]memory.ObservationRevision, len(revisions))
	for _, revision := range revisions {
		byID[revision.RevisionID] = revision
	}
	return byID
}

func laterMilestoneFact(left, right memory.ObservationRevision) bool {
	leftTime, _ := time.Parse(time.RFC3339Nano, left.Timestamp)
	rightTime, _ := time.Parse(time.RFC3339Nano, right.Timestamp)
	if !leftTime.Equal(rightTime) {
		return leftTime.After(rightTime)
	}
	if left.Key.Sequence != right.Key.Sequence {
		return left.Key.Sequence > right.Key.Sequence
	}
	return left.RevisionID > right.RevisionID
}

func milestoneStableID(projectID string, ref reviewv4.SourceTurnRef) string {
	preimage := strings.Join([]string{milestoneGeneratorFamily, projectID, ref.Provider, ref.SessionID, ref.TurnUnitID}, "\x00")
	digest := sha256.Sum256([]byte(preimage))
	return "milestone:" + hex.EncodeToString(digest[:])
}

func milestoneChainDependency(session MilestoneSessionInput) reviewv4.ChainDependency {
	turnIDs := make([]string, len(session.Chain.TurnUnits))
	for index, turn := range session.Chain.TurnUnits {
		turnIDs[index] = turn.TurnUnitID
	}
	return reviewv4.ChainDependency{Provider: session.View.Provider, SessionID: session.View.SessionID, SessionViewDigest: session.View.Digest, DependencyDigest: session.Chain.DependencyDigest, TurnUnitIDs: turnIDs}
}

func validateMilestoneProjection(input MilestoneInput, projection MilestoneProjection) error {
	capability := "0.4.0"
	if len(projection.Timeline) != 0 {
		capability = "0.4.3"
	}
	presentation := reviewv4.Presentation{SchemaVersion: 4, MinimumReaderVersion: capability, MinimumWriterVersion: capability, ProjectID: input.ProjectID, GenerationID: input.GenerationID, ProjectViewDigest: input.ProjectViewDigest, Timeline: projection.Timeline, Decisions: []reviewv4.Decision{}, Risks: []reviewv4.Risk{}, OpenLoops: []reviewv4.OpenLoop{}, ProblemRootIDs: []string{}, ProblemNodes: []reviewv4.ProblemNode{}, ChainDependencies: projection.ChainDependencies, HumanPatches: []reviewv4.Patch{}, OrphanPatches: []reviewv4.Patch{}, GeneratedBaselines: []reviewv4.Baseline{}}
	return reviewv4.ValidatePresentation(presentation)
}
