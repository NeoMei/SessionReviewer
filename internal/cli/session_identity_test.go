package cli

import (
	"strings"
	"testing"
)

func TestNativeSessionIDContractsPreserveCaseAndSafety(t *testing.T) {
	parsers := []struct {
		name  string
		args  []string
		parse func([]string) (string, error)
	}{
		{"summary", []string{"session-summary", "--project-id", "project-p", "--provider", "opencode", "--session-id", "ses_nativeChild", "--expected-generation-id", "generation-1", "--json"}, func(a []string) (string, error) { v, e := ParseInspectContract(a); return v.SessionID, e }},
		{"events", []string{"session-events", "--project-id", "project-p", "--provider", "opencode", "--session-id", "ses_nativeChild", "--expected-generation-id", "generation-1", "--limit", "20", "--json"}, func(a []string) (string, error) { v, e := ParseInspectContract(a); return v.SessionID, e }},
		{"conversation", []string{"conversation-chain", "--project-id", "project-p", "--provider", "opencode", "--session-id", "ses_nativeChild", "--expected-generation-id", "generation-1", "--limit", "20", "--json"}, func(a []string) (string, error) { v, e := ParseInspectContract(a); return v.SessionID, e }},
		{"launch", []string{"open", "--project-id", "project-p", "--provider", "opencode", "--session-id", "ses_nativeChild", "--expected-generation-id", "generation-1", "--expected-session-view-digest", contractTestDigest, "--json"}, func(a []string) (string, error) { v, e := ParseSessionOpenContract(a); return v.SessionID, e }},
		{"pricing", []string{"supplement", "--project-id", "project-p", "--provider", "opencode", "--session-id", "ses_nativeChild", "--usage-record-digest", contractTestDigest, "--expected-ledger-sha256", contractTestSHA, "--json"}, func(a []string) (string, error) { v, e := ParsePricingContract(a); return v.SessionID, e }},
	}
	for _, parser := range parsers {
		t.Run(parser.name, func(t *testing.T) {
			if got, err := parser.parse(parser.args); err != nil || got != "ses_nativeChild" {
				t.Fatalf("Session ID=%q err=%v", got, err)
			}
			for _, invalid := range []struct{ flag, value string }{{"--session-id", "../session"}, {"--session-id", "ses/Child"}, {"--session-id", "ses:Child"}, {"--session-id", strings.Repeat("s", 129)}, {"--provider", "OpenCode"}, {"--project-id", "project-P"}} {
				args := append([]string(nil), parser.args...)
				for i := range args {
					if args[i] == invalid.flag {
						args[i+1] = invalid.value
						break
					}
				}
				if _, err := parser.parse(args); err == nil {
					t.Errorf("accepted %s=%q", invalid.flag, invalid.value)
				}
			}
		})
	}
}
