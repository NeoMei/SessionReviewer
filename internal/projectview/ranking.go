package projectview

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
)

type moduleStats struct {
	path          string
	sessions      map[string]struct{}
	verifications int
	changes       int
	latest        time.Time
	dependencies  []string
	seenDeps      map[string]struct{}
}

func rankModules(events []event, reference time.Time, limit int) ([]memory.DerivedRecord, error) {
	modules := make(map[string]*moduleStats)
	for _, item := range events {
		if item.summary.Kind != "file" {
			continue
		}
		modulePath := normalizedPath(item.summary.Fields["path"])
		if modulePath == "" {
			modulePath = normalizedPath(item.summary.Object)
		}
		if modulePath == "" {
			continue
		}
		stats := modules[modulePath]
		if stats == nil {
			if err := ensureRecordCapacity(len(modules), 1, limit); err != nil {
				return nil, fmt.Errorf("module ranking limit exceeded: %w", err)
			}
			stats = &moduleStats{path: modulePath, sessions: make(map[string]struct{}), seenDeps: make(map[string]struct{})}
			modules[modulePath] = stats
		}
		stats.sessions[item.provider+"\x00"+item.sessionID] = struct{}{}
		if item.summary.Operation == "file_change" && item.summary.Outcome == "success" {
			stats.changes++
		}
		if item.time.After(stats.latest) {
			stats.latest = item.time
		}
		stats.addDependency(item.summary.RevisionID)
	}
	for _, item := range events {
		if item.summary.Kind != "verification" {
			continue
		}
		component := normalizedPath(item.summary.Fields["component"])
		if stats := modules[component]; stats != nil {
			stats.verifications++
			stats.addDependency(item.summary.RevisionID)
			if item.time.After(stats.latest) {
				stats.latest = item.time
			}
		}
	}

	type ranked struct {
		stats  *moduleStats
		score  int
		bucket int
	}
	values := make([]ranked, 0, len(modules))
	for _, stats := range modules {
		bucket := recencyBucket(reference, stats.latest)
		score := 4*len(stats.sessions) + 2*stats.verifications + stats.changes + bucket
		values = append(values, ranked{stats: stats, score: score, bucket: bucket})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].score != values[j].score {
			return values[i].score > values[j].score
		}
		return values[i].stats.path < values[j].stats.path
	})
	result := make([]memory.DerivedRecord, 0, len(values))
	for index, value := range values {
		subject := value.stats.path
		if len(subject) > 256 {
			subject = derivedID("module", subject)
		}
		result = append(result, memory.DerivedRecord{
			ID: derivedID("module-rank", value.stats.path), Kind: "module_rank", Subject: subject,
			OccurredAt: value.stats.latest.UTC().Format(time.RFC3339Nano), DependencyRevisionIDs: append([]string(nil), value.stats.dependencies...),
			RuleID: "module-score", RuleVersion: ReducerVersion,
			Fields: map[string]string{
				"path": value.stats.path, "rank": strconv.Itoa(index + 1), "score": strconv.Itoa(value.score),
				"session_coverage": strconv.Itoa(len(value.stats.sessions)), "verification_count": strconv.Itoa(value.stats.verifications),
				"change_count": strconv.Itoa(value.stats.changes), "recency_bucket": strconv.Itoa(value.bucket),
				"latest_observed_at": value.stats.latest.UTC().Format(time.RFC3339Nano),
			},
		})
	}
	return result, nil
}

func (stats *moduleStats) addDependency(revisionID string) {
	if _, duplicate := stats.seenDeps[revisionID]; duplicate {
		return
	}
	stats.seenDeps[revisionID] = struct{}{}
	stats.dependencies = append(stats.dependencies, revisionID)
}

func normalizedPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return ""
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return ""
	}
	return strings.TrimPrefix(cleaned, "./")
}

func recencyBucket(reference, latest time.Time) int {
	age := reference.Sub(latest)
	switch {
	case age <= 7*24*time.Hour:
		return 3
	case age <= 30*24*time.Hour:
		return 2
	case age <= 90*24*time.Hour:
		return 1
	default:
		return 0
	}
}
