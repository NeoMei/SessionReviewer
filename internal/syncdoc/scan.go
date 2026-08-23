package syncdoc

import (
	"bytes"
	"errors"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
)

var stableScanID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type Entry struct {
	Identity     Identity
	RelativePath string
	PathKey      string
	Document     Document
	Content      []byte
	ContentHash  string
}

type IssueKind string

const (
	IssueMalformed     IssueKind = "malformed"
	IssueDuplicateID   IssueKind = "duplicate_id"
	IssuePathCollision IssueKind = "path_collision"
	IssueReservedEdit  IssueKind = "reserved_edit"
	IssueSensitive     IssueKind = "sensitive_content"
)

type ScanIssue struct {
	Kind         IssueKind
	RelativePath string
	EntityID     string
	Err          error
}

type Inventory struct {
	ByID   map[string]Entry
	Issues []ScanIssue
}

type SourceDocument struct {
	RelativePath string
	Content      []byte
}

// Scan walks a pinned tree, removes synchronization metadata from the source
// set, and returns an inventory whose parse diagnostics never include content.
func Scan(root *pathguard.Directory, rootRelative, goos string, caseMode platform.CaseMode) Inventory {
	sources := make([]SourceDocument, 0)
	err := root.WalkMarkdown(rootRelative, func(relative string, content []byte) error {
		if skipScanPath(relative) {
			return nil
		}
		sources = append(sources, SourceDocument{RelativePath: relative, Content: bytes.Clone(content)})
		return nil
	})
	if err != nil {
		return Inventory{
			ByID: map[string]Entry{},
			Issues: []ScanIssue{{
				Kind:         IssueMalformed,
				RelativePath: rootRelative,
				Err:          errors.New("Markdown tree cannot be scanned safely"),
			}},
		}
	}
	return BuildInventory(sources, goos, caseMode)
}

// BuildInventory parses sources in deterministic slash-path order. Every
// member of a duplicate-ID or normalized-path group is isolated.
func BuildInventory(sources []SourceDocument, goos string, caseMode platform.CaseMode) Inventory {
	ordered := make([]SourceDocument, len(sources))
	for index, source := range sources {
		ordered[index] = SourceDocument{RelativePath: source.RelativePath, Content: bytes.Clone(source.Content)}
	}
	sort.SliceStable(ordered, func(first, second int) bool {
		return ordered[first].RelativePath < ordered[second].RelativePath
	})

	type candidate struct {
		entry Entry
	}
	candidates := make([]candidate, 0, len(ordered))
	issues := make([]ScanIssue, 0)
	for _, source := range ordered {
		pathKey, err := platform.PathKey(goos, caseMode, source.RelativePath)
		if err != nil {
			issues = append(issues, malformedScanIssue(source.RelativePath))
			continue
		}
		document, err := Parse(source.RelativePath, source.Content)
		if err != nil {
			issues = append(issues, malformedScanIssue(source.RelativePath))
			continue
		}
		identity, err := document.Identity()
		if err != nil || !stableScanID.MatchString(identity.ID) {
			issues = append(issues, malformedScanIssue(source.RelativePath))
			continue
		}
		content := bytes.Clone(source.Content)
		candidates = append(candidates, candidate{entry: Entry{
			Identity:     identity,
			RelativePath: source.RelativePath,
			PathKey:      pathKey,
			Document:     document,
			Content:      content,
			ContentHash:  ContentHash(content),
		}})
	}

	byPath := make(map[string][]int, len(candidates))
	byID := make(map[string][]int, len(candidates))
	for index, candidate := range candidates {
		byPath[candidate.entry.PathKey] = append(byPath[candidate.entry.PathKey], index)
		byID[candidate.entry.Identity.ID] = append(byID[candidate.entry.Identity.ID], index)
	}
	excluded := make(map[int]struct{})
	for index, candidate := range candidates {
		if len(byPath[candidate.entry.PathKey]) > 1 {
			excluded[index] = struct{}{}
			issues = append(issues, ScanIssue{
				Kind:         IssuePathCollision,
				RelativePath: candidate.entry.RelativePath,
				EntityID:     candidate.entry.Identity.ID,
				Err:          errors.New("normalized path collision"),
			})
		}
		if len(byID[candidate.entry.Identity.ID]) > 1 {
			excluded[index] = struct{}{}
			issues = append(issues, ScanIssue{
				Kind:         IssueDuplicateID,
				RelativePath: candidate.entry.RelativePath,
				EntityID:     candidate.entry.Identity.ID,
				Err:          errors.New("duplicate entity identity"),
			})
		}
	}
	sort.SliceStable(issues, func(first, second int) bool {
		if issues[first].RelativePath != issues[second].RelativePath {
			return issues[first].RelativePath < issues[second].RelativePath
		}
		if issues[first].Kind != issues[second].Kind {
			return issues[first].Kind < issues[second].Kind
		}
		return issues[first].EntityID < issues[second].EntityID
	})

	inventory := Inventory{ByID: make(map[string]Entry), Issues: issues}
	for index, candidate := range candidates {
		if _, found := excluded[index]; found {
			continue
		}
		inventory.ByID[candidate.entry.Identity.ID] = candidate.entry
	}
	return inventory
}

func malformedScanIssue(relative string) ScanIssue {
	return ScanIssue{
		Kind:         IssueMalformed,
		RelativePath: relative,
		Err:          errors.New("Markdown entity is malformed"),
	}
}

func skipScanPath(relative string) bool {
	components := strings.Split(relative, "/")
	for _, component := range components {
		if component == ".obsidian" || component == "sync-conflicts" || strings.HasPrefix(component, ".") {
			return true
		}
	}
	base := path.Base(relative)
	if !strings.EqualFold(path.Ext(base), ".md") {
		return true
	}
	if strings.HasSuffix(base, strings.TrimPrefix(atomicfile.BackupPath("x"), "x")) || strings.HasPrefix(base, ".session-reviewer-") {
		return true
	}
	return false
}
