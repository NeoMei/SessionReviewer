// Package modelpricewatch fetches and privately caches the public price catalog.
package modelpricewatch

import "github.com/neomei/SessionReviewer/internal/modelpricewatch/catalog"

const (
	AdapterSchemaVersion = catalog.AdapterSchemaVersion
	DefaultBodyLimit     = catalog.DefaultBodyLimit
	FreshCurrent         = catalog.FreshCurrent
	FreshStale           = catalog.FreshStale
	FreshExpired         = catalog.FreshExpired
)

type Catalog = catalog.Catalog
type HistoryCatalog = catalog.HistoryCatalog
type CatalogSet = catalog.CatalogSet
type Listing = catalog.Listing
type Evidence = catalog.Evidence
type ModelHistory = catalog.ModelHistory
type HistoryEntry = catalog.HistoryEntry
type Change = catalog.Change
type CorrectedFrom = catalog.CorrectedFrom
type FreshnessStatus = catalog.FreshnessStatus
type Freshness = catalog.Freshness
