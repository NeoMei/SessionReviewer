package reviewv2

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/ledger"
)

type HumanPatchWire struct {
	EntityID          string   "json:\"entity_id\""
	Field             string   "json:\"field\""
	Operation         string   "json:\"operation\""
	Value             string   "json:\"value,omitempty\""
	Values            []string "json:\"values,omitempty\""
	BaseGeneratedHash string   "json:\"base_generated_hash\""
}

type GeneratedBaselineWire struct {
	GenerationID  string   "json:\"generation_id\""
	EntityID      string   "json:\"entity_id\""
	Field         string   "json:\"field\""
	Value         string   "json:\"value,omitempty\""
	Values        []string "json:\"values,omitempty\""
	GeneratedHash string   "json:\"generated_hash\""
}

type MachineLedgerV3 struct {
	SchemaVersion        int                       "json:\"schema_version\""
	MinimumWriterVersion string                    "json:\"minimum_writer_version\""
	ProjectID            string                    "json:\"project_id\""
	GenerationID         string                    "json:\"generation_id\""
	ProjectViewDigest    string                    "json:\"project_view_digest\""
	AcceptedRevision     int                       "json:\"accepted_revision\""
	ReviewSHA256         string                    "json:\"review_sha256\""
	HistorySHA256        string                    "json:\"history_sha256\""
	LastSuccessfulSync   string                    "json:\"last_successful_sync,omitempty\""
	Accounting           accounting.ProjectSummary "json:\"accounting\""
	Sessions             []ledger.SessionReport    "json:\"sessions\""
	HumanPatches         []HumanPatchWire          "json:\"human_patches\""
	GeneratedBaselines   []GeneratedBaselineWire   "json:\"generated_baselines\""
	LegacyCompatibility  LegacyCompatibility       "json:\"legacy_compatibility\""
}

type StateV3 struct {
	Review  Review
	Events  []Event
	Machine MachineLedgerV3
}

type AcceptedV3 struct {
	State StateV3

	projectRoot string
	projectInfo os.FileInfo
	files       map[string]acceptedFile
}

type machineLedgerV3Wire MachineLedgerV3

type ErrWriterUpgradeRequired struct {
	ProjectRoot string
}

func (err *ErrWriterUpgradeRequired) Error() string {
	if err == nil {
		return "review projection writer upgrade required"
	}
	return fmt.Sprintf("review projection writer upgrade required for %q", err.ProjectRoot)
}

