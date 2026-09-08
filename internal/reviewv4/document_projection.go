package reviewv4

type DocumentProjection struct {
	SchemaVersion    int          `json:"schema_version" required:"true"`
	Format           string       `json:"format" required:"true"`
	PresentationBase Presentation `json:"presentation_base" required:"true"`
}

func presentationCapability(p Presentation) string {
	if presentationUsesHistoricalBindings(p) {
		return "0.4.3"
	}
	return "0.4.0"
}

func documentProjectionCapability(p Presentation) string {
	if presentationUsesHistoricalBindings(p) {
		return "0.4.3"
	}
	return "0.4.1"
}
