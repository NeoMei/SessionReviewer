package reviewv4

type DocumentProjection struct {
	SchemaVersion    int          `json:"schema_version" required:"true"`
	Format           string       `json:"format" required:"true"`
	PresentationBase Presentation `json:"presentation_base" required:"true"`
}
