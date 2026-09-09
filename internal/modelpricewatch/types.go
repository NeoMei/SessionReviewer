// Package modelpricewatch decodes and privately caches the public
// ModelPriceWatch catalog. It never sends Session or project data.
package modelpricewatch

const (
	AdapterSchemaVersion       = 1
	DefaultBodyLimit     int64 = 128 << 20
)

type Catalog struct {
	Count    int
	Updated  string
	Listings map[string]Listing
}
type HistoryCatalog struct {
	Count   int
	Updated string
	Models  map[string]ModelHistory
}
type CatalogSet struct {
	Models  Catalog
	History HistoryCatalog
}

type Listing struct {
	ID                        string    `json:"id" required:"true"`
	Provider                  string    `json:"provider" required:"true"`
	Model                     string    `json:"model" required:"true"`
	Category                  string    `json:"category" required:"true"`
	InputPerMTok              *float64  `json:"input_per_mtok" required:"true" nullable:"true"`
	OutputPerMTok             *float64  `json:"output_per_mtok" required:"true" nullable:"true"`
	CachedInputPerMTok        *float64  `json:"cached_input_per_mtok" required:"true" nullable:"true"`
	Promo                     bool      `json:"promo" required:"true"`
	PromoUntil                *string   `json:"promo_until" required:"true" nullable:"true"`
	PriceNote                 *string   `json:"price_note" required:"true" nullable:"true"`
	ContextWindow             *int64    `json:"context_window" required:"true" nullable:"true"`
	Parameters                *string   `json:"parameters" required:"true" nullable:"true"`
	Evidence                  *Evidence `json:"evidence" required:"true" nullable:"true"`
	Modality                  []string  `json:"modality" required:"true"`
	Tags                      []string  `json:"tags" required:"true"`
	Released                  string    `json:"released" required:"true"`
	Status                    string    `json:"status" required:"true"`
	OpenSource                bool      `json:"open_source" required:"true"`
	BlendedCostPerMTok        float64   `json:"blended_cost_per_mtok" required:"true"`
	PricingURL                string    `json:"pricing_url" required:"true"`
	LastUpdated               string    `json:"last_updated" required:"true"`
	DetailURL                 string    `json:"detail_url" required:"true"`
	HasUnstructuredConditions bool      `json:"-"`
}

type Evidence struct {
	AsOf        string  `json:"as_of" required:"true"`
	Basis       string  `json:"basis" required:"true"`
	Event       *string `json:"event" required:"true" nullable:"true"`
	Source      string  `json:"source" required:"true"`
	Confidence  string  `json:"confidence" required:"true"`
	EvidenceURL *string `json:"evidence_url" required:"true" nullable:"true"`
	CapturedAt  string  `json:"captured_at" required:"true"`
	Snapshot    string  `json:"snapshot" required:"true"`
	SHA         string  `json:"sha" required:"true"`
	AnchorSKU   string  `json:"anchor_sku" required:"true"`
	AnchorScope string  `json:"anchor_scope" required:"true"`
}

type ModelHistory struct {
	Model    string         `json:"model" required:"true"`
	Provider string         `json:"provider" required:"true"`
	History  []HistoryEntry `json:"history" required:"true"`
}
type HistoryEntry struct {
	Date               string         `json:"date" required:"true"`
	InputPerMTok       *float64       `json:"input_per_mtok" required:"true" nullable:"true"`
	OutputPerMTok      *float64       `json:"output_per_mtok" required:"true" nullable:"true"`
	CachedInputPerMTok *float64       `json:"cached_input_per_mtok" required:"true" nullable:"true"`
	ContextWindow      *int64         `json:"context_window" required:"true" nullable:"true"`
	Event              string         `json:"event" required:"true"`
	Changes            []Change       `json:"changes" required:"true"`
	Source             *string        `json:"source,omitempty"`
	Confidence         *string        `json:"confidence,omitempty"`
	EvidenceURL        *string        `json:"evidence_url,omitempty"`
	CapturedAt         *string        `json:"captured_at,omitempty"`
	SHA                *string        `json:"sha,omitempty"`
	Snapshot           *string        `json:"snapshot,omitempty"`
	SupersedesFrom     *string        `json:"supersedes_from,omitempty"`
	SupersedesTo       *string        `json:"supersedes_to,omitempty"`
	CorrectedFrom      *CorrectedFrom `json:"corrected_from,omitempty"`
	Note               *string        `json:"note,omitempty"`
}
type Change struct {
	Field     string   `json:"field" required:"true"`
	Old       *float64 `json:"old" required:"true" nullable:"true"`
	New       float64  `json:"new" required:"true"`
	Direction string   `json:"direction" required:"true"`
	ChangePct *float64 `json:"change_pct" required:"true" nullable:"true"`
}
type CorrectedFrom struct {
	InputPerMTok  *float64 `json:"input_per_mtok,omitempty"`
	OutputPerMTok *float64 `json:"output_per_mtok,omitempty"`
	Provider      *string  `json:"provider,omitempty"`
}
