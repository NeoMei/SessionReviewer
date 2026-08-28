package evidence_test

import (
	"testing"

	"github.com/neomei/SessionReviewer/internal/evidence"
)

func TestWarningContractAcceptsOnlyCanonicalKnownVocabulary(t *testing.T) {
	tests := []struct {
		value string
		want  evidence.Warning
	}{
		{
			value: "redacted:openai_key:1",
			want:  evidence.Warning{Kind: evidence.WarningKindRedacted, Rule: "openai_key", Count: 1},
		},
		{
			value: "malformed_jsonl_lines:27",
			want:  evidence.Warning{Kind: evidence.WarningKindMalformedJSONLLines, Count: 27},
		},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got, err := evidence.ParseWarning(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("warning=%+v want=%+v", got, test.want)
			}
			encoded, err := evidence.FormatWarning(got)
			if err != nil || encoded != test.value {
				t.Fatalf("encoded=%q err=%v want=%q", encoded, err, test.value)
			}
		})
	}

	for _, value := range []string{
		"", "unknown:1", "redacted::1", "redacted:OPENAI_KEY:1", "redacted:openai-key:1",
		"redacted:openai_key:0", "redacted:openai_key:01", "redacted:openai_key:-1",
		"malformed_jsonl_lines:0", "malformed_jsonl_lines:01", "malformed_jsonl_lines:-1",
		"malformed_jsonl_lines:1:extra", "malformed_jsonl_lines_rule:1",
	} {
		t.Run("reject "+value, func(t *testing.T) {
			if got, err := evidence.ParseWarning(value); err == nil {
				t.Fatalf("accepted %q as %+v", value, got)
			}
		})
	}
}

func TestWarningContractRejectsInvalidTypedValues(t *testing.T) {
	for _, warning := range []evidence.Warning{
		{},
		{Kind: evidence.WarningKindRedacted, Count: 1},
		{Kind: evidence.WarningKindRedacted, Rule: "openai_key", Count: 0},
		{Kind: evidence.WarningKindMalformedJSONLLines, Rule: "unexpected", Count: 1},
		{Kind: evidence.WarningKindMalformedJSONLLines, Count: 0},
	} {
		if value, err := evidence.FormatWarning(warning); err == nil {
			t.Fatalf("formatted invalid warning %+v as %q", warning, value)
		}
	}
}
