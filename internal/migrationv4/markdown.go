package migrationv4

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

const (
	FormatMarkdownV2 = "review-markdown-v2"
	FormatMarkdownV3 = "review-markdown-v3"
	FormatJSONV4     = "review-json-v4"
	FormatMarkdownV1 = "review-markdown-v1"
)

var legacyReconstructionBlockers = []string{
	"verified_conversation_chain_required",
	"verified_user_request_classification_required",
}

// BuildBindingSuccessor creates a deterministic private binding only for an
// old-v4 manifest that did not authenticate an index. Both seed and target are
// built from the manifest's authenticated ProjectView and SessionViews.
func BuildBindingSuccessor(input BindingSuccessorInput) (BindingSuccessor, error) {
	if err := memory.ValidateGenerationManifest(input.SourceManifest); err != nil {
		return BindingSuccessor{}, fmt.Errorf("source manifest: %w", err)
	}
	digest, err := memory.Digest(input.SourceManifest)
	if err != nil || digest != input.SourceManifestDigest {
		return BindingSuccessor{}, errors.Join(errors.New("source manifest digest mismatch"), err)
	}
	if input.ProjectView.ProjectID != input.SourceManifest.ProjectID || input.ProjectView.Digest != input.SourceManifest.ProjectViewDigest {
		return BindingSuccessor{}, errors.New("source ProjectView does not match manifest")
	}
	generatedAt, err := time.Parse(time.RFC3339Nano, input.SourceManifest.CreatedAt)
	if err != nil {
		return BindingSuccessor{}, err
	}
	if input.SourceManifest.SessionIndexDigest != "" {
		return BindingSuccessor{}, errors.New("authenticated source index does not require a binding successor")
	}
	seed, err := sessionindex.Build(sessionindex.BuildInput{
		ProjectView: input.ProjectView, Manifest: input.SourceManifest,
		SessionViews: input.SessionViews, GeneratedAt: generatedAt,
	})
	if err != nil {
		return BindingSuccessor{}, fmt.Errorf("build authenticated migration seed index: %w", err)
	}
	if input.SourceIndex != nil {
		body, renderErr := sessionindex.Render(*input.SourceIndex)
		candidate, parseErr := sessionindex.Parse(body)
		if renderErr != nil || parseErr != nil || candidate.ProjectID != input.SourceManifest.ProjectID || candidate.GenerationID != input.SourceManifest.GenerationID || candidate.ProjectViewDigest != input.SourceManifest.ProjectViewDigest || candidate.Digest != seed.Digest {
			return BindingSuccessor{}, errors.Join(errors.New("public source index cannot be reconstructed from authenticated dependencies"), renderErr, parseErr)
		}
	}
	identity := struct {
		Domain               string `json:"domain"`
		SourceManifestDigest string `json:"source_manifest_digest"`
		TargetFormat         string `json:"target_format"`
		SeedIndexDigest      string `json:"seed_index_digest"`
	}{"session-reviewer/markdown-migration-binding/v1", input.SourceManifestDigest, FormatMarkdownV1, seed.Digest}
	body, err := strictjson.Encode(identity)
	if err != nil {
		return BindingSuccessor{}, err
	}
	targetGeneration := "migration-" + strings.TrimPrefix(digestBytes(body), "sha256:")[:32]
	successor := input.SourceManifest
	successor.GenerationID = targetGeneration
	targetIndex, err := sessionindex.Build(sessionindex.BuildInput{
		ProjectView: input.ProjectView, Manifest: successor,
		SessionViews: input.SessionViews, GeneratedAt: generatedAt,
	})
	if err != nil {
		return BindingSuccessor{}, fmt.Errorf("build authenticated migration target index: %w", err)
	}
	successor.SessionIndexDigest = targetIndex.Digest
	if err := memory.ValidateGenerationManifest(successor); err != nil {
		return BindingSuccessor{}, fmt.Errorf("target manifest: %w", err)
	}
	targetManifestDigest, err := memory.Digest(successor)
	if err != nil {
		return BindingSuccessor{}, err
	}
	return BindingSuccessor{Manifest: successor, Index: targetIndex, SourceManifestDigest: input.SourceManifestDigest, TargetManifestDigest: targetManifestDigest, SeedIndexDigest: seed.Digest}, nil
}

func digestBytes(body []byte) string { return digest(body) }

// BuildMarkdownPreview produces either a complete publishable Markdown target
// or a digest-bound informative preview with no target artifacts. A typed
// Presentation is never treated as authenticity evidence by itself.
func BuildMarkdownPreview(input MarkdownMigrationInput) (Result, error) {
	source := input.Source
	format, acceptedV4, err := classifyMarkdownMigrationSource(source)
	if err != nil {
		return Result{}, err
	}
	switch format {
	case FormatJSONV4:
		return buildOldV4MarkdownPreview(source, acceptedV4)
	case FormatMarkdownV2, FormatMarkdownV3:
		return buildBlockedLegacyMarkdownPreview(source, format, input.Reconstructed)
	default:
		return Result{}, errors.New("unsupported Markdown migration source")
	}
}

