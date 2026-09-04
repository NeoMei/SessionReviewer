package inspect

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var eventKinds = map[string]bool{
	"message": true, "tool_call": true, "tool_result": true, "cwd_change": true,
	"usage": true, "skip": true, "file_change": true, "command": true,
	"verification": true, "error": true, "artifact": true,
}

func validID(value string) bool { return len(value) <= 256 && idRE.MatchString(value) }
func validCoverage(coverage Coverage) bool {
	return coverage.Indexed+coverage.Collapsed+coverage.Unprojected+coverage.Undecodable+coverage.Truncated == coverage.Seen
}

func validateIdentity(schemaVersion int, reader, project, provider, session, generation, digest string) error {
	if schemaVersion != 1 || reader != "0.4.0" || !validID(project) || !validID(provider) || !validID(session) || !validID(generation) || !digestRE.MatchString(digest) {
		return errors.New("invalid inspection identity")
	}
	return nil
}

func validateEntry(entry Entry) error {
	if len(entry.OccurredAt) > 128 || entry.Sequence == 0 || !validID(entry.RevisionID) || len(entry.Text) > 512 || len(entry.SourceRevisionIDs) > 64 {
		return errors.New("invalid summary entry")
	}
	seen := map[string]bool{}
	for _, revision := range entry.SourceRevisionIDs {
		if !validID(revision) || seen[revision] {
			return errors.New("invalid or duplicate source revision")
		}
		seen[revision] = true
	}
	return nil
}

func validateBlock(block Block) error {
	if block.Shown > block.Total || block.Omitted != block.Total-block.Shown || uint64(len(block.Items)) != block.Shown || len(block.Items) > 32 || !validCoverage(block.Coverage) {
		return errors.New("summary block does not reconcile")
	}
	for _, entry := range block.Items {
		if err := validateEntry(entry); err != nil {
			return err
		}
	}
	if !sort.SliceIsSorted(block.Items, func(i, j int) bool { return entryLess(block.Items[i], block.Items[j]) }) {
		return errors.New("summary items are not in canonical order")
	}
	return nil
}

func validateErrorBlock(block ErrorBlock) error {
	if block.Shown > block.Total || block.Omitted != block.Total-block.Shown || uint64(len(block.Items)) != block.Shown || len(block.Items) > 32 || !validCoverage(block.Coverage) {
		return errors.New("summary error block does not reconcile")
	}
	entries := make([]Entry, len(block.Items))
	for index, item := range block.Items {
		if !validID(item.Code) {
			return errors.New("invalid error code")
		}
		entries[index] = Entry{OccurredAt: item.OccurredAt, Sequence: item.Sequence, RevisionID: item.RevisionID, Text: item.Text, SourceRevisionIDs: item.SourceRevisionIDs}
		if err := validateEntry(entries[index]); err != nil {
			return err
		}
	}
	if !sort.SliceIsSorted(entries, func(i, j int) bool { return entryLess(entries[i], entries[j]) }) {
		return errors.New("error items are not in canonical order")
	}
	return nil
}

func entryLess(left, right Entry) bool {
	if left.OccurredAt != right.OccurredAt {
		return left.OccurredAt < right.OccurredAt
	}
	if left.Sequence != right.Sequence {
		return left.Sequence < right.Sequence
	}
	return left.RevisionID < right.RevisionID
}

func ValidateSummary(summary SessionSummary) error {
	if err := validateIdentity(summary.SchemaVersion, summary.MinimumReaderVersion, summary.ProjectID, summary.Provider, summary.SessionID, summary.GenerationID, summary.SessionViewDigest); err != nil {
		return err
	}
	for _, block := range []Block{summary.PhaseBoundaries, summary.KeyOperations, summary.VerificationResults, summary.UnresolvedQuestions} {
		if err := validateBlock(block); err != nil {
			return err
		}
	}
	if err := validateErrorBlock(summary.Errors); err != nil {
		return err
	}
	if !validID(summary.Rules.RuleID) || !validID(summary.Rules.RuleVersion) || len(summary.Rules.DependencyDigests) > 128 {
		return errors.New("invalid summary rules")
	}
	seenDigests := map[string]bool{}
	for _, digest := range summary.Rules.DependencyDigests {
		if !digestRE.MatchString(digest) || seenDigests[digest] {
			return errors.New("invalid or duplicate rule dependency digest")
		}
		seenDigests[digest] = true
	}
	if !validCoverage(summary.Coverage) {
		return errors.New("summary coverage does not reconcile")
	}
	return nil
}

