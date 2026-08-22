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

func TestDefaultRedactsConventionalNamedSecretsWithQuotedValues(t *testing.T) {
	inputs := []string{
		`MY_API_KEY = "canary value with suffix" trailing`,
		`{"password": "canary password with spaces", "safe": true}`,
		`service_access_token='canary token with spaces' trailing`,
	}
	for i, input := range inputs {
		result := Default().Text(input)
		if strings.Contains(strings.ToLower(result.Text), "canary") {
			t.Fatalf("named-secret case %d leaked", i)
		}
		if !strings.Contains(result.Text, "[REDACTED:NAMED_SECRET]") {
			t.Fatalf("named-secret case %d was not redacted", i)
		}
	}
}

func TestDefaultRejectsStableIDPrefixImpostor(t *testing.T) {
	input := "msg_q7Vx2Pm9Lk4Nz8Rc1Ya6Wt3Hu5Jd0Sf7Bg2Ke9UiExtra"
	result := Default().Text(input)
	if result.Text == input || len(result.Findings) != 1 || result.Findings[0].Rule != "high_entropy_token" {
		t.Fatal("stable-ID prefix impostor bypassed entropy redaction")
	}
}

func TestDefaultRedactsTruncatedPrivateKey(t *testing.T) {
	input := "before -----BEGIN PRIVATE KEY-----\nCANARYTRUNCATEDPRIVATEKEY"
	result := Default().Text(input)
	if strings.Contains(strings.ToLower(result.Text), "canary") || !strings.Contains(result.Text, "[REDACTED:PRIVATE_KEY]") {
		t.Fatal("truncated private key leaked")
	}
}

func TestDefaultRedactsGenericCredentialURLButPreservesNormalURL(t *testing.T) {
	credentialURL := "https://admin:canary-password@example.test/path"
	result := Default().Text(credentialURL)
	if strings.Contains(strings.ToLower(result.Text), "canary") || !strings.Contains(result.Text, "[REDACTED:CONNECTION_URL]") {
		t.Fatal("generic credential URL leaked")
	}

	normalURL := "https://example.test/path?q=public"
	if got := Default().Text(normalURL).Text; got != normalURL {
		t.Fatal("normal URL was changed")
	}
}

func TestDefaultCountsBearerOnce(t *testing.T) {
	result := Default().Text("Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.synthetic.signature")
	if len(result.Findings) != 1 || result.Findings[0] != (Finding{Rule: "bearer", Count: 1}) {
		t.Fatal("bearer finding count was not semantically accurate")
	}
}

func TestDefaultRedactsUnclosedQuotedNamedSecretThroughEnd(t *testing.T) {
	input := `password="alpha bravo with no closing quote`
	result := Default().Text(input)
	if strings.Contains(result.Text, "alpha") || strings.Contains(result.Text, "bravo") || !strings.Contains(result.Text, "[REDACTED:NAMED_SECRET]") {
		t.Fatal("unclosed quoted named secret leaked")
	}
}

func TestDefaultRedactsCamelCaseSecretKey(t *testing.T) {
	input := `{"openaiApiKey":"short canary"}`
	result := Default().Text(input)
	if strings.Contains(strings.ToLower(result.Text), "canary") || !strings.Contains(result.Text, "[REDACTED:NAMED_SECRET]") {
		t.Fatal("camelCase secret key leaked")
	}
}

func TestDefaultRedactsBracketedNamedSecretValue(t *testing.T) {
	input := `password=[short-canary]`
	result := Default().Text(input)
	if strings.Contains(strings.ToLower(result.Text), "canary") || !strings.Contains(result.Text, "[REDACTED:NAMED_SECRET]") {
		t.Fatal("bracketed named secret leaked")
	}
}

func TestDefaultCountsQuotedAuthorizationBearerOnce(t *testing.T) {
	result := Default().Text(`{"authorization":"Bearer eyJhbGciOiJIUzI1NiJ9.synthetic.signature"}`)
	if len(result.Findings) != 1 || result.Findings[0] != (Finding{Rule: "bearer", Count: 1}) {
		t.Fatal("quoted bearer finding count was not semantically accurate")
	}
}

func TestDefaultPreservesHarmlessJSONAndCamelCaseFields(t *testing.T) {
	inputs := []string{
		`{"monkey":"public value","secretary":"public"}`,
		`{"apiKeyboard":"public","authorizationMode":"public"}`,
		`{"passwordHint":"public","cookiePolicy":"public"}`,
	}
	for i, input := range inputs {
		result := Default().Text(input)
		if result.Text != input || len(result.Findings) != 0 {
			t.Fatalf("harmless structured case %d was changed", i)
		}
	}
}

func TestDefaultRedactsControlAndMultilineNamedSecretValues(t *testing.T) {
	cases := []struct {
		input    string
		neighbor string
	}{
		{"password=alpha\x00bravo; mode=public", "; mode=public"},
		{"password=\"alpha\x00bravo\", \"mode\":\"public\"", `, "mode":"public"`},
		{"password=\"alpha\nbravo\"\nmode=public", "\nmode=public"},
		{"password=\"alpha\r\nbravo\"\r\nmode=public", "\r\nmode=public"},
		{"password=alpha\nbravo\nmode=public", "\nmode=public"},
		{"password=alpha\r\nbravo\r\nmode=public", "\r\nmode=public"},
		{"password=\"alpha\nbravo\nmode=public", "\nmode=public"},
	}
	for i, tc := range cases {
		result := Default().Text(tc.input)
		if strings.Contains(result.Text, "alpha") || strings.Contains(result.Text, "bravo") {
			t.Fatalf("control or multiline case %d leaked", i)
		}
		if !strings.Contains(result.Text, tc.neighbor) {
			t.Fatalf("control or multiline case %d swallowed neighboring field", i)
		}
		if len(result.Findings) != 1 || result.Findings[0] != (Finding{Rule: "named_secret", Count: 1}) {
			t.Fatalf("control or multiline case %d had inaccurate findings", i)
		}
	}
}
