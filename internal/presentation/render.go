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

func Render(input ProjectInput, output ProjectOutput) (RenderPlan, error) {
	reviewBody, err := renderReviewBody(input, output)
	if err != nil {
		return RenderPlan{}, err
	}
	historyBody, err := reviewv2.RenderHistoryV3(input.ProjectView.ProjectID, input.Revision, input.GenerationID, output.Events)
	if err != nil {
		return RenderPlan{}, err
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
	return plan, nil
}

func renderReviewBody(input ProjectInput, output ProjectOutput) ([]byte, error) {
	body, err := reviewv2.RenderReviewV3(output.Review)
	if err != nil {
		return nil, err
	}
	var additions strings.Builder
	if output.RecentProgress != "" {
		additions.WriteString("\n## 近期进展\n" + output.RecentProgress + "\n")
	}
	if output.Usage != "" {
		additions.WriteString("\n## 模型与 Token 使用\n" + output.Usage + "\n")
	}
	if len(output.UnknownBlocks) != 0 {
		additions.WriteString("\n## 自定义内容\n")
		for _, key := range sortedUnknownKeys(output.UnknownBlocks) {
			additions.Write(output.UnknownBlocks[key])
			additions.WriteByte('\n')
		}
	}
	if additions.Len() == 0 {
		return body, nil
	}
	anchor := []byte("\n## 项目历史\n")
	position := bytes.LastIndex(body, anchor)
	if position < 0 {
		return nil, errors.New("render review: generated project-history anchor is missing")
	}
	body = append([]byte(nil), body...)
	insertion := []byte(additions.String())
	body = append(body[:position], append(insertion, body[position:]...)...)
	if _, err := reviewv2.ParseReview(body); err != nil {
		return nil, fmt.Errorf("render review: custom concise sections are invalid: %w", err)
	}
	return body, nil
}

func renderMachineLedger(input ProjectInput, output ProjectOutput, reviewBody, historyBody []byte) ([]byte, error) {
	value := reviewv2.MachineLedgerV3{
		SchemaVersion: reviewv2.SchemaVersion, MinimumWriterVersion: reviewv2.MinimumWriterVersion,
		ProjectID: input.ProjectView.ProjectID, GenerationID: input.GenerationID,
		ProjectViewDigest: publicDigest(input.ProjectView.Digest), AcceptedRevision: input.Revision,
		ReviewSHA256: sha256Hex(reviewBody), HistorySHA256: sha256Hex(historyBody),
		Accounting: accounting.ProjectSummary{Models: []accounting.ProjectModelSummary{}},
		Sessions:   []ledger.SessionReport{}, HumanPatches: presentationPatchesToWire(output.ActivePatches),
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
