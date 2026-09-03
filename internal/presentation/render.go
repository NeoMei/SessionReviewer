package presentation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/ledger"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

type FilePlan struct {
	Relative       string
	Expected       []byte
	ExpectedExists bool
	Desired        []byte
	Mode           fs.FileMode
}

type RenderPlan struct {
	ProjectID         string
	GenerationID      string
	ProjectViewDigest string
	Files             []FilePlan
	Patches           []Patch
	Baselines         []Baseline
}

const (
	GeneratedSectionRecentProgress = "recent-progress"
	GeneratedSectionModelUsage     = "model-usage"
	GeneratedSectionCustomContent  = "custom-content"
)

func Render(input ProjectInput, output ProjectOutput) (RenderPlan, error) {
	reviewBody, err := renderReviewBody(input, output)
	if err != nil {
		return RenderPlan{}, err
	}
	historyBody, err := reviewv2.RenderHistoryV3(input.ProjectView.ProjectID, input.Revision, input.GenerationID, output.Events)
	if err != nil {
		return RenderPlan{}, err
	}
	if current, exists := input.ExpectedFiles[reviewv2.ReviewRelativePath]; exists {
		reviewBody, err = preserveHumanMarkdown(reviewv2.ReviewRelativePath, current, reviewBody)
		if err != nil {
			return RenderPlan{}, fmt.Errorf("preserve review presentation: %w", err)
		}
	}
	if current, exists := input.ExpectedFiles[reviewv2.HistoryRelativePath]; exists {
		historyBody, err = preserveHumanMarkdown(reviewv2.HistoryRelativePath, current, historyBody)
		if err != nil {
			return RenderPlan{}, fmt.Errorf("preserve history presentation: %w", err)
		}
	}
	machineBody, err := renderMachineLedger(input, output, reviewBody, historyBody)
	if err != nil {
		return RenderPlan{}, err
	}
	plan := RenderPlan{
		ProjectID: input.ProjectView.ProjectID, GenerationID: input.GenerationID,
		ProjectViewDigest: input.ProjectView.Digest, Patches: clonePatchSet(output.ActivePatches),
		Baselines: append([]Baseline(nil), output.Baselines...),
		Files: []FilePlan{
			{Relative: reviewv2.ReviewRelativePath, Desired: reviewBody, Mode: 0o644},
			{Relative: reviewv2.HistoryRelativePath, Desired: historyBody, Mode: 0o644},
			{Relative: reviewv2.MachineLedgerRelativePath, Desired: machineBody, Mode: 0o600},
		},
	}
	for index := range plan.Files {
		expected, exists := input.ExpectedFiles[plan.Files[index].Relative]
		plan.Files[index].ExpectedExists = exists
		if exists {
			plan.Files[index].Expected = append([]byte(nil), expected...)
		}
	}
	if _, err := reviewv2.LoadV3Bytes(reviewBody, historyBody, machineBody); err != nil {
		return RenderPlan{}, err
	}
	machine, err := reviewv2.ParseMachineLedgerV3(machineBody)
	if err != nil || machine.ReviewSHA256 != sha256Hex(reviewBody) || machine.HistorySHA256 != sha256Hex(historyBody) {
		return RenderPlan{}, errors.New("render v3: generated baseline document hashes do not match exact rendered bytes")
	}
	return plan, nil
}

