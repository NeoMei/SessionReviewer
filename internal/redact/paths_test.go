package redact

import "testing"

func TestAbsolutePathsPreservesExistingSanitizerSemantics(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"open file:///Users/alice/private.txt now", "open [REDACTED:ABSOLUTE_PATH] now"},
		{"path:/Users/alice/private.txt", "path:[REDACTED:ABSOLUTE_PATH]"},
		{`share:\\server\private\file.txt`, "share:[REDACTED:ABSOLUTE_PATH]"},
		{`path:C:\Users\alice\private.txt`, "path:[REDACTED:ABSOLUTE_PATH]"},
		{"<root>/Users/private/repo</root>", "<root>[REDACTED:ABSOLUTE_PATH]</root>"},
		{"Discuss </environment_context> literally", "Discuss </environment_context> literally"},
	}
	for _, test := range tests {
		if got := AbsolutePaths(test.input); got != test.want {
			t.Fatalf("AbsolutePaths(%q)=%q want %q", test.input, got, test.want)
		}
	}
}
