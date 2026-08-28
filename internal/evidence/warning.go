package evidence

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/neomei/SessionReviewer/internal/redact"
)

type WarningKind string

const (
	WarningKindRedacted            WarningKind = "redacted"
	WarningKindMalformedJSONLLines WarningKind = "malformed_jsonl_lines"
)

type Warning struct {
	Kind  WarningKind
	Rule  string
	Count int
}

// ParseWarning accepts the complete evidence-v2 warning vocabulary and
// rejects aliases and non-canonical counts.
func ParseWarning(value string) (Warning, error) {
	parts := strings.Split(value, ":")
	var warning Warning
	switch {
	case len(parts) == 3 && parts[0] == string(WarningKindRedacted):
		warning = Warning{Kind: WarningKindRedacted, Rule: parts[1]}
	case len(parts) == 2 && parts[0] == string(WarningKindMalformedJSONLLines):
		warning = Warning{Kind: WarningKindMalformedJSONLLines}
	default:
		return Warning{}, errors.New("unknown evidence warning")
	}
	countText := parts[len(parts)-1]
	if !canonicalPositiveDecimal(countText) {
		return Warning{}, errors.New("invalid evidence warning count")
	}
	count, err := strconv.Atoi(countText)
	if err != nil || count <= 0 {
		return Warning{}, errors.New("invalid evidence warning count")
	}
	warning.Count = count
	if warning.Kind == WarningKindRedacted {
		if !redact.IsKnownRuleName(warning.Rule) {
			return Warning{}, errors.New("invalid redaction warning rule")
		}
	} else if warning.Rule != "" {
		return Warning{}, errors.New("structural warning cannot name a redaction rule")
	}
	return warning, nil
}

// FormatWarning is the producer-side counterpart to ParseWarning.
func FormatWarning(warning Warning) (string, error) {
	var value string
	switch warning.Kind {
	case WarningKindRedacted:
		value = fmt.Sprintf("redacted:%s:%d", warning.Rule, warning.Count)
	case WarningKindMalformedJSONLLines:
		if warning.Rule != "" {
			return "", errors.New("structural warning cannot name a redaction rule")
		}
		value = fmt.Sprintf("malformed_jsonl_lines:%d", warning.Count)
	default:
		return "", errors.New("unknown evidence warning kind")
	}
	if _, err := ParseWarning(value); err != nil {
		return "", err
	}
	return value, nil
}

func canonicalPositiveDecimal(value string) bool {
	if value == "" || value[0] < '1' || value[0] > '9' {
		return false
	}
	for index := 1; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}