func preserveHumanMarkdown(relative string, current, desired []byte) ([]byte, error) {
	if relative == reviewv2.ReviewRelativePath {
		currentReview, currentErr := reviewv2.ParseReview(current)
		desiredReview, desiredErr := reviewv2.ParseReview(desired)
		if currentErr == nil && desiredErr == nil &&
			currentReview.Model.Name == currentReview.Model.ProjectID &&
			currentReview.Model.Name != desiredReview.Model.Name {
			current, currentErr = syncdoc.AlignRootHeading(relative, current, desired)
			if currentErr != nil {
				return nil, fmt.Errorf("align generated project title: %w", currentErr)
			}
		}
	}
	currentDocument, err := syncdoc.Parse(relative, current)
	if err != nil {
		return nil, fmt.Errorf("parse current document: %w", err)
	}
	desiredDocument, err := syncdoc.Parse(relative, desired)
	if err != nil {
		return nil, fmt.Errorf("parse desired document: %w", err)
	}
	currentUnits := currentDocument.SemanticUnits()
	desiredUnits := desiredDocument.SemanticUnits()

	base := desiredDocument
	merged := desiredUnits
	if sameMarkerSet(currentUnits, desiredUnits) {
		base = currentDocument
		merged = currentUnits
		for key, unit := range desiredUnits {
			if key.Kind != syncdoc.UnitPreamble {
				merged[key] = unit
			}
		}
		for key, unit := range currentUnits {
			if _, retained := desiredUnits[key]; retained {
				continue
			}
			if isPresentationMarkerUnit(key) || generatedProjectionUnit(unit) {
				delete(merged, key)
			}
		}
	} else {
		reserved := syncdoc.MachineReservedFields()
		for key := range syncdoc.ProposalOwnedFields() {
			reserved[key] = struct{}{}
		}
		for key, unit := range currentUnits {
			if _, generated := desiredUnits[key]; generated {
				continue
			}
			if key.Kind == syncdoc.UnitFrontmatter {
				if _, protected := reserved[key.Name]; protected {
					continue
				}
			} else if isPresentationMarkerUnit(key) || generatedProjectionUnit(unit) {
				continue
			}
			merged[key] = unit
		}
	}

	preserved, err := base.WithSemanticUnits(merged)
	if err != nil {
		return nil, err
	}
	return preserved.Render()
}

func sameMarkerSet(first, second syncdoc.UnitSet) bool {
	firstCount, secondCount := 0, 0
	for key := range first {
		if !isPresentationMarkerUnit(key) {
			continue
		}
		firstCount++
		if _, exists := second[key]; !exists {
			return false
		}
	}
	for key := range second {
		if !isPresentationMarkerUnit(key) {
			continue
		}
		secondCount++
		if _, exists := first[key]; !exists {
			return false
		}
	}
	return firstCount == secondCount
}

func isPresentationMarkerUnit(key syncdoc.UnitKey) bool {
	return key.Kind == syncdoc.UnitSection && strings.HasPrefix(key.Name, "session-reviewer/")
}

func generatedProjectionUnit(unit syncdoc.Unit) bool {
	return bytes.Contains(unit.Value, []byte("<!-- presentation:section id=\"")) ||
		bytes.Contains(unit.Value, []byte("<!-- /presentation:section id=\""))
}

func renderReviewBody(input ProjectInput, output ProjectOutput) ([]byte, error) {
	body, err := reviewv2.RenderReviewV3(output.Review)
	if err != nil {
		return nil, err
	}
	var additions strings.Builder
	if output.RecentProgress != "" {
		writeGeneratedSection(&additions, GeneratedSectionRecentProgress, "近期进展", output.RecentProgress)
	}
	if output.Usage != "" {
		writeGeneratedSection(&additions, GeneratedSectionModelUsage, "模型与 Token 使用", output.Usage)
	}
	if len(output.UnknownBlocks) != 0 {
		additions.WriteString("\n## 自定义内容\n" + generatedSectionOpen(GeneratedSectionCustomContent) + "\n")
		for _, key := range sortedUnknownKeys(output.UnknownBlocks) {
			additions.Write(output.UnknownBlocks[key])
			additions.WriteByte('\n')
		}
		additions.WriteString(generatedSectionClose(GeneratedSectionCustomContent) + "\n")
	}
	if additions.Len() == 0 {
		return body, nil
	}
	body = append([]byte(nil), body...)
	body = append(body, []byte(additions.String())...)
	if _, err := reviewv2.ParseReview(body); err != nil {
		return nil, fmt.Errorf("render review: custom concise sections are invalid: %w", err)
	}
	return body, nil
}

func writeGeneratedSection(output *strings.Builder, identity, title, body string) {
	// Keep ownership markers inside the Markdown section they describe. A
	// marker before the H2 belongs to the preceding section in the semantic
	// document model and can be stranded when human sections are reordered.
	output.WriteString("\n## " + title + "\n" + generatedSectionOpen(identity) + "\n" + body + "\n" +
		generatedSectionClose(identity) + "\n")
}

func generatedSectionOpen(identity string) string {
	return "<!-- presentation:section id=\"" + identity + "\" -->"
}

func generatedSectionClose(identity string) string {
	return "<!-- /presentation:section id=\"" + identity + "\" -->"
}