func classifyMarkdownMigrationSource(input Input) (string, reviewv4.Accepted, error) {
	if len(input.Review) == 0 || len(input.History) == 0 || len(input.Ledger) == 0 {
		return "", reviewv4.Accepted{}, errors.New("complete authenticated migration source is required")
	}
	sourceIndex := input.SourceSessionIndex
	if presentation, err := reviewv4.DecodePresentation(input.Review); err == nil && presentation.SchemaVersion == 4 {
		if len(sourceIndex) == 0 {
			return "", reviewv4.Accepted{}, errors.New("old v4 source session index is required")
		}
		accepted, err := reviewv4.LoadProjection(input.Review, input.History, input.Ledger, sourceIndex)
		if err != nil || accepted.Ledger.DocumentProjection != nil {
			return "", reviewv4.Accepted{}, errors.Join(errors.New("old v4 JSON source is not authenticated"), err)
		}
		return FormatJSONV4, accepted, nil
	}
	if _, err := reviewv2.LoadV3Bytes(input.Review, input.History, input.Ledger); err == nil {
		return FormatMarkdownV3, reviewv4.Accepted{}, nil
	}
	if review, reviewErr := reviewv2.ParseReview(input.Review); reviewErr == nil {
		history, historyErr := reviewv2.ParseHistory(input.History)
		ledger, ledgerErr := reviewv2.ParseMachineLedger(input.Ledger)
		if historyErr == nil && ledgerErr == nil && review.Model.ProjectID == history.ProjectID && review.Model.ProjectID == ledger.ProjectID {
			return FormatMarkdownV2, reviewv4.Accepted{}, nil
		}
	}
	return "", reviewv4.Accepted{}, errors.New("migration source format is malformed or unsupported")
}

func buildBlockedLegacyMarkdownPreview(input Input, format string, reconstructed *reviewv4.Presentation) (Result, error) {
	var projectID, generationID string
	var sourceVersion int
	switch format {
	case FormatMarkdownV3:
		accepted, err := reviewv2.LoadV3Bytes(input.Review, input.History, input.Ledger)
		if err != nil {
			return Result{}, err
		}
		projectID, generationID, sourceVersion = accepted.State.Machine.ProjectID, accepted.State.Machine.GenerationID, 3
	case FormatMarkdownV2:
		review, err := reviewv2.ParseReview(input.Review)
		if err != nil {
			return Result{}, err
		}
		projectID, sourceVersion = review.Model.ProjectID, 2
	}
	if reconstructed != nil && (reconstructed.ProjectID != projectID || (generationID != "" && reconstructed.GenerationID != generationID)) {
		return Result{}, errors.New("reconstructed presentation identity does not match authenticated legacy source")
	}
	preview := MigrationPreview{
		SchemaVersion: 1, SourceVersion: sourceVersion, TargetVersion: 4,
		ProjectID: projectID, GenerationID: generationID, RequiresSessionIndex: true,
		SourceFormat: format, TargetFormat: FormatMarkdownV1,
		SourceHashes: sourceHashes(input), TargetPreimageHashes: targetPreimageHashes(input.TargetPreimages),
		VaultPreimageHashes:          optionalPreimageHashes(input.TargetVaultPreimages),
		SessionViewDependencyDigests: sortedUnique(input.SessionViewDependencyDigests),
		BlockingReasons:              append([]string(nil), legacyReconstructionBlockers...),
	}
	preview.PreviewDigest = MigrationPreviewDigest(preview)
	if err := validateBlockedPreview(preview); err != nil {
		return Result{}, err
	}
	return Result{Preview: preview, TargetPreimages: clonePreimages(input.TargetPreimages), TargetVaultPreimages: clonePreimages(input.TargetVaultPreimages)}, nil
}

