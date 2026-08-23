package recovery

import (
	"container/heap"
	"os"
	"sort"
	"strings"

	"github.com/neomei/SessionReviewer/internal/ledger"
)

type Theme struct {
	Name        string
	DecisionIDs []string
	OpenLoopIDs []string
}

type HistoryView struct {
	ProjectID string
	Timeline  []ledger.TimelineEvent
	Decisions []ledger.Decision
	OpenLoops []ledger.OpenLoop
	Themes    []Theme
}

// HistoryLedgerOnly renders accepted cross-session history. It never consults
// pending evidence or any derived/cache/repository surface.
func HistoryLedgerOnly(projectRoot string) (HistoryView, error) {
	state, err := ledger.Load(projectRoot)
	return historyLedgerOnly(state, err)
}

// HistoryLedgerOnlyExpected derives the same accepted-only project history
// while requiring the project root opened by ledger.LoadExpected to retain a
// caller-pinned filesystem identity.
func HistoryLedgerOnlyExpected(projectRoot string, expectedRoot os.FileInfo) (HistoryView, error) {
	state, err := ledger.LoadExpected(projectRoot, expectedRoot)
	return historyLedgerOnly(state, err)
}

func historyLedgerOnly(state ledger.State, err error) (HistoryView, error) {
	if err != nil {
		return HistoryView{}, err
	}
	if err := validateRecoveryState(state); err != nil {
		return HistoryView{}, err
	}

	view := HistoryView{ProjectID: state.ProjectID}
	view.Timeline = cloneTimeline(state.Timeline)
	sort.Slice(view.Timeline, func(i, j int) bool {
		if view.Timeline[i].OccurredAt != view.Timeline[j].OccurredAt {
			return view.Timeline[i].OccurredAt < view.Timeline[j].OccurredAt
		}
		return view.Timeline[i].ID < view.Timeline[j].ID
	})
	view.Decisions = orderedDecisions(state.Decisions)
	view.OpenLoops = make([]ledger.OpenLoop, 0, len(state.OpenLoops))
	for _, loop := range state.OpenLoops {
		view.OpenLoops = append(view.OpenLoops, cloneOpenLoop(loop))
	}
	sort.Slice(view.OpenLoops, func(i, j int) bool { return view.OpenLoops[i].ID < view.OpenLoops[j].ID })
	view.Themes = buildThemes(view.Decisions, view.OpenLoops)
	return view, nil
}

func (view HistoryView) Markdown() string {
	out := newRecoveryMarkdownBuilder()
	if !out.raw("# Project history\n\n") {
		return out.finish()
	}
	if !out.field("Project", view.ProjectID) {
		return out.finish()
	}
	if len(view.Timeline) != 0 {
		if !out.raw("## Timeline\n\n") {
			return out.finish()
		}
		for _, event := range view.Timeline {
			if !out.raw("- ") || !out.escaped(event.OccurredAt) || !out.raw(" | ") || !out.escaped(event.Title) || !out.raw(" | ") || !out.escaped(string(event.Class)) {
				return out.finish()
			}
			if strings.TrimSpace(event.Summary) != "" {
				if !out.raw(" | ") || !out.escaped(event.Summary) {
					return out.finish()
				}
			}
			if !out.raw("\n") {
				return out.finish()
			}
		}
		if !out.raw("\n") {
			return out.finish()
		}
	}
	if len(view.Decisions) != 0 {
		if !out.raw("## Decisions\n\n") {
			return out.finish()
		}
		for _, decision := range view.Decisions {
			if !out.raw("- ") || !out.escaped(decision.Title) || !out.raw(" [") || !out.escaped(decision.ID) || !out.raw("; ") || !out.escaped(decision.Status) || !out.raw("]\n") {
				return out.finish()
			}
			if len(decision.Supersedes) != 0 {
				if !out.raw("  - Supersedes: ") || !out.escapedList(decision.Supersedes, ", ") || !out.raw("\n") {
					return out.finish()
				}
			}
		}
		if !out.raw("\n") {
			return out.finish()
		}
	}
	if len(view.OpenLoops) != 0 {
		if !out.raw("## Open loops\n\n") {
			return out.finish()
		}
		for _, loop := range view.OpenLoops {
			if !out.raw("- ") || !out.escaped(loop.Title) || !out.raw(" [") || !out.escaped(loop.ID) || !out.raw("; ") || !out.escaped(loop.Status) || !out.raw("]\n") {
				return out.finish()
			}
		}
		if !out.raw("\n") {
			return out.finish()
		}
	}
	if len(view.Themes) != 0 {
		if !out.raw("## Themes\n\n") {
			return out.finish()
		}
		for _, theme := range view.Themes {
			if !out.raw("- ") || !out.escaped(theme.Name) || !out.raw("\n") {
				return out.finish()
			}
			if len(theme.DecisionIDs) != 0 {
				if !out.raw("  - Decisions: ") || !out.escapedList(theme.DecisionIDs, ", ") || !out.raw("\n") {
					return out.finish()
				}
			}
			if len(theme.OpenLoopIDs) != 0 {
				if !out.raw("  - Open loops: ") || !out.escapedList(theme.OpenLoopIDs, ", ") || !out.raw("\n") {
					return out.finish()
				}
			}
		}
		if !out.raw("\n") {
			return out.finish()
		}
	}
	return out.finish()
}

