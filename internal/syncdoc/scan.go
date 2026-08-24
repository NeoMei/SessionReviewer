package syncdoc

import (
	"bytes"
	"errors"
	"regexp"
	"sort"

	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
)

var stableScanID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

const (
	maxScanMarkdownFiles = 4_096
	maxScanMarkdownBytes = 64 << 20
)

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
	return scanWithLimits(root, rootRelative, goos, caseMode, maxScanMarkdownFiles, maxScanMarkdownBytes)
}

func scanWithLimits(root *pathguard.Directory, rootRelative, goos string, caseMode platform.CaseMode, maxFiles, maxBytes int) Inventory {
	sources := make([]SourceDocument, 0)
	files, totalBytes := 0, 0
	consume := func(size int) error {
		if maxFiles < 0 || maxBytes < 0 || files >= maxFiles || size > maxBytes-totalBytes {
			return errors.New("Markdown scan exceeds aggregate budget")
		}
		files++
		totalBytes += size
		return nil
	}
	err := root.WalkMarkdownIsolated(rootRelative, func(relative string, content []byte) error {
		if err := consume(len(content)); err != nil {
			return err
		}
		sources = append(sources, SourceDocument{RelativePath: relative, Content: content})
		return nil
	}, func(relative string) error {
		if err := consume(0); err != nil {
			return err
		}
		sources = append(sources, SourceDocument{RelativePath: relative})
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

	type analyzedSource struct {
		source  SourceDocument
		pathKey string
		entry   *Entry
	}
	analyzed := make([]analyzedSource, 0, len(ordered))
	issues := make([]ScanIssue, 0)
	byPath := make(map[string][]int, len(ordered))
	for _, source := range ordered {
		pathKey, err := platform.PathKey(goos, caseMode, source.RelativePath)
		if err != nil {
			issues = append(issues, malformedScanIssue(source.RelativePath))
			continue
		}
		index := len(analyzed)
		analyzed = append(analyzed, analyzedSource{source: source, pathKey: pathKey})
		byPath[pathKey] = append(byPath[pathKey], index)
	}

	for index := range analyzed {
		source := analyzed[index].source
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
		entry := Entry{
			Identity:     identity,
			RelativePath: source.RelativePath,
			PathKey:      analyzed[index].pathKey,
			Document:     document,
			Content:      content,
			ContentHash:  ContentHash(content),
		}
		analyzed[index].entry = &entry
	}

	byID := make(map[string][]int, len(analyzed))
	for index, source := range analyzed {
		if source.entry != nil {
			byID[source.entry.Identity.ID] = append(byID[source.entry.Identity.ID], index)
		}
	}
	excluded := make(map[int]struct{})
	for index, source := range analyzed {
		entityID := ""
		if source.entry != nil {
			entityID = source.entry.Identity.ID
		}
		if len(byPath[source.pathKey]) > 1 {
			excluded[index] = struct{}{}
			issues = append(issues, ScanIssue{
				Kind:         IssuePathCollision,
				RelativePath: source.source.RelativePath,
				EntityID:     entityID,
				Err:          errors.New("normalized path collision"),
			})
		}
		if source.entry != nil && len(byID[source.entry.Identity.ID]) > 1 {
			excluded[index] = struct{}{}
			issues = append(issues, ScanIssue{
				Kind:         IssueDuplicateID,
				RelativePath: source.entry.RelativePath,
				EntityID:     source.entry.Identity.ID,
				Err:          errors.New("duplicate entity identity"),
			})
		}
	}
	sortScanIssues(issues)

	inventory := Inventory{ByID: make(map[string]Entry), Issues: issues}
	for index, source := range analyzed {
		if _, found := excluded[index]; found {
			continue
		}
		if source.entry != nil {
			inventory.ByID[source.entry.Identity.ID] = *source.entry
		}
	}
	return inventory
}

func sortScanIssues(issues []ScanIssue) {
	sort.SliceStable(issues, func(first, second int) bool {
		if issues[first].RelativePath != issues[second].RelativePath {
			return issues[first].RelativePath < issues[second].RelativePath
		}
		if issues[first].Kind != issues[second].Kind {
			return issues[first].Kind < issues[second].Kind
		}
		return issues[first].EntityID < issues[second].EntityID
	})
}

func malformedScanIssue(relative string) ScanIssue {
	return ScanIssue{
		Kind:         IssueMalformed,
		RelativePath: relative,
		Err:          errors.New("Markdown entity is malformed"),
	}
}
