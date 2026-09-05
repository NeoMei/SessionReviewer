package pricing

type PriceStatus string

const (
	PricePending          PriceStatus = "pending"
	PriceCurrent          PriceStatus = "current"
	PricePromotion        PriceStatus = "promotion"
	PriceStaleEstimate    PriceStatus = "stale_estimate"
	PriceManualSupplement PriceStatus = "manual_supplement"
	PriceAmbiguous        PriceStatus = "ambiguous"
	PriceLegacyUnverified PriceStatus = "legacy_unverified"
	PriceSuperseded       PriceStatus = "superseded"
)

type Rates struct {
	Input           *float64 `json:"input" required:"true" nullable:"true"`
	CachedInput     *float64 `json:"cached_input" required:"true" nullable:"true"`
	CacheWriteInput *float64 `json:"cache_write_input" required:"true" nullable:"true"`
	Output          *float64 `json:"output" required:"true" nullable:"true"`
	ReasoningOutput *float64 `json:"reasoning_output" required:"true" nullable:"true"`
}

type Quantities struct {
	Input           uint64 `json:"input" required:"true"`
	CachedInput     uint64 `json:"cached_input" required:"true"`
	CacheWriteInput uint64 `json:"cache_write_input" required:"true"`
	Output          uint64 `json:"output" required:"true"`
	ReasoningOutput uint64 `json:"reasoning_output" required:"true"`
}

type LineCosts struct {
	Input           *float64 `json:"input" required:"true" nullable:"true"`
	CachedInput     *float64 `json:"cached_input" required:"true" nullable:"true"`
	CacheWriteInput *float64 `json:"cache_write_input" required:"true" nullable:"true"`
	Output          *float64 `json:"output" required:"true" nullable:"true"`
	ReasoningOutput *float64 `json:"reasoning_output" required:"true" nullable:"true"`
}

type Snapshot struct {
	SchemaVersion            int         `json:"schema_version" required:"true"`
	MinimumReaderVersion     string      `json:"minimum_reader_version" required:"true"`
	SnapshotID               string      `json:"snapshot_id" required:"true"`
	ProjectID                string      `json:"project_id" required:"true"`
	Provider                 string      `json:"provider" required:"true"`
	SessionID                string      `json:"session_id" required:"true"`
	UsageRecordDigest        string      `json:"usage_record_digest" required:"true"`
	BillingHost              string      `json:"billing_host" required:"true"`
	BilledModelID            string      `json:"billed_model_id" required:"true"`
	BillingMode              string      `json:"billing_mode" required:"true"`
	BillingRuleVersion       string      `json:"billing_rule_version" required:"true"`
	Region                   *string     `json:"region" required:"true" nullable:"true"`
	PricedAt                 string      `json:"priced_at" required:"true"`
	CreatedAt                string      `json:"created_at" required:"true"`
	Status                   PriceStatus `json:"status" required:"true"`
	ModelPriceWatchListingID *string     `json:"modelpricewatch_listing_id" required:"true" nullable:"true"`
	SourceKind               string      `json:"source_kind" required:"true"`
	SourceURL                *string     `json:"source_url" required:"true" nullable:"true"`
	DetailURL                *string     `json:"detail_url" required:"true" nullable:"true"`
	SourceLastUpdated        *string     `json:"source_last_updated" required:"true" nullable:"true"`
	RetrievedAt              *string     `json:"retrieved_at" required:"true" nullable:"true"`
	Promo                    bool        `json:"promo" required:"true"`
	PromoUntil               *string     `json:"promo_until" required:"true" nullable:"true"`
	Rates                    Rates       `json:"rates" required:"true"`
	BillableQuantities       Quantities  `json:"billable_quantities" required:"true"`
	LineCostsUSD             LineCosts   `json:"line_costs_usd" required:"true"`
	MissingBillingDimensions []string    `json:"missing_billing_dimensions" required:"true"`
	KnownSubtotalUSD         float64     `json:"known_subtotal_usd" required:"true"`
	TotalCostUSD             *float64    `json:"total_cost_usd" required:"true" nullable:"true"`
	PricingComplete          bool        `json:"pricing_complete" required:"true"`
	SupersedesSnapshotID     *string     `json:"supersedes_snapshot_id" required:"true" nullable:"true"`
	AuditReason              string      `json:"audit_reason" required:"true"`
}

type Supplement struct {
	SchemaVersion        int     `json:"schema_version" required:"true"`
	MinimumReaderVersion string  `json:"minimum_reader_version" required:"true"`
	ProjectID            string  `json:"project_id" required:"true"`
	Provider             string  `json:"provider" required:"true"`
	SessionID            string  `json:"session_id" required:"true"`
	UsageRecordDigest    string  `json:"usage_record_digest" required:"true"`
	BillingHost          string  `json:"billing_host" required:"true"`
	BilledModelID        string  `json:"billed_model_id" required:"true"`
	BillingMode          string  `json:"billing_mode" required:"true"`
	BillingRuleVersion   string  `json:"billing_rule_version" required:"true"`
	Region               *string `json:"region" required:"true" nullable:"true"`
	EffectiveFrom        string  `json:"effective_from" required:"true"`
	EffectiveUntil       *string `json:"effective_until" required:"true" nullable:"true"`
	Rates                Rates   `json:"rates" required:"true"`
	SourceURL            string  `json:"source_url" required:"true"`
	DetailURL            *string `json:"detail_url" required:"true" nullable:"true"`
	AuditReason          string  `json:"audit_reason" required:"true"`
	SupersedesSnapshotID *string `json:"supersedes_snapshot_id" required:"true" nullable:"true"`
}
