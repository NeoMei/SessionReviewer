package inspect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/memorystore"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

type SearchRequest struct {
	DataRoot, ProjectID, ExpectedGenerationID, QueryKind, Query, Cursor string
	Limit                                                               int
}
type SearchItem struct {
	Provider  string `json:"provider"`
	SessionID string `json:"session_id"`
	MatchKind string `json:"match_kind"`
}
type SearchPage struct {
	SchemaVersion  int          `json:"schema_version"`
	ProjectID      string       `json:"project_id"`
	GenerationID   string       `json:"generation_id"`
	Total          int          `json:"total"`
	Items          []SearchItem `json:"items"`
	NextCursor     *string      `json:"next_cursor"`
	PreviousCursor *string      `json:"previous_cursor"`
}

// LoadSessionSearch reads authenticated, published typed observations only.
// Queries are literal normalized text, never paths or executable expressions.
func LoadSessionSearch(ctx context.Context, request SearchRequest) (SearchPage, error) {
	invalid := func() (SearchPage, error) {
		return SearchPage{}, publicError(CodeInvalidArgument, "published search state is unavailable or invalid")
	}
	query := normalizeSearchText(request.Query)
	if ctx == nil || ctx.Err() != nil || !filepath.IsAbs(request.DataRoot) || filepath.Clean(request.DataRoot) != request.DataRoot || request.Limit < 1 || request.Limit > 100 || request.ProjectID == "" || request.ExpectedGenerationID == "" || query == "" || len(request.Query) > 256 || !utf8.ValidString(request.Query) || len(request.Cursor) > 4096 || (request.QueryKind != "branch" && request.QueryKind != "file" && request.QueryKind != "error") {
		return invalid()
	}
	cfg, err := config.Load(filepath.Join(request.DataRoot, "config.toml"))
	if err != nil {
		return invalid()
	}
	mapping, found := cfg.ProjectByID(request.ProjectID)
	if !found {
		return invalid()
	}
	binding, err := projectidentity.Resolve(mapping, mapping.Root, runtime.GOOS)
	if err != nil || binding.ProjectID != request.ProjectID {
		return invalid()
	}
	store, err := memorystore.OpenReadOnly(request.DataRoot, request.ProjectID)
	if err != nil {
		return invalid()
	}
	defer store.Close()
	id, manifest, err := store.LoadPublishedContext(ctx)
	if err != nil {
		return invalid()
	}
	if id != request.ExpectedGenerationID {
		return SearchPage{}, publicError(CodeGenerationMismatch, "published generation does not match the expected generation")
	}
	body, err := store.LoadObjectContext(ctx, memorystore.ObjectSessionIndex, manifest.SessionIndexDigest)
	if err != nil {
		return invalid()
	}
	index, err := sessionindex.Parse(body)
	if err != nil || index.ProjectID != request.ProjectID || index.GenerationID != id || index.Digest != manifest.SessionIndexDigest || index.ProjectViewDigest != manifest.ProjectViewDigest {
		return invalid()
	}
	matches := []SearchItem{}
	keyMaterial := []byte("session-reviewer/search/v1")
	for _, entry := range index.Sessions {
		if ctx.Err() != nil {
			return invalid()
		}
		if entry.SessionViewDigest == nil {
			continue
		}
		dep, ok := selectedDependency(manifest, entry.Provider, entry.SessionID)
		if !ok || dep.Digest != *entry.SessionViewDigest {
			return invalid()
		}
		bytes, err := store.LoadObjectContext(ctx, memorystore.ObjectSessionView, dep.Digest)
		if err != nil {
			return invalid()
		}
		var view memory.SessionView
		if json.Unmarshal(bytes, &view) != nil || view.Digest != dep.Digest || view.ProjectID != request.ProjectID || view.Provider != entry.Provider || view.SessionID != entry.SessionID {
			return invalid()
		}
		rows, err := loadSelectedRevisions(ctx, store, view)
		if err != nil {
			return invalid()
		}
		keyMaterial = append(keyMaterial, cursorAuthenticationKey(view)...)
		if searchMatches(rows, request.QueryKind, query) {
			matches = append(matches, SearchItem{Provider: entry.Provider, SessionID: entry.SessionID, MatchKind: request.QueryKind})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Provider != matches[j].Provider {
			return matches[i].Provider < matches[j].Provider
		}
		return matches[i].SessionID < matches[j].SessionID
	})
	key := sha256.Sum256(keyMaterial)
	qsum := sha256.Sum256([]byte(request.QueryKind + "\x00" + query))
	cursorRequest := EventPageRequest{ProjectID: request.ProjectID, Provider: "session-search", SessionID: hex.EncodeToString(qsum[:]), ExpectedGenerationID: id, Limit: request.Limit, Cursor: request.Cursor}
	offset, err := eventPageOffset(cursorRequest, index.Digest, key[:], uint64(len(matches)))
	if err != nil {
		return SearchPage{}, err
	}
	end := int(offset) + request.Limit
	if end > len(matches) {
		end = len(matches)
	}
	page := SearchPage{SchemaVersion: 1, ProjectID: request.ProjectID, GenerationID: id, Total: len(matches), Items: append([]SearchItem{}, matches[int(offset):end]...)}
	if end < len(matches) {
		page.NextCursor = cursorPointer(cursorRequest, index.Digest, key[:], uint64(end))
	}
	if offset > 0 {
		page.PreviousCursor = cursorPointer(cursorRequest, index.Digest, key[:], offset-uint64(request.Limit))
	}
	if projectidentity.Reauthenticate(binding) != nil {
		return invalid()
	}
	if err := recheckPublished(ctx, store, id, manifest); err != nil {
		return SearchPage{}, err
	}
	return page, nil
}

func normalizeSearchText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
func searchMatches(rows []memory.ObservationRevision, kind, query string) bool {
	query = normalizeSearchText(query)
	for _, row := range rows {
		candidates := []string{}
		switch kind {
		case "branch":
			candidates = append(candidates, row.Fields["branch"])
		case "file":
			if row.Key.Kind == "file" {
				candidates = append(candidates, row.Object, row.Fields["path"])
			}
		case "error":
			if row.Key.Kind == "error" {
				candidates = append(candidates, row.Object, row.Fields["code"], row.Excerpt)
			}
		}
		for _, value := range candidates {
			if strings.Contains(normalizeSearchText(value), query) {
				return true
			}
		}
	}
	return false
}
