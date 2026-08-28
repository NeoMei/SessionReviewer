package evidence_test

import (
	"testing"

	"github.com/neomei/SessionReviewer/internal/evidence"
)

func TestEventIDContractMatchesExtractorIdentityShape(t *testing.T) {
	for _, value := range []string{"ev-0123456789ab", "ev-abcdef012345"} {
		if !evidence.ValidEventID(value) {
			t.Errorf("canonical event ID rejected: %q", value)
		}
	}
	for _, value := range []string{
		"", "e1", "ev-message", "ev-0123456789a", "ev-0123456789abc",
		"ev-0123456789aB", "ev_0123456789ab", "EV-0123456789ab",
	} {
		if evidence.ValidEventID(value) {
			t.Errorf("noncanonical event ID accepted: %q", value)
		}
	}
}