func ValidateEventPage(page SessionEventPage) error {
	if err := validateIdentity(page.SchemaVersion, page.MinimumReaderVersion, page.ProjectID, page.Provider, page.SessionID, page.GenerationID, page.SessionViewDigest); err != nil {
		return err
	}
	if page.RangeStart > page.RangeEnd || page.RangeEnd > page.Total || uint64(len(page.Items)) != page.RangeEnd-page.RangeStart || len(page.Items) > 100 {
		return errors.New("event page range does not reconcile")
	}
	for _, cursor := range []*string{page.PreviousCursor, page.NextCursor, page.FirstCursor, page.LastCursor} {
		if cursor != nil && len(*cursor) > 4096 {
			return errors.New("event cursor is too large")
		}
	}
	if page.Total == 0 && (page.RangeStart != 0 || page.RangeEnd != 0 || page.PreviousCursor != nil || page.NextCursor != nil || page.FirstCursor != nil || page.LastCursor != nil) {
		return errors.New("empty event page cannot have a range or cursors")
	}
	if !validCoverage(page.Coverage) {
		return errors.New("event page coverage does not reconcile")
	}
	if page.Total != page.Coverage.Indexed {
		return errors.New("event page total does not match indexed coverage")
	}
	for index, item := range page.Items {
		if !eventKinds[item.Kind] || len(item.Excerpt) > 512 || !validID(item.RevisionID) || item.Sequence == 0 || len(item.OccurredAt) > 128 {
			return fmt.Errorf("invalid event item %d", index)
		}
	}
	if !sort.SliceIsSorted(page.Items, func(i, j int) bool {
		left, right := page.Items[i], page.Items[j]
		if left.OccurredAt != right.OccurredAt {
			return left.OccurredAt < right.OccurredAt
		}
		if left.Sequence != right.Sequence {
			return left.Sequence < right.Sequence
		}
		return left.RevisionID < right.RevisionID
	}) {
		return errors.New("event items are not in canonical order")
	}
	return nil
}

func ParseSummary(data []byte) (SessionSummary, error) {
	var summary SessionSummary
	if err := strictjson.Decode(data, &summary); err != nil {
		return summary, err
	}
	if err := ValidateSummary(summary); err != nil {
		return summary, err
	}
	return summary, nil
}

func ParseEventPage(data []byte) (SessionEventPage, error) {
	var page SessionEventPage
	if err := strictjson.Decode(data, &page); err != nil {
		return page, err
	}
	if err := ValidateEventPage(page); err != nil {
		return page, err
	}
	return page, nil
}

func RenderSummary(summary SessionSummary) ([]byte, error) {
	normalizeSummary(&summary)
	if err := ValidateSummary(summary); err != nil {
		return nil, err
	}
	body, err := strictjson.Encode(summary)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseSummary(body)
	if err != nil || !reflect.DeepEqual(summary, parsed) {
		return nil, errors.New("rendered session summary changed or failed validation")
	}
	return body, nil
}

func RenderEventPage(page SessionEventPage) ([]byte, error) {
	if page.Items == nil {
		page.Items = []EventItem{}
	}
	if err := ValidateEventPage(page); err != nil {
		return nil, err
	}
	body, err := strictjson.Encode(page)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseEventPage(body)
	if err != nil || !reflect.DeepEqual(page, parsed) {
		return nil, errors.New("rendered event page changed or failed validation")
	}
	return body, nil
}

func normalizeSummary(summary *SessionSummary) {
	blocks := []*Block{&summary.PhaseBoundaries, &summary.KeyOperations, &summary.VerificationResults, &summary.UnresolvedQuestions}
	for _, block := range blocks {
		if block.Items == nil {
			block.Items = []Entry{}
		}
		for i := range block.Items {
			if block.Items[i].SourceRevisionIDs == nil {
				block.Items[i].SourceRevisionIDs = []string{}
			}
		}
	}
	if summary.Errors.Items == nil {
		summary.Errors.Items = []ErrorEntry{}
	}
	for i := range summary.Errors.Items {
		if summary.Errors.Items[i].SourceRevisionIDs == nil {
			summary.Errors.Items[i].SourceRevisionIDs = []string{}
		}
	}
	if summary.Rules.DependencyDigests == nil {
		summary.Rules.DependencyDigests = []string{}
	}
}
