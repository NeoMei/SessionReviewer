package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

type diagramNode struct {
	kind  string
	id    string
	label string
}

type diagramEdge struct {
	from string
	to   string
}

// RenderDiagram is the cycle-free bridge used by internal/diagram. Ledger
// rendering calls the same pure implementation directly.
func RenderDiagram(state State) ([]byte, error) {
	return renderDiagram(state)
}

func renderDiagram(state State) ([]byte, error) {
	if err := validateDiagramState(state); err != nil {
		return nil, err
	}

	timeline := append([]TimelineEvent(nil), state.Timeline...)
	sort.Slice(timeline, func(i, j int) bool {
		if timeline[i].OccurredAt != timeline[j].OccurredAt {
			return timeline[i].OccurredAt < timeline[j].OccurredAt
		}
		return timeline[i].ID < timeline[j].ID
	})
	decisions := sortedDiagramDecisions(state.Decisions)
	loops := sortedDiagramLoops(state.OpenLoops)
	sessions := sortedDiagramSessions(state.Sessions)

	causalNodes := make([]diagramNode, 0, len(timeline)+len(decisions)+len(loops))
	for _, event := range timeline {
		label := event.Title
		if event.OccurredAt != "" {
			label = event.OccurredAt + " · " + label
		}
		causalNodes = append(causalNodes, diagramNode{kind: "event", id: event.ID, label: label})
	}
	for _, decision := range decisions {
		causalNodes = append(causalNodes, diagramNode{kind: "decision", id: decision.ID, label: entityDiagramLabel("Decision", decision.Title, decision.Status, decision.Tags)})
	}
	for _, loop := range loops {
		causalNodes = append(causalNodes, diagramNode{kind: "loop", id: loop.ID, label: entityDiagramLabel("Loop", loop.Title, loop.Status, loop.Tags)})
	}

	var causalEdges []diagramEdge
	for i := 1; i < len(timeline); i++ {
		causalEdges = append(causalEdges, diagramEdge{from: nodeID("event", timeline[i-1].ID), to: nodeID("event", timeline[i].ID)})
	}
	for _, event := range timeline {
		from := nodeID("event", event.ID)
		for _, id := range sortedUniqueStrings(event.DecisionIDs) {
			causalEdges = append(causalEdges, diagramEdge{from: from, to: nodeID("decision", id)})
		}
		for _, id := range sortedUniqueStrings(event.OpenLoopIDs) {
			causalEdges = append(causalEdges, diagramEdge{from: from, to: nodeID("loop", id)})
		}
	}

	relationshipNodes := make([]diagramNode, 0, 1+len(sessions)+len(decisions)+3*len(loops)+len(state.CurrentState.Blockers))
	if state.ProjectID != "" {
		projectLabel := "Project: " + state.ProjectID
		if state.CurrentState.Goal != "" {
			projectLabel += " · goal: " + state.CurrentState.Goal
		}
		relationshipNodes = append(relationshipNodes, diagramNode{kind: "project", id: state.ProjectID, label: projectLabel})
	}
	for _, session := range sessions {
		relationshipNodes = append(relationshipNodes, diagramNode{kind: "session", id: session.ID, label: "Session: " + session.SessionID})
	}
	for _, decision := range decisions {
		relationshipNodes = append(relationshipNodes, diagramNode{kind: "decision", id: decision.ID, label: entityDiagramLabel("Decision", decision.Title, decision.Status, decision.Tags)})
	}
	for _, loop := range loops {
		relationshipNodes = append(relationshipNodes, diagramNode{kind: "loop", id: loop.ID, label: entityDiagramLabel("Loop", loop.Title, loop.Status, loop.Tags)})
		if loop.Blocker != "" {
			relationshipNodes = append(relationshipNodes, diagramNode{kind: "blocker", id: loop.ID + "\x00" + loop.Blocker, label: "Blocker: " + loop.Blocker})
		}
		if loop.NextExperiment != "" {
			relationshipNodes = append(relationshipNodes, diagramNode{kind: "experiment", id: loop.ID + "\x00" + loop.NextExperiment, label: "Next experiment: " + loop.NextExperiment})
		}
	}
	for _, blocker := range sortedUniqueStrings(state.CurrentState.Blockers) {
		relationshipNodes = append(relationshipNodes, diagramNode{kind: "blocker", id: "current-state\x00" + blocker, label: "Blocker: " + blocker})
	}

	var relationshipEdges []diagramEdge
	projectNode := nodeID("project", state.ProjectID)
	if state.ProjectID != "" {
		for _, session := range sessions {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: projectNode, to: nodeID("session", session.ID)})
		}
		for _, decision := range decisions {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: projectNode, to: nodeID("decision", decision.ID)})
		}
		for _, loop := range loops {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: projectNode, to: nodeID("loop", loop.ID)})
		}
		for _, blocker := range sortedUniqueStrings(state.CurrentState.Blockers) {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: projectNode, to: nodeID("blocker", "current-state\x00"+blocker)})
		}
	}
	for _, session := range sessions {
		from := nodeID("session", session.ID)
		for _, id := range sortedUniqueStrings(append(append([]string(nil), session.DecisionsAdded...), session.DecisionsRevised...)) {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: from, to: nodeID("decision", id)})
		}
		for _, id := range sortedUniqueStrings(append(append([]string(nil), session.OpenLoopsCreated...), session.OpenLoopsClosed...)) {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: from, to: nodeID("loop", id)})
		}
	}
	for _, decision := range decisions {
		from := nodeID("decision", decision.ID)
		for _, id := range sortedUniqueStrings(decision.Supersedes) {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: from, to: nodeID("decision", id)})
		}
	}
	for _, loop := range loops {
		from := nodeID("loop", loop.ID)
		if loop.Blocker != "" {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: from, to: nodeID("blocker", loop.ID+"\x00"+loop.Blocker)})
		}
		if loop.NextExperiment != "" {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: from, to: nodeID("experiment", loop.ID+"\x00"+loop.NextExperiment)})
		}
	}

	var output bytes.Buffer
	output.WriteString("# Project evolution\n\n")
	output.WriteString("This file is derived from the accepted project ledger. Manual edits are overwritten on the next accepted render.\n\n")
	writeMermaidGraph(&output, "Causal evolution", "flowchart LR", causalNodes, causalEdges)
	output.WriteByte('\n')
	writeMermaidGraph(&output, "Project relationships", "graph TD", relationshipNodes, relationshipEdges)
	return output.Bytes(), nil
}

