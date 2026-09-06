package syncproject

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

type ProjectionFormat string

const (
	ProjectionLegacy   ProjectionFormat = "legacy"
	ProjectionV2       ProjectionFormat = "review-markdown-v2"
	ProjectionV3       ProjectionFormat = "review-markdown-v3"
	ProjectionJSONV4   ProjectionFormat = "review-json-v4"
	ProjectionMarkdown ProjectionFormat = "review-markdown-v1"
)

// DetectFormat validates enough of the complete four-file combination to keep
// malformed or partial state from being guessed as authenticated Markdown.
func DetectFormat(ctx context.Context, options Options) (_ ProjectionFormat, retErr error) {
	return detectFormat(ctx, options, false)
}

// DetectStatusFormat preserves the legacy Engine's diagnostic reads even when
// its ledger or human documents are malformed. It never classifies a partial
// v4 projection as legacy merely because full format validation failed.
func DetectStatusFormat(ctx context.Context, options Options) (_ ProjectionFormat, retErr error) {
	return detectFormat(ctx, options, true)
}

func detectFormat(ctx context.Context, options Options, diagnosticStatus bool) (_ ProjectionFormat, retErr error) {
	if ctx == nil {
		return "", errors.New("sync context is required")
	}
	pin, err := PinMapping(options)
	if err != nil {
		return "", err
	}
	defer func() { retErr = errors.Join(retErr, pin.Close()) }()
	if err := pin.verify(options); err != nil {
		return "", err
	}
	if diagnosticStatus {
		format, known, err := legacyStatusFormat(pin)
		if err != nil {
			return "", err
		}
		if known {
			return format, pin.verify(options)
		}
	}
	read := func(relative string) ([]byte, error) {
		body, found, err := pin.project.ReadRegularOptional(relative, 64<<20)
		if err != nil || !found {
			return nil, errors.Join(fmt.Errorf("required projection file %q is missing", relative), err)
		}
		return body, nil
	}
	ledgerBody, ledgerFound, ledgerReadErr := pin.project.ReadRegularOptional(reviewv2.MachineLedgerRelativePath, 64<<20)
	if ledgerReadErr != nil {
		return "", fmt.Errorf("read machine ledger for format detection: %w", ledgerReadErr)
	}
	if ledgerFound {
		ledger, decodeErr := reviewv4.DecodeLedger(ledgerBody)
		if decodeErr == nil {
			reviewBody, reviewErr := read(reviewv2.ReviewRelativePath)
			historyBody, historyErr := read(reviewv2.HistoryRelativePath)
			indexBody, indexErr := read(filepath.ToSlash(filepath.Join("docs/session-review", ".session-reviewer/session-index.json")))
			if err := errors.Join(reviewErr, historyErr, indexErr); err != nil {
				return "", err
			}
			if ledger.DocumentProjection != nil {
				draft, err := reviewv4.ParseMarkdownDraft(reviewv4.MarkdownPair{Review: reviewBody, History: historyBody}, ledger)
				if err != nil {
					return "", fmt.Errorf("validate Markdown projection format: %w", err)
				}
				index, err := sessionindex.Parse(indexBody)
				if err != nil || index.ProjectID != draft.Presentation.ProjectID || index.GenerationID != draft.Presentation.GenerationID || index.ProjectViewDigest != draft.Presentation.ProjectViewDigest || index.Digest != ledger.SyncHashes.SessionIndexDigest {
					return "", errors.Join(errors.New("Markdown index binding mismatch"), err)
				}
				return ProjectionMarkdown, nil
			}
			accepted, err := reviewv4.LoadProjection(reviewBody, historyBody, ledgerBody, indexBody)
			if err != nil {
				return "", fmt.Errorf("validate v4 JSON projection format: %w", err)
			}
			if accepted.Ledger.DocumentProjection != nil {
				return "", errors.New("unexpected Markdown projection classification")
			}
			return ProjectionJSONV4, nil
		}
		reviewBody, reviewErr := read(reviewv2.ReviewRelativePath)
		historyBody, historyErr := read(reviewv2.HistoryRelativePath)
		if !diagnosticStatus && reviewErr == nil && historyErr == nil {
			if _, err := reviewv2.LoadV3Bytes(reviewBody, historyBody, ledgerBody); err == nil {
				return ProjectionV3, nil
			}
		}
		if reviewErr == nil {
			if _, markdownErr := reviewv4.ParseMarkdownDocument(reviewv2.ReviewRelativePath, reviewBody); markdownErr == nil {
				return "", errors.Join(errors.New("present v4 machine ledger is malformed or unsupported"), decodeErr)
			}
			if _, jsonErr := reviewv4.DecodePresentation(reviewBody); jsonErr == nil {
				return "", errors.Join(errors.New("present v4 machine ledger is malformed or unsupported"), decodeErr)
			}
		}
		if version, legacyErr := reviewv2.DetectVersionExpected(pin.project.Path, pin.project.Info()); legacyErr != nil || version == reviewv2.VersionV3 {
			return "", errors.Join(errors.New("present machine ledger is malformed or unsupported"), decodeErr, legacyErr)
		}
	}
	version, err := reviewv2.DetectVersionExpected(pin.project.Path, pin.project.Info())
	if err != nil {
		return "", err
	}
	if diagnosticStatus {
		return "", errors.New("status projection is not a known legacy or validated v4 format")
	}
	switch version {
	case reviewv2.VersionLegacy:
		return ProjectionLegacy, nil
	case reviewv2.VersionV2:
		return ProjectionV2, nil
	case reviewv2.VersionV3:
		return ProjectionV3, nil
	default:
		return "", fmt.Errorf("unsupported or incomplete projection format %q", version)
	}
}