func ParseMachineLedgerV3(body []byte) (MachineLedgerV3, error) {
	if len(body) > MaxMachineLedgerBytes {
		return MachineLedgerV3{}, fmt.Errorf("machine ledger exceeds %d bytes", MaxMachineLedgerBytes)
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return MachineLedgerV3{}, err
	}
	if err := rejectInexactMachineLedgerV3Fields(body); err != nil {
		return MachineLedgerV3{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var wire machineLedgerV3Wire
	if err := decoder.Decode(&wire); err != nil {
		return MachineLedgerV3{}, fmt.Errorf("decode machine ledger: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return MachineLedgerV3{}, err
	}
	value := MachineLedgerV3(wire)
	if err := validateMachineLedgerV3(value); err != nil {
		return MachineLedgerV3{}, err
	}
	return value, nil
}

func rejectInexactMachineLedgerV3Fields(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	cache := make(map[reflect.Type]map[string]reflect.Type)
	if err := scanExactJSONFields(decoder, reflect.TypeOf(machineLedgerV3Wire{}), "$", cache); err != nil {
		return fmt.Errorf("decode machine ledger: %w", err)
	}
	return requireJSONEOF(decoder)
}

func RenderMachineLedgerV3(value MachineLedgerV3) ([]byte, error) {
	value = normalizeMachineLedgerV3(value)
	if err := validateMachineLedgerV3(value); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(machineLedgerV3Wire(value)); err != nil {
		return nil, fmt.Errorf("encode machine ledger: %w", err)
	}
	if output.Len() > MaxMachineLedgerBytes {
		return nil, fmt.Errorf("machine ledger exceeds %d bytes", MaxMachineLedgerBytes)
	}
	return output.Bytes(), nil
}

func LoadV3(projectRoot string) (AcceptedV3, error) {
	directory, err := openReviewRoot(projectRoot, nil)
	if err != nil {
		return AcceptedV3{}, err
	}
	defer directory.Close()
	files := make(map[string]acceptedFile, 3)
	read := func(relative string, maximum int64) ([]byte, error) {
		body, perm, err := readStableReviewFile(directory, relative, maximum)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", relative, err)
		}
		files[relative] = acceptedFile{body: append([]byte(nil), body...), perm: perm}
		return body, nil
	}
	reviewBody, err := read(ReviewRelativePath, MaxDocumentBytes)
	if err != nil {
		return AcceptedV3{}, err
	}
	historyBody, err := read(HistoryRelativePath, MaxDocumentBytes)
	if err != nil {
		return AcceptedV3{}, err
	}
	machineBody, err := read(MachineLedgerRelativePath, MaxMachineLedgerBytes)
	if err != nil {
		return AcceptedV3{}, err
	}
	review, err := ParseReview(reviewBody)
	if err != nil {
		return AcceptedV3{}, err
	}
	history, err := ParseHistory(historyBody)
	if err != nil {
		return AcceptedV3{}, err
	}
	machine, err := ParseMachineLedgerV3(machineBody)
	if err != nil {
		return AcceptedV3{}, err
	}
	if review.Model.ProjectID != history.ProjectID || review.Model.ProjectID != machine.ProjectID ||
		review.Model.Revision != history.Revision || review.Model.Revision != machine.AcceptedRevision {
		return AcceptedV3{}, errors.New("v3 projection identities do not match")
	}
	if review.Model.GenerationID != machine.GenerationID || history.GenerationID != machine.GenerationID ||
		review.Model.MinimumWriterVersion != machine.MinimumWriterVersion || history.MinimumWriterVersion != machine.MinimumWriterVersion {
		return AcceptedV3{}, errors.New("v3 projection writer or generation identities do not match")
	}
	for _, event := range history.Events {
		if event.GenerationID != machine.GenerationID {
			return AcceptedV3{}, errors.New("history event generation does not match machine ledger")
		}
	}
	if err := revalidateLoadedFiles(directory, files); err != nil {
		return AcceptedV3{}, err
	}
	return AcceptedV3{
		State:       StateV3{Review: review.Model, Events: history.Events, Machine: machine},
		projectRoot: projectRoot, projectInfo: directory.Info(), files: files,
	}, nil
}

func RenderReviewV3(value Review) ([]byte, error) {
	if !validGenerationID(value.GenerationID) {
		return nil, errors.New("render review v3: invalid generation ID")
	}
	generationID := value.GenerationID
	value.GenerationID, value.MinimumWriterVersion = "", ""
	body, err := RenderReview(value)
	if err != nil {
		return nil, err
	}
	return upgradeMarkdownToV3(body, generationID)
}

func RenderHistoryV3(projectID string, revision int, generationID string, events []Event) ([]byte, error) {
	if !validGenerationID(generationID) {
		return nil, errors.New("render history v3: invalid generation ID")
	}
	body, err := RenderHistory(projectID, revision, events)
	if err != nil {
		return nil, err
	}
	return upgradeMarkdownToV3(body, generationID)
}

func upgradeMarkdownToV3(body []byte, generationID string) ([]byte, error) {
	old := fmt.Sprintf("schema_version: %d\n", LegacySchemaVersion)
	next := fmt.Sprintf("schema_version: %d\nminimum_writer_version: %s\ngeneration_id: %s\n", SchemaVersion, MinimumWriterVersion, generationID)
	body = bytes.Replace(body, []byte(old), []byte(next), 1)
	if !bytes.Contains(body, []byte("minimum_writer_version: "+MinimumWriterVersion)) || !bytes.Contains(body, []byte("generation_id: "+generationID)) {
		return nil, errors.New("render v3: legacy frontmatter identity was not upgraded")
	}
	return body, nil
}

func normalizeMachineLedgerV3(value MachineLedgerV3) MachineLedgerV3 {
	value.Sessions = append([]ledger.SessionReport{}, value.Sessions...)
	sort.Slice(value.Sessions, func(i, j int) bool {
		if value.Sessions[i].SessionID != value.Sessions[j].SessionID {
			return value.Sessions[i].SessionID < value.Sessions[j].SessionID
		}
		return value.Sessions[i].ID < value.Sessions[j].ID
	})
	value.HumanPatches = append([]HumanPatchWire{}, value.HumanPatches...)
	sort.Slice(value.HumanPatches, func(i, j int) bool {
		if value.HumanPatches[i].EntityID != value.HumanPatches[j].EntityID {
			return value.HumanPatches[i].EntityID < value.HumanPatches[j].EntityID
		}
		return value.HumanPatches[i].Field < value.HumanPatches[j].Field
	})
	value.GeneratedBaselines = append([]GeneratedBaselineWire{}, value.GeneratedBaselines...)
	sort.Slice(value.GeneratedBaselines, func(i, j int) bool {
		if value.GeneratedBaselines[i].EntityID != value.GeneratedBaselines[j].EntityID {
			return value.GeneratedBaselines[i].EntityID < value.GeneratedBaselines[j].EntityID
		}
		return value.GeneratedBaselines[i].Field < value.GeneratedBaselines[j].Field
	})
	value.Accounting.Models = append([]accounting.ProjectModelSummary{}, value.Accounting.Models...)
	value.LegacyCompatibility = cloneLegacyCompatibility(value.LegacyCompatibility)
	return value
}

func validateMachineLedgerV3(value MachineLedgerV3) error {
	if value.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported review projection schema version %d", value.SchemaVersion)
	}
	if !writerAtLeastV3(value.MinimumWriterVersion) {
		return fmt.Errorf("review projection writer version %q is below %s", value.MinimumWriterVersion, MinimumWriterVersion)
	}
	if !validStableID(value.ProjectID) || !strings.HasPrefix(value.ProjectID, "project-") {
		return errors.New("invalid v3 project identity")
	}
	if !validGenerationID(value.GenerationID) || !lowercaseSHA256.MatchString(value.ProjectViewDigest) ||
		!lowercaseSHA256.MatchString(value.ReviewSHA256) || !lowercaseSHA256.MatchString(value.HistorySHA256) {
		return errors.New("invalid v3 generation or digest identity")
	}
	if value.AcceptedRevision < 0 {
		return errors.New("v3 accepted revision must be nonnegative")
	}
	if value.Sessions == nil || value.HumanPatches == nil || value.GeneratedBaselines == nil ||
		value.Accounting.Models == nil || value.LegacyCompatibility.Timeline == nil ||
		value.LegacyCompatibility.Decisions == nil || value.LegacyCompatibility.OpenLoops == nil ||
		value.LegacyCompatibility.CurrentRisks == nil {
		return errors.New("v3 machine arrays must not be null or omitted")
	}
	if err := validateProjectSummaryScalars(value.Accounting); err != nil {
		return fmt.Errorf("invalid v3 accounting: %w", err)
	}
	sessionIDs := make(map[string]struct{}, len(value.Sessions))
	for _, session := range value.Sessions {
		if session.ProjectID != value.ProjectID {
			return fmt.Errorf("v3 session %q has a different project", session.ID)
		}
		if _, duplicate := sessionIDs[session.SessionID]; duplicate {
			return fmt.Errorf("duplicate v3 source session %q", session.SessionID)
		}
		sessionIDs[session.SessionID] = struct{}{}
		if err := accounting.ValidateStoredSessionAccounting(session.Accounting); err != nil {
			return fmt.Errorf("v3 session %q accounting: %w", session.ID, err)
		}
	}
	patches := make(map[string]struct{}, len(value.HumanPatches))
	for _, patch := range value.HumanPatches {
		key := patch.EntityID + "\x00" + patch.Field
		if _, duplicate := patches[key]; duplicate {
			return fmt.Errorf("duplicate v3 patch identity %s/%s", patch.EntityID, patch.Field)
		}
		patches[key] = struct{}{}
		if !validStableID(patch.EntityID) || patch.Field == "" || !lowercaseSHA256.MatchString(patch.BaseGeneratedHash) {
			return fmt.Errorf("invalid v3 patch identity %s/%s", patch.EntityID, patch.Field)
		}
		switch patch.Operation {
		case "set":
			if patch.Value == "" && len(patch.Values) == 0 {
				return errors.New("v3 set patch requires a value")
			}
		case "suppress", "restore_default":
			if patch.Value != "" || patch.Values != nil {
				return errors.New("v3 suppress/restore patch cannot carry a value")
			}
		default:
			return fmt.Errorf("invalid v3 patch operation %q", patch.Operation)
		}
	}
	baselines := make(map[string]struct{}, len(value.GeneratedBaselines))
	for _, baseline := range value.GeneratedBaselines {
		key := baseline.EntityID + "\x00" + baseline.Field
		if _, duplicate := baselines[key]; duplicate {
			return fmt.Errorf("duplicate v3 baseline identity %s/%s", baseline.EntityID, baseline.Field)
		}
		baselines[key] = struct{}{}
		if baseline.GenerationID != value.GenerationID || !validStableID(baseline.EntityID) || baseline.Field == "" || !lowercaseSHA256.MatchString(baseline.GeneratedHash) {
			return fmt.Errorf("invalid v3 baseline identity %s/%s", baseline.EntityID, baseline.Field)
		}
		if baseline.Value == "" && baseline.Values == nil {
			return errors.New("v3 baseline requires a generated value")
		}
		if baseline.GeneratedHash != generatedBaselineHash(value.GenerationID, baseline) {
			return errors.New("v3 baseline hash does not match its canonical generated value")
		}
	}
	return nil
}

func generatedBaselineHash(generationID string, baseline GeneratedBaselineWire) string {
	identity := struct {
		GenerationID string
		EntityID     string
		Field        string
		Value        string
		Values       []string
	}{generationID, baseline.EntityID, baseline.Field, baseline.Value, baseline.Values}
	body, err := json.Marshal(identity)
	if err != nil {
		return ""
	}
	return sha256Hex(body)
}

func validGenerationID(value string) bool {
	return validStableID(value) && strings.HasPrefix(value, "generation-")
}

func writerAtLeastV3(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	minimum := strings.Split(MinimumWriterVersion, ".")
	for index := 0; index < 3; index++ {
		current, currentErr := strconv.Atoi(parts[index])
		required, requiredErr := strconv.Atoi(minimum[index])
		if currentErr != nil || requiredErr != nil {
			return false
		}
		if current != required {
			return current > required
		}
	}
	return true
}
