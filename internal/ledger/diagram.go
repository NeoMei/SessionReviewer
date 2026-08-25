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

const (
	maxDiagramNodes       = 20_000
	maxDiagramEdges       = 50_000
	maxDiagramLabelValues = 100_000
	maxDiagramIdentityLen = 128
)

type diagramRenderOptions struct {
	nodeDigest     func([]byte) [32]byte
	maxNodes       int
	maxEdges       int
	maxOutputBytes int
	maxLabelValues int
}

func (options diagramRenderOptions) normalized() diagramRenderOptions {
	if options.nodeDigest == nil {
		options.nodeDigest = sha256.Sum256
	}
	if options.maxNodes <= 0 {
		options.maxNodes = maxDiagramNodes
	}
	if options.maxEdges <= 0 {
		options.maxEdges = maxDiagramEdges
	}
	if options.maxOutputBytes <= 0 {
		options.maxOutputBytes = MaxDocumentBytes
	}
	if options.maxLabelValues <= 0 {
		options.maxLabelValues = maxDiagramLabelValues
	}
	return options
}

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
	return renderDiagramWithOptions(state, diagramRenderOptions{})
}

func renderDiagramWithOptions(state State, options diagramRenderOptions) ([]byte, error) {
	options = options.normalized()
	if err := preflightDiagramBudget(state, options); err != nil {
		return nil, err
	}
	if err := validateDiagramState(state); err != nil {
		return nil, err
	}
	makeNodeID := func(kind, id string) string {
		return nodeIDWithDigest(kind, id, options.nodeDigest)
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
		causalEdges = append(causalEdges, diagramEdge{from: makeNodeID("event", timeline[i-1].ID), to: makeNodeID("event", timeline[i].ID)})
	}
	for _, event := range timeline {
		from := makeNodeID("event", event.ID)
		for _, id := range sortedUniqueStrings(event.DecisionIDs) {
			causalEdges = append(causalEdges, diagramEdge{from: from, to: makeNodeID("decision", id)})
		}
		for _, id := range sortedUniqueStrings(event.OpenLoopIDs) {
			causalEdges = append(causalEdges, diagramEdge{from: from, to: makeNodeID("loop", id)})
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
	if err := validateGeneratedDiagramNodeIDs(causalNodes, relationshipNodes, options.nodeDigest); err != nil {
		return nil, err
	}

	projectNode := makeNodeID("project", state.ProjectID)
	if state.ProjectID != "" {
		for _, session := range sessions {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: projectNode, to: makeNodeID("session", session.ID)})
		}
		for _, decision := range decisions {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: projectNode, to: makeNodeID("decision", decision.ID)})
		}
		for _, loop := range loops {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: projectNode, to: makeNodeID("loop", loop.ID)})
		}
		for _, blocker := range sortedUniqueStrings(state.CurrentState.Blockers) {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: projectNode, to: makeNodeID("blocker", "current-state\x00"+blocker)})
		}
	}
	for _, session := range sessions {
		from := makeNodeID("session", session.ID)
		for _, id := range sortedUniqueStrings(append(append([]string(nil), session.DecisionsAdded...), session.DecisionsRevised...)) {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: from, to: makeNodeID("decision", id)})
		}
		for _, id := range sortedUniqueStrings(append(append([]string(nil), session.OpenLoopsCreated...), session.OpenLoopsClosed...)) {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: from, to: makeNodeID("loop", id)})
		}
	}
	for _, decision := range decisions {
		from := makeNodeID("decision", decision.ID)
		for _, id := range sortedUniqueStrings(decision.Supersedes) {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: from, to: makeNodeID("decision", id)})
		}
	}
	for _, loop := range loops {
		from := makeNodeID("loop", loop.ID)
		if loop.Blocker != "" {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: from, to: makeNodeID("blocker", loop.ID+"\x00"+loop.Blocker)})
		}
		if loop.NextExperiment != "" {
			relationshipEdges = append(relationshipEdges, diagramEdge{from: from, to: makeNodeID("experiment", loop.ID+"\x00"+loop.NextExperiment)})
		}
	}

	output := newCappedDiagramBuffer(options.maxOutputBytes)
	output.WriteString("# Project evolution\n\n")
	output.WriteString("This file is derived from the accepted project ledger. Manual edits are overwritten on the next accepted render.\n\n")
	recovery, err := RenderRecoveryMermaid(state)
	if err != nil {
		return nil, err
	}
	output.WriteString("## Recovery mainline\n\n```mermaid\n")
	output.WriteString(recovery)
	output.WriteString("```\n\n")
	writeMermaidGraph(output, "Causal evolution", "flowchart LR", causalNodes, causalEdges, options.nodeDigest)
	output.appendByte('\n')
	writeMermaidGraph(output, "Project relationships", "graph TD", relationshipNodes, relationshipEdges, options.nodeDigest)
	if output.err != nil {
		return nil, output.err
	}
	return output.Bytes(), nil
}

