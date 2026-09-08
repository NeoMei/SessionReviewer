package reviewv4

import (
	"errors"

	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

type MarkdownDraftProjection struct {
	Ledger       MachineLedger
	Draft        MarkdownDraft
	SessionIndex sessionindex.Document
}

func ParseMarkdownDraftProjection(review, history, ledger, index []byte) (MarkdownDraftProjection, error) {
	l, err := DecodeLedger(ledger)
	if err != nil {
		return MarkdownDraftProjection{}, err
	}
	if l.DocumentProjection == nil {
		return MarkdownDraftProjection{}, errors.New("current Markdown projection is required")
	}
	d, err := ParseMarkdownDraft(MarkdownPair{Review: review, History: history}, l)
	if err != nil {
		return MarkdownDraftProjection{}, err
	}
	i, err := sessionindex.Parse(index)
	if err != nil {
		return MarkdownDraftProjection{}, err
	}
	if i.ProjectID != d.Presentation.ProjectID || i.GenerationID != d.Presentation.GenerationID ||
		i.ProjectViewDigest != d.Presentation.ProjectViewDigest || i.Digest != l.SyncHashes.SessionIndexDigest {
		return MarkdownDraftProjection{}, errors.New("Markdown index binding mismatch")
	}
	return MarkdownDraftProjection{Ledger: l, Draft: d, SessionIndex: i}, nil
}