func buildOldV4MarkdownPreview(input Input, source reviewv4.Accepted) (Result, error) {
	index := input.SessionIndex
	if len(index) == 0 {
		index = input.SourceSessionIndex
	}
	presentation := source.Review
	sourceGenerationID := presentation.GenerationID
	if input.GenerationID != "" && input.GenerationID != presentation.GenerationID {
		presentation.GenerationID = input.GenerationID
		for index := range presentation.Timeline {
			presentation.Timeline[index].GenerationID = input.GenerationID
		}
		for index := range presentation.GeneratedBaselines {
			presentation.GeneratedBaselines[index].GenerationID = input.GenerationID
		}
	}
	ledger := source.Ledger
	ledger.MinimumReaderVersion, ledger.MinimumWriterVersion = "0.4.1", "0.4.1"
	ledger.GenerationID = presentation.GenerationID
	ledger.HumanPatches = append([]reviewv4.Patch(nil), presentation.HumanPatches...)
	ledger.OrphanPatches = append([]reviewv4.Patch(nil), presentation.OrphanPatches...)
	ledger.GeneratedBaselines = append([]reviewv4.Baseline(nil), presentation.GeneratedBaselines...)
	parsedIndex, err := sessionindex.Parse(index)
	if err != nil {
		return Result{}, err
	}
	ledger.SyncHashes.SessionIndexDigest = parsedIndex.Digest
	ledger.DocumentProjection = &reviewv4.DocumentProjection{SchemaVersion: 1, Format: FormatMarkdownV1, PresentationBase: presentation}
	seed, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		return Result{}, err
	}
	ledger, err = reviewv4.DecodeLedger(seed)
	if err != nil {
		return Result{}, err
	}
	pair, err := reviewv4.RenderMarkdown(presentation, ledger, nil)
	if err != nil {
		return Result{}, err
	}
	pair.History = appendHistoricalPreservation(pair.History, input.History)
	ledger.ReviewSHA256, ledger.HistorySHA256 = bareDigest(pair.Review), bareDigest(pair.History)
	ledger.SyncHashes.ReviewSHA256, ledger.SyncHashes.HistorySHA256 = ledger.ReviewSHA256, ledger.HistorySHA256
	ledgerBody, err := reviewv4.RenderLedger(ledger)
	if err != nil {
		return Result{}, err
	}
	accepted, err := reviewv4.LoadProjection(pair.Review, pair.History, ledgerBody, index)
	if err != nil {
		return Result{}, fmt.Errorf("validate Markdown migration target: %w", err)
	}
	result := Result{Review: pair.Review, History: pair.History, Ledger: ledgerBody, SessionIndex: bytes.Clone(index), Accepted: accepted, TargetPreimages: clonePreimages(input.TargetPreimages), TargetVaultPreimages: clonePreimages(input.TargetVaultPreimages)}
	result.Preview = MigrationPreview{
		SchemaVersion: 1, SourceVersion: 4, TargetVersion: 4,
		ProjectID: accepted.Review.ProjectID, GenerationID: accepted.Review.GenerationID, SourceGenerationID: sourceGenerationID,
		SourceManifestDigest: input.SourceManifestDigest, TargetManifestDigest: input.TargetManifestDigest, SourceJournalDigest: input.SourceJournalDigest,
		RequiresSessionIndex: true, SourceFormat: FormatJSONV4, TargetFormat: FormatMarkdownV1,
		SourceHashes:                 sourceHashes(input),
		TargetHashes:                 ArtifactHashes{Review: digest(result.Review), History: digest(result.History), Ledger: digest(result.Ledger), SessionIndex: digest(result.SessionIndex)},
		TargetPreimageHashes:         targetPreimageHashes(input.TargetPreimages),
		VaultPreimageHashes:          optionalPreimageHashes(input.TargetVaultPreimages),
		SessionViewDependencyDigests: sortedUnique(input.SessionViewDependencyDigests),
		PreservedCustomHashes:        map[string]string{HistoryRelativePath: digest(input.History)},
	}
	result.Preview.PreviewDigest = MigrationPreviewDigest(result.Preview)
	if err := validatePreview(result.Preview); err != nil {
		return Result{}, err
	}
	return result, nil
}

func appendHistoricalPreservation(rendered, source []byte) []byte {
	result := bytes.Clone(rendered)
	if len(result) != 0 && result[len(result)-1] != '\n' {
		result = append(result, '\n')
	}
	fenceLength, run := 3, 0
	for _, value := range source {
		if value == '`' {
			run++
			if run >= fenceLength {
				fenceLength = run + 1
			}
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", fenceLength)
	result = append(result, []byte("\n## 历史格式保留\n\n升级前历史原文存档（非权威，不作为当前字段或结构依据）。\n\n"+fence+"text\n")...)
	result = append(result, source...)
	if len(source) == 0 || source[len(source)-1] != '\n' {
		result = append(result, '\n')
	}
	result = append(result, []byte(fence+"\n")...)
	return result
}

func sourceHashes(input Input) ArtifactHashes {
	index := input.SourceSessionIndex
	return ArtifactHashes{Review: sourceHash(input.Review), History: sourceHash(input.History), Ledger: sourceHash(input.Ledger), SessionIndex: sourceHash(index)}
}

func sourceHash(body []byte) string {
	if len(body) == 0 {
		return AbsentPreimageSHA256
	}
	return digest(body)
}

func targetPreimageHashes(preimages map[string]Preimage) ArtifactHashes {
	return ArtifactHashes{
		Review: preimageHash(preimages[ReviewRelativePath]), History: preimageHash(preimages[HistoryRelativePath]),
		Ledger: preimageHash(preimages[LedgerRelativePath]), SessionIndex: preimageHash(preimages[SessionIndexRelativePath]),
	}
}

func optionalPreimageHashes(preimages map[string]Preimage) *ArtifactHashes {
	if preimages == nil {
		return nil
	}
	hashes := targetPreimageHashes(preimages)
	return &hashes
}

func bareDigest(body []byte) string { return strings.TrimPrefix(digest(body), "sha256:") }