type diagramBudget struct {
	options     diagramRenderOptions
	nodes       int
	edges       int
	labelValues int
	labelBytes  int
}

func preflightDiagramBudget(state State, options diagramRenderOptions) error {
	budget := diagramBudget{options: options}
	if err := budget.addNodes(len(state.Timeline)); err != nil {
		return err
	}
	if state.ProjectID != "" {
		if err := budget.addNodes(1); err != nil {
			return err
		}
	}
	for _, count := range []int{len(state.Decisions), len(state.Decisions), len(state.OpenLoops), len(state.OpenLoops), len(state.Sessions), len(state.CurrentState.Blockers)} {
		if err := budget.addNodes(count); err != nil {
			return err
		}
	}
	for _, loop := range state.OpenLoops {
		if loop.Blocker != "" {
			if err := budget.addNodes(1); err != nil {
				return err
			}
		}
		if loop.NextExperiment != "" {
			if err := budget.addNodes(1); err != nil {
				return err
			}
		}
	}
	if err := preflightDiagramIdentities(state); err != nil {
		return err
	}

	if len(state.Timeline) > 1 {
		if err := budget.addEdges(len(state.Timeline) - 1); err != nil {
			return err
		}
	}
	for _, event := range state.Timeline {
		if err := budget.addEdges(len(event.DecisionIDs)); err != nil {
			return err
		}
		if err := budget.addEdges(len(event.OpenLoopIDs)); err != nil {
			return err
		}
	}
	if state.ProjectID != "" {
		for _, count := range []int{len(state.Sessions), len(state.Decisions), len(state.OpenLoops), len(state.CurrentState.Blockers)} {
			if err := budget.addEdges(count); err != nil {
				return err
			}
		}
	}
	for _, session := range state.Sessions {
		for _, count := range []int{len(session.DecisionsAdded), len(session.DecisionsRevised), len(session.OpenLoopsCreated), len(session.OpenLoopsClosed)} {
			if err := budget.addEdges(count); err != nil {
				return err
			}
		}
	}
	for _, decision := range state.Decisions {
		if err := budget.addEdges(len(decision.Supersedes)); err != nil {
			return err
		}
	}
	for _, loop := range state.OpenLoops {
		if loop.Blocker != "" {
			if err := budget.addEdges(1); err != nil {
				return err
			}
		}
		if loop.NextExperiment != "" {
			if err := budget.addEdges(1); err != nil {
				return err
			}
		}
	}

	if state.ProjectID != "" {
		if err := budget.addLabel(state.ProjectID, 1); err != nil {
			return err
		}
		if err := budget.addLabel(state.CurrentState.Goal, 1); err != nil {
			return err
		}
	}
	for _, event := range state.Timeline {
		if err := budget.addLabel(event.OccurredAt, 1); err != nil {
			return err
		}
		if err := budget.addLabel(event.Title, 1); err != nil {
			return err
		}
	}
	for _, decision := range state.Decisions {
		for _, value := range []string{decision.Title, decision.Status} {
			if err := budget.addLabel(value, 2); err != nil {
				return err
			}
		}
		for _, tag := range decision.Tags {
			if err := budget.addLabel(tag, 2); err != nil {
				return err
			}
		}
	}
	for _, loop := range state.OpenLoops {
		for _, value := range []string{loop.Title, loop.Status} {
			if err := budget.addLabel(value, 2); err != nil {
				return err
			}
		}
		for _, tag := range loop.Tags {
			if err := budget.addLabel(tag, 2); err != nil {
				return err
			}
		}
		for _, value := range []string{loop.Blocker, loop.NextExperiment} {
			if err := budget.addLabel(value, 1); err != nil {
				return err
			}
		}
	}
	for _, session := range state.Sessions {
		if err := budget.addLabel(session.SessionID, 1); err != nil {
			return err
		}
	}
	for _, blocker := range state.CurrentState.Blockers {
		if err := budget.addLabel(blocker, 1); err != nil {
			return err
		}
	}
	return nil
}

