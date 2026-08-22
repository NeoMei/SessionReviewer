package redact

import (
	"strings"
	"testing"
)

func TestDefaultRedactsCredentialCanaries(t *testing.T) {
	canaries := []string{
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.canary.signature",
		"OPENAI_API_KEY=sk-canary-123456789012345678901234567890",
		"postgres://admin:canary-password@db.example/test",
		"-----BEGIN PRIVATE KEY-----\nCANARYPRIVATEKEY\n-----END PRIVATE KEY-----",
		"cookie: session=canary-cookie-value",
	}
	for i, input := range canaries {
		result := Default().Text(input)
		if strings.Contains(result.Text, "canary") {
			t.Fatalf("credential canary %d leaked", i)
		}
		if !strings.Contains(result.Text, "[REDACTED:") {
			t.Fatalf("credential canary %d was not redacted", i)
		}
	}
}

func TestDefaultPreservesSessionAndItemIDs(t *testing.T) {
	input := "session 01a02971-61d6-7251-bdcf-f999230f961d item msg_01a02974-3c83-7390-acd8-cb0fd17b6eef"
	if got := Default().Text(input).Text; got != input {
		t.Fatal("stable session or item ID was changed")
	}
}

func TestDefaultRedactsHighEntropyCanary(t *testing.T) {
	input := "q7Vx2Pm9Lk4Nz8Rc1Ya6Wt3Hu5Jd0Sf7Bg2Ke9Ui"
	result := Default().Text(input)
	if strings.Contains(result.Text, input) || len(result.Findings) != 1 || result.Findings[0].Rule != "high_entropy_token" {
		t.Fatal("high-entropy canary was not safely reported")
	}
}

func TestDefaultPreservesStableItemID(t *testing.T) {
	input := "msg_01a029743c837390acd8cb0fd17b6eef00000000"
	if got := Default().Text(input).Text; got != input {
		t.Fatal("stable item ID was changed")
	}
}
