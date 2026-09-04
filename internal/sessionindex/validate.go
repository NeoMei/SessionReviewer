package sessionindex

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var stateReasons = map[string]bool{
	"not_discovered": true, "duplicate_candidate": true, "freeze_terminal": true,
	"malformed_source_records": true, "unsupported_source_records": true,
	"source_missing": true, "source_unreadable": true, "source_ambiguous": true,
	"source_unsupported": true, "source_unavailable": true, "partial_observations": true,
	"unprojected_facts": true, "undecodable_facts": true, "scan_cancelled": true,
}

func validID(value string) bool                     { return len(value) <= 256 && idRE.MatchString(value) }
func validOptional(value *string, maximum int) bool { return value == nil || len(*value) <= maximum }
func validDigest(value *string) bool                { return value == nil || digestRE.MatchString(*value) }
func reconcileCoverage(coverage Coverage) bool {
	total, ok := checkedSum(coverage.Indexed, coverage.Collapsed, coverage.Unprojected, coverage.Undecodable, coverage.Truncated)
	return ok && total == coverage.Seen
}

func checkedSum(values ...uint64) (uint64, bool) {
	var total uint64
	for _, value := range values {
		if ^uint64(0)-total < value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func Validate(document Document) error {
	if document.SchemaVersion != 1 || document.MinimumReaderVersion != "0.4.0" ||
		!validID(document.ProjectID) || !validID(document.GenerationID) ||
		!digestRE.MatchString(document.Digest) || !digestRE.MatchString(document.ProjectViewDigest) ||
		document.GeneratedAt == "" || len(document.GeneratedAt) > 128 || document.SortVersion != SortVersion {
		return errors.New("invalid session index metadata")
	}
	if len(document.Sessions) > 65536 {
		return errors.New("too many sessions")
	}
	calculated := IndexCoverage{Total: uint64(len(document.Sessions))}
	keys := map[SessionKey]bool{}
	for index, entry := range document.Sessions {
		key := SessionKey{Provider: entry.Provider, SessionID: entry.SessionID}
		if !validID(entry.Provider) || !validID(entry.SessionID) || keys[key] {
			return fmt.Errorf("invalid or duplicate session identity at %d", index)
		}
		keys[key] = true
		switch entry.ProcessingState {
		case ProcessingComplete:
			calculated.Complete++
		case ProcessingPartial:
			calculated.Partial++
		case ProcessingError:
			calculated.Error++
		case ProcessingUnprocessed:
			calculated.Unprocessed++
		default:
			return fmt.Errorf("invalid processing state at %d", index)
		}
		switch entry.SourceAvailability {
		case "available":
			calculated.SourceAvailable++
		case "unavailable":
			calculated.SourceUnavailable++
		default:
			return fmt.Errorf("invalid source availability at %d", index)
		}
		if entry.StartedAt == "" || len(entry.StartedAt) > 128 || entry.EndedAt == "" || len(entry.EndedAt) > 128 || !validOptional(entry.SourceTerminalState, 64) {
			return fmt.Errorf("invalid session timestamps at %d", index)
		}
		calculated.StartedAtKnown++
		calculated.EndedAtKnown++
		if entry.UsageRecordDigest != nil {
			calculated.UsageKnown++
		}
		if len(entry.StateReasonCodes) > 64 {
			return fmt.Errorf("too many state reason codes at %d", index)
		}
		seenReasons := map[string]bool{}
		for _, reason := range entry.StateReasonCodes {
			if !stateReasons[reason] || seenReasons[reason] {
				return fmt.Errorf("invalid or duplicate state reason %q", reason)
			}
			seenReasons[reason] = true
		}
		if !reconcileCoverage(entry.Coverage) || entry.IndexedEventCount != entry.Coverage.Indexed {
			return fmt.Errorf("session %s coverage does not reconcile", entry.SessionID)
		}
		if !validDigest(entry.SessionViewDigest) || !validDigest(entry.UsageRecordDigest) || !validDigest(entry.SummaryDigest) ||
			!validOptional(entry.LastSeenGenerationID, 256) || !validOptional(entry.LastSuccessfulGenerationID, 256) {
			return fmt.Errorf("session %s has an invalid digest or generation reference", entry.SessionID)
		}
	}
	states, statesOK := checkedSum(document.Coverage.Complete, document.Coverage.Partial, document.Coverage.Error, document.Coverage.Unprocessed)
	sources, sourcesOK := checkedSum(document.Coverage.SourceAvailable, document.Coverage.SourceUnavailable)
	if calculated != document.Coverage || !statesOK || states != document.Coverage.Total || !sourcesOK || sources != document.Coverage.Total {
		return errors.New("index coverage does not reconcile")
	}
	if !sort.SliceIsSorted(document.Sessions, func(i, j int) bool {
		return less(document.Sessions[i], document.Sessions[j])
	}) {
		return errors.New("sessions are not in canonical order")
	}
	return nil
}

func less(left, right Entry) bool {
	if left.StartedAt != right.StartedAt {
		return left.StartedAt > right.StartedAt
	}
	if left.Provider != right.Provider {
		return left.Provider < right.Provider
	}
	return left.SessionID < right.SessionID
}

func Parse(data []byte) (Document, error) {
	var document Document
	if err := strictjson.Decode(data, &document); err != nil {
		return document, err
	}
	if err := Validate(document); err != nil {
		return document, strictjson.NewRejection(strictjson.CodeContractInvalid, err)
	}
	if !isZeroDigest(document.Digest) && CanonicalDigest(document) != document.Digest {
		return document, strictjson.NewRejection(strictjson.CodeContractInvalid, errors.New("session index digest mismatch"))
	}
	return document, nil
}

func Render(document Document) ([]byte, error) {
	normalize(&document)
	document.Digest = zeroDigest()
	if err := Validate(document); err != nil {
		return nil, err
	}
	document.Digest = CanonicalDigest(document)
	body, err := strictjson.Encode(document)
	if err != nil {
		return nil, err
	}
	parsed, err := Parse(body)
	if err != nil {
		return nil, fmt.Errorf("rendered session index failed validation: %w", err)
	}
	if !reflect.DeepEqual(document, parsed) {
		return nil, errors.New("rendered session index changed semantic value")
	}
	return body, nil
}

func CanonicalDigest(document Document) string {
	document.Digest = ""
	view := struct {
		SchemaVersion        int           `json:"schema_version"`
		MinimumReaderVersion string        `json:"minimum_reader_version"`
		ProjectID            string        `json:"project_id"`
		GenerationID         string        `json:"generation_id"`
		ProjectViewDigest    string        `json:"project_view_digest"`
		GeneratedAt          string        `json:"generated_at"`
		SortVersion          string        `json:"sort_version"`
		Coverage             IndexCoverage `json:"coverage"`
		Sessions             []Entry       `json:"sessions"`
	}{document.SchemaVersion, document.MinimumReaderVersion, document.ProjectID, document.GenerationID, document.ProjectViewDigest, document.GeneratedAt, document.SortVersion, document.Coverage, document.Sessions}
	body, err := strictjson.Encode(view)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func normalize(document *Document) {
	if document.Sessions == nil {
		document.Sessions = []Entry{}
	}
	for index := range document.Sessions {
		if document.Sessions[index].StateReasonCodes == nil {
			document.Sessions[index].StateReasonCodes = []string{}
		}
	}
}

func zeroDigest() string             { return "sha256:" + strings.Repeat("0", 64) }
func isZeroDigest(value string) bool { return value == zeroDigest() }