func orderedDecisions(byID map[string]ledger.Decision) []ledger.Decision {
	incoming := make(map[string]int, len(byID))
	for id := range byID {
		incoming[id] = 0
	}
	for _, decision := range byID {
		for _, predecessor := range sortedUnique(decision.Supersedes) {
			incoming[predecessor]++
		}
	}
	ready := &bytewiseIDHeap{}
	heap.Init(ready)
	for id, count := range incoming {
		if count == 0 {
			heap.Push(ready, id)
		}
	}
	ordered := make([]ledger.Decision, 0, len(byID))
	for ready.Len() != 0 {
		id := heap.Pop(ready).(string)
		ordered = append(ordered, cloneDecision(byID[id]))
		for _, predecessor := range sortedUnique(byID[id].Supersedes) {
			incoming[predecessor]--
			if incoming[predecessor] == 0 {
				heap.Push(ready, predecessor)
			}
		}
	}
	return ordered
}

type bytewiseIDHeap []string

func (items bytewiseIDHeap) Len() int           { return len(items) }
func (items bytewiseIDHeap) Less(i, j int) bool { return items[i] < items[j] }
func (items bytewiseIDHeap) Swap(i, j int)      { items[i], items[j] = items[j], items[i] }
func (items *bytewiseIDHeap) Push(value any)    { *items = append(*items, value.(string)) }
func (items *bytewiseIDHeap) Pop() any {
	old := *items
	last := old[len(old)-1]
	*items = old[:len(old)-1]
	return last
}

func buildThemes(decisions []ledger.Decision, loops []ledger.OpenLoop) []Theme {
	type members struct {
		decisions map[string]struct{}
		loops     map[string]struct{}
	}
	grouped := make(map[string]*members)
	memberFor := func(tag string) *members {
		if strings.TrimSpace(tag) == "" {
			return nil
		}
		name := tag
		if grouped[name] == nil {
			grouped[name] = &members{decisions: make(map[string]struct{}), loops: make(map[string]struct{})}
		}
		return grouped[name]
	}
	for _, decision := range decisions {
		for _, tag := range decision.Tags {
			if member := memberFor(tag); member != nil {
				member.decisions[decision.ID] = struct{}{}
			}
		}
	}
	for _, loop := range loops {
		if !unresolvedLoop(loop.Status) {
			continue
		}
		for _, tag := range loop.Tags {
			if member := memberFor(tag); member != nil {
				member.loops[loop.ID] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(grouped))
	for name := range grouped {
		names = append(names, name)
	}
	sort.Strings(names)
	themes := make([]Theme, 0, len(names))
	for _, name := range names {
		member := grouped[name]
		theme := Theme{Name: name, DecisionIDs: sortedSet(member.decisions), OpenLoopIDs: sortedSet(member.loops)}
		themes = append(themes, theme)
	}
	return themes
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func cloneTimeline(items []ledger.TimelineEvent) []ledger.TimelineEvent {
	result := make([]ledger.TimelineEvent, len(items))
	for index, item := range items {
		item.Evidence = append([]ledger.EvidenceRef(nil), item.Evidence...)
		item.DecisionIDs = append([]string(nil), item.DecisionIDs...)
		item.OpenLoopIDs = append([]string(nil), item.OpenLoopIDs...)
		result[index] = item
	}
	return result
}

func cloneDecision(item ledger.Decision) ledger.Decision {
	item.Tags = append([]string(nil), item.Tags...)
	item.Supersedes = append([]string(nil), item.Supersedes...)
	item.SourceSessions = append([]string(nil), item.SourceSessions...)
	item.Evidence = append([]ledger.EvidenceRef(nil), item.Evidence...)
	item.Alternatives = append([]string(nil), item.Alternatives...)
	item.RejectedPaths = append([]string(nil), item.RejectedPaths...)
	return item
}

func cloneOpenLoop(item ledger.OpenLoop) ledger.OpenLoop {
	item.Tags = append([]string(nil), item.Tags...)
	item.SourceSessions = append([]string(nil), item.SourceSessions...)
	item.Evidence = append([]ledger.EvidenceRef(nil), item.Evidence...)
	item.Attempts = append([]string(nil), item.Attempts...)
	return item
}