func validateDiagramState(state State) error {
	owners := map[string]string{
		"current-state":      "reserved current state",
		"evolution-timeline": "reserved timeline",
	}
	register := func(id, owner string) error {
		if id == "" {
			return fmt.Errorf("diagram state has empty %s ID", owner)
		}
		if previous, exists := owners[id]; exists {
			return fmt.Errorf("duplicate ledger ID %q in %s and %s", id, previous, owner)
		}
		owners[id] = owner
		return nil
	}
	for i, event := range state.Timeline {
		if err := register(event.ID, fmt.Sprintf("timeline event %d", i)); err != nil {
			return err
		}
	}
	for key, decision := range state.Decisions {
		if key != decision.ID {
			return fmt.Errorf("decision map key %q does not match ID %q", key, decision.ID)
		}
		if decision.ProjectID != state.ProjectID {
			return fmt.Errorf("decision %q project mismatch", decision.ID)
		}
		if err := register(decision.ID, "decision"); err != nil {
			return err
		}
	}
	for key, loop := range state.OpenLoops {
		if key != loop.ID {
			return fmt.Errorf("open-loop map key %q does not match ID %q", key, loop.ID)
		}
		if loop.ProjectID != state.ProjectID {
			return fmt.Errorf("open loop %q project mismatch", loop.ID)
		}
		if err := register(loop.ID, "open loop"); err != nil {
			return err
		}
	}
	sessionIDs := make(map[string]string, len(state.Sessions))
	for key, session := range state.Sessions {
		if key != session.ID {
			return fmt.Errorf("session map key %q does not match ID %q", key, session.ID)
		}
		if session.ProjectID != state.ProjectID {
			return fmt.Errorf("session %q project mismatch", session.ID)
		}
		if session.SessionID == "" {
			return fmt.Errorf("session %q has empty session ID", session.ID)
		}
		if previous, duplicate := sessionIDs[session.SessionID]; duplicate {
			return fmt.Errorf("duplicate session ID %q in %q and %q", session.SessionID, previous, session.ID)
		}
		sessionIDs[session.SessionID] = session.ID
		if err := register(session.ID, "session"); err != nil {
			return err
		}
	}
	if state.CurrentState.ProjectID != "" && state.CurrentState.ProjectID != state.ProjectID {
		return errors.New("current state project mismatch")
	}
	for _, event := range state.Timeline {
		if err := validateDiagramReferences(event.DecisionIDs, state.Decisions, "timeline decision"); err != nil {
			return fmt.Errorf("event %q: %w", event.ID, err)
		}
		if err := validateDiagramReferences(event.OpenLoopIDs, state.OpenLoops, "timeline open-loop"); err != nil {
			return fmt.Errorf("event %q: %w", event.ID, err)
		}
	}
	for _, decision := range state.Decisions {
		if err := validateDiagramReferences(decision.Supersedes, state.Decisions, "superseded decision"); err != nil {
			return fmt.Errorf("decision %q: %w", decision.ID, err)
		}
	}
	for _, session := range state.Sessions {
		decisionRefs := append(append([]string(nil), session.DecisionsAdded...), session.DecisionsRevised...)
		if err := validateDiagramReferences(decisionRefs, state.Decisions, "session decision"); err != nil {
			return fmt.Errorf("session %q: %w", session.ID, err)
		}
		loopRefs := append(append([]string(nil), session.OpenLoopsCreated...), session.OpenLoopsClosed...)
		if err := validateDiagramReferences(loopRefs, state.OpenLoops, "session open-loop"); err != nil {
			return fmt.Errorf("session %q: %w", session.ID, err)
		}
	}
	return nil
}