// CaptureCustomContent recovers the byte-preserved payload from the designated
// human custom-content section. The ownership markers themselves remain
// renderer-owned and are regenerated on every projection.
func CaptureCustomContent(source []byte) (map[string][]byte, error) {
	prefixes := [][]byte{
		[]byte("## 自定义内容\n" + generatedSectionOpen(GeneratedSectionCustomContent) + "\n"),
		[]byte(generatedSectionOpen(GeneratedSectionCustomContent) + "\n## 自定义内容\n"),
	}
	suffix := []byte("\n" + generatedSectionClose(GeneratedSectionCustomContent))
	start, prefixLength := -1, 0
	for _, prefix := range prefixes {
		for offset := 0; offset <= len(source); {
			match := bytes.Index(source[offset:], prefix)
			if match < 0 {
				break
			}
			if start >= 0 {
				return nil, errors.New("duplicate custom-content presentation section")
			}
			start, prefixLength = offset+match, len(prefix)
			offset = start + len(prefix)
		}
	}
	if start < 0 {
		return nil, nil
	}
	payloadStart := start + prefixLength
	endRelative := bytes.Index(source[payloadStart:], suffix)
	if endRelative < 0 {
		return nil, errors.New("custom-content presentation section is not closed")
	}
	payloadEnd := payloadStart + endRelative
	if bytes.Index(source[payloadEnd+len(suffix):], []byte(generatedSectionClose(GeneratedSectionCustomContent))) >= 0 {
		return nil, errors.New("duplicate custom-content presentation close marker")
	}
	if payloadEnd == payloadStart {
		return nil, nil
	}
	return map[string][]byte{"preserved": bytes.Clone(source[payloadStart:payloadEnd])}, nil
}

func renderMachineLedger(input ProjectInput, output ProjectOutput, reviewBody, historyBody []byte) ([]byte, error) {
	accountingInputs := make([]*accounting.SessionAccounting, 0, len(input.SessionReports))
	for index := range input.SessionReports {
		accountingInputs = append(accountingInputs, input.SessionReports[index].Accounting)
	}
	if err := accounting.ValidateProjectSummary(input.Accounting, accountingInputs); err != nil {
		return nil, fmt.Errorf("render machine accounting: %w", err)
	}
	value := reviewv2.MachineLedgerV3{
		SchemaVersion: reviewv2.SchemaVersion, MinimumWriterVersion: reviewv2.MinimumWriterVersion,
		ProjectID: input.ProjectView.ProjectID, GenerationID: input.GenerationID,
		ProjectViewDigest: publicDigest(input.ProjectView.Digest), AcceptedRevision: input.Revision,
		ReviewSHA256: sha256Hex(reviewBody), HistorySHA256: sha256Hex(historyBody),
		LastSuccessfulSync: input.LastSuccessfulSync,
		Accounting:         input.Accounting,
		Sessions:           append([]ledger.SessionReport{}, input.SessionReports...), HumanPatches: presentationPatchesToWire(output.ActivePatches),
		OrphanPatches:       presentationPatchesToWire(output.OrphanPatches),
		GeneratedBaselines:  presentationBaselinesToWire(input.GenerationID, output.Baselines),
		LegacyCompatibility: input.Legacy.Compatibility,
	}
	return reviewv2.RenderMachineLedgerV3(value)
}

func presentationPatchesToWire(values []Patch) []reviewv2.HumanPatchWire {
	result := make([]reviewv2.HumanPatchWire, 0, len(values))
	for _, value := range values {
		result = append(result, reviewv2.HumanPatchWire{
			EntityID: value.EntityID, Field: value.Field, Operation: string(value.Operation),
			Value: value.Value, Values: append([]string(nil), value.Values...),
			BaseGeneratedHash: value.BaseGeneratedHash,
		})
	}
	return result
}

func presentationBaselinesToWire(generationID string, values []Baseline) []reviewv2.GeneratedBaselineWire {
	result := make([]reviewv2.GeneratedBaselineWire, 0, len(values))
	for _, value := range values {
		result = append(result, reviewv2.GeneratedBaselineWire{
			GenerationID: generationID, EntityID: value.EntityID, Field: value.Field,
			Kind: string(value.Kind), Value: value.Value, Values: append([]string(nil), value.Values...),
			GeneratedHash: value.GeneratedHash,
		})
	}
	return result
}

func sha256Hex(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func publicDigest(value string) string {
	return strings.TrimPrefix(value, "sha256:")
}
