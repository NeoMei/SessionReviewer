package sourcecatalog

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/pathguard"
)

// ReadAuthenticated reads exactly one content-free record without creating
// directories, acquiring writable locks, recovering journals, or chmod. A
// concurrent catalog advance is usable only if it still matches the published
// SessionView digest. Partial batch state therefore cannot authenticate here.
func ReadAuthenticated(ctx context.Context, dataRoot, provider, sessionID, digest string) (memory.SourceRecord, error) {
	if ctx == nil || !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return memory.SourceRecord{}, errors.New("invalid source catalog request")
	}
	root, err := pathguard.Open(filepath.Join(dataRoot, "source-catalog"))
	if err != nil {
		return memory.SourceRecord{}, err
	}
	defer root.Close()
	leaf := sourceLeaf(provider, sessionID)
	body, found, err := root.ReadRegularContext(ctx, leaf, maxCatalogRecord)
	if err != nil || !found {
		return memory.SourceRecord{}, errors.New("source record unavailable")
	}
	if err := requirePrivateFile(root.Root, leaf); err != nil {
		return memory.SourceRecord{}, err
	}
	var record memory.SourceRecord
	if err := decodeCanonical(body, &record); err != nil {
		return memory.SourceRecord{}, err
	}
	if err := memory.ValidateSourceRecord(record); err != nil {
		return memory.SourceRecord{}, err
	}
	actual, err := memory.DigestContext(ctx, record)
	if err != nil || actual != digest || record.Provider != provider || record.SessionID != sessionID {
		return memory.SourceRecord{}, errors.New("source record does not match published dependency")
	}
	return record, nil
}
