package syncdoc

import (
	"bytes"
	"errors"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/neomei/SessionReviewer/internal/ledger"
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

type analyzedSource struct {
	source  SourceDocument
	pathKey string
	entry   *Entry
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
		if derivedDocument(relative, rootRelative) {
			return nil
		}
		if err := consume(len(content)); err != nil {
			return err
		}
		sources = append(sources, SourceDocument{RelativePath: relative, Content: content})
		return nil
	}, func(relative string) error {
		if derivedDocument(relative, rootRelative) {
			return nil
		}
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

func derivedDocument(relative, rootRelative string) bool {
	prefix := strings.TrimSuffix(rootRelative, "/") + "/"
	within := strings.TrimPrefix(relative, prefix)
	if within == "diagrams" || strings.HasPrefix(within, "diagrams/") {
		return true
	}
	rootBase := path.Base(strings.TrimSuffix(rootRelative, "/"))
	if rootBase == "decisions" || rootBase == "open-loops" || rootBase == "sessions" {
		within = path.Join(rootBase, within)
	}
	return ledger.IsStandaloneDerivedPath(path.Join("docs/session-review", within))
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
	if inventory, v2 := compactV2Inventory(ordered, analyzed); v2 {
		if inventory != nil {
			return *inventory
		}
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

func compactV2Inventory(ordered []SourceDocument, analyzed []analyzedSource) (*Inventory, bool) {
	v2 := false
	for _, source := range ordered {
		if sourceDeclaresV2(source.Content) {
			v2 = true
			break
		}
	}
	for _, source := range analyzed {
		if source.entry == nil {
			continue
		}
		if _, ok := source.entry.Document.v2EntityType(); ok {
			v2 = true
		}
	}
	if !v2 {
		return nil, false
	}
	valid := len(ordered) == 2 && len(analyzed) == 2
	seen := make(map[string]Entry, 2)
	parent := ""
	projectID := ""
	for _, source := range analyzed {
		if source.entry == nil {
			valid = false
			continue
		}
		entry := *source.entry
		entityType, ok := entry.Document.v2EntityType()
		if !ok {
			valid = false
			continue
		}
		wantName, wantType := "", ""
		switch entry.Identity.ID {
		case "project-overview":
			wantName, wantType = "项目回顾.md", "project_review"
		case "project-history":
			wantName, wantType = "项目历史.md", "project_history"
		default:
			valid = false
		}
		if entityType != wantType || path.Base(entry.RelativePath) != wantName {
			valid = false
		}
		currentParent := path.Dir(entry.RelativePath)
		if parent == "" {
			parent = currentParent
		} else if parent != currentParent {
			valid = false
		}
		if projectID == "" {
			projectID = entry.Identity.ProjectID
		} else if projectID != entry.Identity.ProjectID {
			valid = false
		}
		if _, duplicate := seen[entry.Identity.ID]; duplicate {
			valid = false
		}
		seen[entry.Identity.ID] = entry
	}
	valid = valid && len(seen) == 2 && seen["project-overview"].Identity.ID != "" && seen["project-history"].Identity.ID != ""
	if valid {
		return nil, true
	}
	issues := make([]ScanIssue, 0, len(ordered))
	for _, source := range ordered {
		issues = append(issues, malformedScanIssue(source.RelativePath))
	}
	sortScanIssues(issues)
	return &Inventory{ByID: map[string]Entry{}, Issues: issues}, true
}

func sourceDeclaresV2(content []byte) bool {
	frontmatter, _, err := splitFrontmatter(content)
	if err != nil {
		return false
	}
	mapping, err := decodeFrontmatter(frontmatter)
	if err != nil {
		return false
	}
	schema, ok := mappingValue(mapping, "schema_version")
	if !ok {
		return false
	}
	version, err := positiveInt(schema)
	return err == nil && (version == 2 || version == 3)
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