func preflightDiagramIdentities(state State) error {
	check := func(value, owner string) error {
		if len(value) > maxDiagramIdentityLen {
			return fmt.Errorf("diagram %s identity exceeds size limit", owner)
		}
		return nil
	}
	checkAll := func(values []string, owner string) error {
		for _, value := range values {
			if err := check(value, owner); err != nil {
				return err
			}
		}
		return nil
	}
	for _, value := range []struct {
		identity string
		owner    string
	}{
		{identity: state.ProjectID, owner: "project"},
		{identity: state.CurrentState.ProjectID, owner: "current-state project"},
	} {
		if err := check(value.identity, value.owner); err != nil {
			return err
		}
	}
	for _, event := range state.Timeline {
		if err := check(event.ID, "timeline event"); err != nil {
			return err
		}
		if err := checkAll(event.DecisionIDs, "timeline decision reference"); err != nil {
			return err
		}
		if err := checkAll(event.OpenLoopIDs, "timeline open-loop reference"); err != nil {
			return err
		}
	}
	for key, decision := range state.Decisions {
		for _, value := range []struct {
			identity string
			owner    string
		}{
			{identity: key, owner: "decision map key"},
			{identity: decision.ID, owner: "decision"},
			{identity: decision.ProjectID, owner: "decision project"},
		} {
			if err := check(value.identity, value.owner); err != nil {
				return err
			}
		}
		if err := checkAll(decision.Supersedes, "superseded decision reference"); err != nil {
			return err
		}
	}
	for key, loop := range state.OpenLoops {
		for _, value := range []struct {
			identity string
			owner    string
		}{
			{identity: key, owner: "open-loop map key"},
			{identity: loop.ID, owner: "open loop"},
			{identity: loop.ProjectID, owner: "open-loop project"},
		} {
			if err := check(value.identity, value.owner); err != nil {
				return err
			}
		}
	}
	for key, session := range state.Sessions {
		for _, value := range []struct {
			identity string
			owner    string
		}{
			{identity: key, owner: "session map key"},
			{identity: session.ID, owner: "session"},
			{identity: session.ProjectID, owner: "session project"},
		} {
			if err := check(value.identity, value.owner); err != nil {
				return err
			}
		}
		for _, references := range []struct {
			values []string
			owner  string
		}{
			{values: session.DecisionsAdded, owner: "session added-decision reference"},
			{values: session.DecisionsRevised, owner: "session revised-decision reference"},
			{values: session.OpenLoopsCreated, owner: "session created-loop reference"},
			{values: session.OpenLoopsClosed, owner: "session closed-loop reference"},
		} {
			if err := checkAll(references.values, references.owner); err != nil {
				return err
			}
		}
	}
	return nil
}

func (budget *diagramBudget) addNodes(count int) error {
	if count < 0 || count > budget.options.maxNodes-budget.nodes {
		return errors.New("diagram node limit exceeded")
	}
	budget.nodes += count
	return nil
}

func (budget *diagramBudget) addEdges(count int) error {
	if count < 0 || count > budget.options.maxEdges-budget.edges {
		return errors.New("diagram edge limit exceeded")
	}
	budget.edges += count
	return nil
}