func validateDiagramReferences[T any](ids []string, entities map[string]T, label string) error {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate %s ID %q", label, id)
		}
		seen[id] = struct{}{}
		if _, exists := entities[id]; !exists {
			return fmt.Errorf("unknown %s ID %q", label, id)
		}
	}
	return nil
}

func sortedDiagramDecisions(values map[string]Decision) []Decision {
	result := make([]Decision, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func sortedDiagramLoops(values map[string]OpenLoop) []OpenLoop {
	result := make([]OpenLoop, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func sortedDiagramSessions(values map[string]SessionReport) []SessionReport {
	result := make([]SessionReport, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func entityDiagramLabel(kind, title, status string, tags []string) string {
	label := kind + ": " + title
	if status != "" {
		label += " · status: " + status
	}
	if sorted := sortedUniqueStrings(tags); len(sorted) != 0 {
		label += " · tags: " + strings.Join(sorted, ", ")
	}
	return label
}

func writeMermaidGraph(output *bytes.Buffer, title, declaration string, nodes []diagramNode, edges []diagramEdge) {
	output.WriteString("## ")
	output.WriteString(title)
	output.WriteString("\n\n```mermaid\n")
	output.WriteString(declaration)
	output.WriteByte('\n')
	for _, node := range nodes {
		output.WriteString("  ")
		output.WriteString(nodeID(node.kind, node.id))
		output.WriteString("[\"")
		output.WriteString(escapeDiagramLabel(node.label))
		output.WriteString("\"]\n")
	}
	for _, edge := range sortedUniqueEdges(edges) {
		output.WriteString("  ")
		output.WriteString(edge.from)
		output.WriteString(" --> ")
		output.WriteString(edge.to)
		output.WriteByte('\n')
	}
	output.WriteString("```\n")
}

func sortedUniqueEdges(values []diagramEdge) []diagramEdge {
	result := append([]diagramEdge(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].from != result[j].from {
			return result[i].from < result[j].from
		}
		return result[i].to < result[j].to
	})
	write := 0
	for _, edge := range result {
		if write != 0 && result[write-1] == edge {
			continue
		}
		result[write] = edge
		write++
	}
	return result[:write]
}

func sortedUniqueStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	write := 0
	for _, value := range result {
		if write != 0 && result[write-1] == value {
			continue
		}
		result[write] = value
		write++
	}
	return result[:write]
}

func nodeID(kind, id string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + id))
	return kind + "_" + hex.EncodeToString(sum[:6])
}

func escapeDiagramLabel(value string) string {
	var escaped strings.Builder
	for _, character := range value {
		switch character {
		case '&':
			escaped.WriteString("&amp;")
		case '"':
			escaped.WriteString("&quot;")
		case '[':
			escaped.WriteString("&#91;")
		case ']':
			escaped.WriteString("&#93;")
		case '<':
			escaped.WriteString("&lt;")
		case '>':
			escaped.WriteString("&gt;")
		case '`':
			escaped.WriteString("&#96;")
		case '\\':
			escaped.WriteString("&#92;")
		default:
			if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) || unicode.Is(unicode.Zl, character) || unicode.Is(unicode.Zp, character) {
				escaped.WriteByte(' ')
			} else {
				escaped.WriteRune(character)
			}
		}
	}
	return escaped.String()
}
