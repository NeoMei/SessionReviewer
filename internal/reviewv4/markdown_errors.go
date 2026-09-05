package reviewv4

import "errors"

const (
	MarkdownFormatInvalid                = "markdown_format_invalid"
	MarkdownFieldDuplicate               = "markdown_field_duplicate"
	MarkdownFieldMissing                 = "markdown_field_missing"
	MarkdownStructureEditRequiresCommand = "markdown_structure_edit_requires_command"
	MarkdownGeneratedRegionModified      = "markdown_generated_region_modified"
	MarkdownBaselineMissing              = "markdown_baseline_missing"
	MarkdownFieldConflict                = "markdown_field_conflict"
	MarkdownMigrationConflict            = "markdown_migration_conflict"
)

var markdownCodes = map[string]struct{}{
	MarkdownFormatInvalid: {}, MarkdownFieldDuplicate: {}, MarkdownFieldMissing: {},
	MarkdownStructureEditRequiresCommand: {}, MarkdownGeneratedRegionModified: {},
	MarkdownBaselineMissing: {}, MarkdownFieldConflict: {}, MarkdownMigrationConflict: {},
}

type MarkdownError struct {
	Code     string
	Relative string
	Entity   string
	Field    string
	Cause    error
}

func (e *MarkdownError) Error() string { return e.Code }
func (e *MarkdownError) Unwrap() error { return e.Cause }

func MarkdownCodeOf(err error) string {
	var markdown *MarkdownError
	if errors.As(err, &markdown) && markdown != nil {
		if _, ok := markdownCodes[markdown.Code]; !ok {
			return ""
		}
		return markdown.Code
	}
	return ""
}