func (budget *diagramBudget) addLabel(value string, copies int) error {
	if copies < 0 || copies > budget.options.maxLabelValues-budget.labelValues {
		return errors.New("diagram label-value limit exceeded")
	}
	budget.labelValues += copies
	if len(value) != 0 && copies > (budget.options.maxOutputBytes-budget.labelBytes)/len(value) {
		return errors.New("diagram size limit exceeded")
	}
	budget.labelBytes += len(value) * copies
	return nil
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

func validateGeneratedDiagramNodeIDs(causal, relationships []diagramNode, digest func([]byte) [32]byte) error {
	generated := make(map[string]string, len(causal)+len(relationships))
	for _, nodes := range [][]diagramNode{causal, relationships} {
		for _, node := range nodes {
			original := node.kind + "\x00" + node.id
			generatedID := nodeIDWithDigest(node.kind, node.id, digest)
			if previous, exists := generated[generatedID]; exists && previous != original {
				return fmt.Errorf("generated diagram node ID collision %q between %q and %q", generatedID, previous, original)
			}
			generated[generatedID] = original
		}
	}
	return nil
}

type cappedDiagramBuffer struct {
	buffer bytes.Buffer
	limit  int
	err    error
}

func newCappedDiagramBuffer(limit int) *cappedDiagramBuffer {
	return &cappedDiagramBuffer{limit: limit}
}

func (output *cappedDiagramBuffer) WriteString(value string) {
	if output.err != nil {
		return
	}
	if len(value) > output.limit-output.buffer.Len() {
		output.err = errors.New("diagram size limit exceeded")
		return
	}
	_, _ = output.buffer.WriteString(value)
}

func (output *cappedDiagramBuffer) appendByte(value byte) {
	if output.err != nil {
		return
	}
	if output.buffer.Len() >= output.limit {
		output.err = errors.New("diagram size limit exceeded")
		return
	}
	_ = output.buffer.WriteByte(value)
}

func (output *cappedDiagramBuffer) Bytes() []byte {
	if output.err != nil {
		return nil
	}
	return output.buffer.Bytes()
}

func writeMermaidGraph(output *cappedDiagramBuffer, title, declaration string, nodes []diagramNode, edges []diagramEdge, digest func([]byte) [32]byte) {
	output.WriteString("## ")
	output.WriteString(title)
	output.WriteString("\n\n```mermaid\n")
	output.WriteString(declaration)
	output.appendByte('\n')
	for _, node := range nodes {
		output.WriteString("  ")
		output.WriteString(nodeIDWithDigest(node.kind, node.id, digest))
		output.WriteString("[\"")
		writeEscapedDiagramLabel(output, node.label)
		output.WriteString("\"]\n")
	}
	for _, edge := range sortedUniqueEdges(edges) {
		output.WriteString("  ")
		output.WriteString(edge.from)
		output.WriteString(" --> ")
		output.WriteString(edge.to)
		output.appendByte('\n')
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
	return nodeIDWithDigest(kind, id, sha256.Sum256)
}

func nodeIDWithDigest(kind, id string, digest func([]byte) [32]byte) string {
	sum := digest([]byte(kind + "\x00" + id))
	return kind + "_" + hex.EncodeToString(sum[:6])
}

func writeEscapedDiagramLabel(output *cappedDiagramBuffer, value string) {
	for _, character := range value {
		switch character {
		case '&':
			output.WriteString("&amp;")
		case '"':
			output.WriteString("&quot;")
		case '[':
			output.WriteString("&#91;")
		case ']':
			output.WriteString("&#93;")
		case '<':
			output.WriteString("&lt;")
		case '>':
			output.WriteString("&gt;")
		case '`':
			output.WriteString("&#96;")
		case '\\':
			output.WriteString("&#92;")
		default:
			if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) || unicode.Is(unicode.Zl, character) || unicode.Is(unicode.Zp, character) {
				output.appendByte(' ')
			} else {
				output.WriteString(string(character))
			}
		}
		if output.err != nil {
			return
		}
	}
}
