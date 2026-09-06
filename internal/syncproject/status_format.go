package syncproject

import (
	"bytes"
	"errors"
	"path/filepath"
	"regexp"

	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/publicationstate"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

// This is a conservative format discriminator, not an acceptance parser. It
// recognizes even truncated v4 schema/format declarations so they cannot be
// sent to a legacy repair-capable engine. Full v4 validation remains shared.
var modernStatusSchema = regexp.MustCompile(`(?m)(?:"schema_version"|^[ \t]*["']?schema_version["']?)[ \t\r\n]*:[ \t\r\n]*["']?(?:[4-9]|[1-9][0-9]+)(?:[^0-9]|$)`)
var modernStatusDocumentFormat = regexp.MustCompile(`(?m)^[ \t]*(?:document_format|["']document_format["'])[ \t]*:[ \t]*["']?review-markdown-v1(?:["' \t\r\n]|$)`)
var modernStatusProjection = regexp.MustCompile(`"document_projection"[ \t\r\n]*:`)

func legacyStatusFormat(pin *MappingPin) (ProjectionFormat, bool, error) {
	var projectReview, projectLedger []byte
	v4 := false
	for _, side := range []struct {
		root    *pathguard.Directory
		prefix  string
		project bool
	}{
		{pin.project, "docs/session-review", true}, {pin.vault, pin.mapping.VaultReviewPath, false},
	} {
		for _, relative := range []string{markdownReviewRelative, markdownHistoryRelative, markdownLedgerRelative, markdownIndexRelative} {
			body, found, err := side.root.ReadRegularOptional(filepath.ToSlash(filepath.Join(side.prefix, relative)), 64<<20)
			if err != nil {
				return "", false, errors.New("status format input is unavailable or unsafe")
			}
			if !found {
				continue
			}
			identity := statusFormatIdentity(body)
			if relative == markdownIndexRelative || modernStatusSchema.Match(identity) || modernStatusDocumentFormat.Match(identity) ||
				(relative == markdownLedgerRelative && modernStatusProjection.Match(body)) {
				v4 = true
			}
			if side.project && relative == markdownReviewRelative {
				projectReview = body
			}
			if side.project && relative == markdownLedgerRelative {
				projectLedger = body
			}
		}
	}
	_, receiptFound, err := pin.data.ReadRegularOptional(filepath.ToSlash(filepath.Join("publication-journal", pin.mapping.ID, publicationstate.AcceptedReceiptLeaf)), 4<<20)
	if err != nil {
		return "", false, errors.New("status publication binding is unavailable or unsafe")
	}
	if v4 || receiptFound {
		return "", false, nil
	}
	version, err := reviewv2.DetectVersionExpected(pin.project.Path, pin.project.Info())
	if err != nil {
		return "", false, err
	}
	switch version {
	case reviewv2.VersionLegacy:
		return ProjectionLegacy, true, nil
	case reviewv2.VersionV3:
		// A parsed review or existing legacy Base distinguishes a legacy
		// diagnostic from v4 content whose metadata was partially downgraded.
		if legacyStatusReviewKnown(pin, projectReview) {
			return ProjectionV3, true, nil
		}
	case reviewv2.VersionV2:
		if review, err := reviewv2.ParseReview(projectReview); err == nil && review.Model.ProjectID == pin.mapping.ID {
			return ProjectionV2, true, nil
		}
		if ledger, err := reviewv2.ParseMachineLedger(projectLedger); err == nil && ledger.ProjectID == pin.mapping.ID {
			return ProjectionV2, true, nil
		}
	}
	return "", false, nil
}

func legacyStatusReviewKnown(pin *MappingPin, body []byte) bool {
	if review, err := reviewv2.ParseReview(body); err == nil && review.Model.ProjectID == pin.mapping.ID {
		return true
	}
	base, found, err := (syncengine.BaseStore{Root: pin.syncData.Root}).Load("project-overview")
	if err != nil || !found {
		return false
	}
	review, err := reviewv2.ParseReview(base.Content)
	return err == nil && review.Model.ProjectID == pin.mapping.ID
}

func statusFormatIdentity(body []byte) []byte {
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		return body
	}
	normalized := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(normalized, []byte("---\n")) {
		return nil
	}
	if end := bytes.Index(normalized[4:], []byte("\n---")); end >= 0 {
		return normalized[4 : 4+end]
	}
	// A truncated frontmatter declaration must still prevent a downgrade.
	return normalized[4:]
}
