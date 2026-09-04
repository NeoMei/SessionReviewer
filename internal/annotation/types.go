package annotation

type CandidateStatus string

const (
	CandidatePending     CandidateStatus = "pending"
	CandidateConfirmed   CandidateStatus = "confirmed"
	CandidateIgnored     CandidateStatus = "ignored"
	CandidateNotDecision CandidateStatus = "not_decision"
	CandidateStale       CandidateStatus = "stale"
)

type StoreRecord struct {
	SchemaVersion        int          `json:"schema_version" required:"true"`
	MinimumReaderVersion string       `json:"minimum_reader_version" required:"true"`
	ProjectID            string       `json:"project_id" required:"true"`
	Annotations          []Annotation `json:"annotations" required:"true"`
	ExtractionRuns       []Run        `json:"extraction_runs" required:"true"`
}

type Annotation struct {
	ID                  string          `json:"id" required:"true"`
	ProjectID           string          `json:"project_id" required:"true"`
	EntityID            string          `json:"entity_id" required:"true"`
	Field               string          `json:"field" required:"true"`
	Status              CandidateStatus `json:"status" required:"true"`
	Text                string          `json:"text" required:"true"`
	GenerationID        string          `json:"generation_id" required:"true"`
	SchemaVersion       int             `json:"schema_version" required:"true"`
	AnalysisProfile     string          `json:"analysis_profile" required:"true"`
	AgentRunID          string          `json:"agent_run_id" required:"true"`
	Dependencies        []Dependency    `json:"dependencies" required:"true"`
	Revision            int             `json:"revision" required:"true"`
	CreatedAt           string          `json:"created_at" required:"true"`
	ConfirmedDecisionID *string         `json:"confirmed_decision_id" required:"true" nullable:"true"`
}

type Dependency struct {
	Kind       string `json:"kind" required:"true"`
	RevisionID string `json:"revision_id" required:"true"`
	Digest     string `json:"digest" required:"true"`
}

type Run struct {
	RunID               string   `json:"run_id" required:"true"`
	ProjectID           string   `json:"project_id" required:"true"`
	Status              string   `json:"status" required:"true"`
	ExtractorVersion    string   `json:"extractor_version" required:"true"`
	PromptSchemaVersion string   `json:"prompt_schema_version" required:"true"`
	DependencyDigests   []string `json:"dependency_digests" required:"true"`
	CreatedAt           string   `json:"created_at" required:"true"`
	UpdatedAt           string   `json:"updated_at" required:"true"`
}
