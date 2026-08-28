package reviewprompt_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/reviewprompt"
)

func TestBuildRejectsNoncanonicalEvidenceEventIDWithZeroBundle(t *testing.T) {
	input := fixtureInput()
	input.Packet.Events[0].ID = "ev-not-twelve-lowercase-hex"
	bundle, err := reviewprompt.Build(input)
	if err != reviewprompt.ErrInvalidInput {
		t.Fatalf("err=%v want exact ErrInvalidInput", err)
	}
	if !reflect.DeepEqual(bundle, reviewprompt.Bundle{}) {
		t.Fatalf("failure returned nonzero bundle: %+v", bundle)
	}
}

func TestBuildAcceptsBalancedLowercaseSHA256WithoutProseScanning(t *testing.T) {
	input := fixtureInput()
	balanced := strings.Repeat("0123456789abcdef", 4)
	input.Packet.Events[0].SourceHash = balanced
	input.Packet.NextCursor.SourceHash = balanced
	if _, err := reviewprompt.Build(input); err != nil {
		t.Fatalf("valid protocol hash was treated as prose: %v", err)
	}
}
