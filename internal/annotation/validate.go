package annotation

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validID(value string) bool                { return len(value) <= 256 && idRE.MatchString(value) }
func validText(value string, maximum int) bool { return len(value) <= maximum }

func Validate(store StoreRecord) error {
	if store.SchemaVersion != 1 || store.MinimumReaderVersion != "0.4.0" || !validID(store.ProjectID) {
		return errors.New("invalid annotation store identity")
	}
	if len(store.Annotations) > 65536 || len(store.ExtractionRuns) > 65536 {
		return errors.New("annotation store exceeds array limit")
	}
	runs := make(map[string]struct{}, len(store.ExtractionRuns))
	for index, run := range store.ExtractionRuns {
		if run.ProjectID != store.ProjectID || !validID(run.RunID) || !validID(run.ExtractorVersion) || !validID(run.PromptSchemaVersion) || !validText(run.CreatedAt, 128) || !validText(run.UpdatedAt, 128) || len(run.DependencyDigests) > 256 {
			return fmt.Errorf("invalid extraction run %d", index)
		}
		if _, exists := runs[run.RunID]; exists {
			return fmt.Errorf("duplicate extraction run %q", run.RunID)
		}
		runs[run.RunID] = struct{}{}
		switch run.Status {
		case "pending", "running", "completed", "failed", "cancelled":
		default:
			return fmt.Errorf("invalid extraction status %q", run.Status)
		}
		seenDigests := map[string]bool{}
		for _, digest := range run.DependencyDigests {
			if !digestRE.MatchString(digest) || seenDigests[digest] {
				return errors.New("invalid or duplicate extraction dependency digest")
			}
			seenDigests[digest] = true
		}
	}
	annotations := make(map[string]struct{}, len(store.Annotations))
	for index, annotation := range store.Annotations {
		if annotation.SchemaVersion != 1 || annotation.ProjectID != store.ProjectID || !validID(annotation.ID) || !validID(annotation.EntityID) || !validID(annotation.Field) || !validID(annotation.GenerationID) || !validID(annotation.AnalysisProfile) || !validID(annotation.AgentRunID) || !validText(annotation.Text, 4096) || annotation.Revision < 1 || !validText(annotation.CreatedAt, 128) || len(annotation.Dependencies) > 256 {
			return fmt.Errorf("invalid annotation %d", index)
		}
		if _, exists := annotations[annotation.ID]; exists {
			return fmt.Errorf("duplicate annotation %q", annotation.ID)
		}
		annotations[annotation.ID] = struct{}{}
		if _, exists := runs[annotation.AgentRunID]; !exists {
			return fmt.Errorf("annotation %q references missing extraction run", annotation.ID)
		}
		switch annotation.Status {
		case CandidatePending, CandidateIgnored, CandidateNotDecision, CandidateStale:
			if annotation.ConfirmedDecisionID != nil {
				return fmt.Errorf("candidate %q is not confirmed but has a decision", annotation.ID)
			}
		case CandidateConfirmed:
			if annotation.ConfirmedDecisionID == nil || !validID(*annotation.ConfirmedDecisionID) {
				return fmt.Errorf("confirmed candidate %q has no valid decision", annotation.ID)
			}
		default:
			return fmt.Errorf("invalid annotation status %q", annotation.Status)
		}
		dependencies := map[string]bool{}
		for _, dependency := range annotation.Dependencies {
			if (dependency.Kind != "observation" && dependency.Kind != "session_view") || !validID(dependency.RevisionID) || !digestRE.MatchString(dependency.Digest) {
				return errors.New("invalid annotation dependency")
			}
			key := dependency.Kind + "\x00" + dependency.RevisionID
			if dependencies[key] {
				return errors.New("duplicate annotation dependency")
			}
			dependencies[key] = true
		}
	}
	return nil
}

func Parse(data []byte) (StoreRecord, error) {
	var store StoreRecord
	if err := strictjson.Decode(data, &store); err != nil {
		return store, err
	}
	if err := Validate(store); err != nil {
		return store, strictjson.NewRejection(strictjson.CodeContractInvalid, err)
	}
	return store, nil
}

func Render(store StoreRecord) ([]byte, error) {
	normalize(&store)
	if err := Validate(store); err != nil {
		return nil, err
	}
	body, err := strictjson.Encode(store)
	if err != nil {
		return nil, err
	}
	parsed, err := Parse(body)
	if err != nil {
		return nil, fmt.Errorf("rendered annotation store failed validation: %w", err)
	}
	if !reflect.DeepEqual(store, parsed) {
		return nil, errors.New("rendered annotation store changed semantic value")
	}
	return body, nil
}

func normalize(store *StoreRecord) {
	if store.Annotations == nil {
		store.Annotations = []Annotation{}
	}
	if store.ExtractionRuns == nil {
		store.ExtractionRuns = []Run{}
	}
	for index := range store.Annotations {
		if store.Annotations[index].Dependencies == nil {
			store.Annotations[index].Dependencies = []Dependency{}
		}
	}
	for index := range store.ExtractionRuns {
		if store.ExtractionRuns[index].DependencyDigests == nil {
			store.ExtractionRuns[index].DependencyDigests = []string{}
		}
	}
}
