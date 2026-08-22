package evidence

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestEvidenceV2SchemaHasStrictConditionalCursorHashes(t *testing.T) {
	body, err := os.ReadFile("../../schemas/evidence-v2.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatal("packet schema permits extra fields")
	}
	required, _ := schema["required"].([]any)
	for _, field := range []string{"schema_version", "project_id", "session_id", "cwd", "from_cursor", "to_cursor", "expected_cursor", "next_cursor", "has_more", "events"} {
		found := false
		for _, value := range required {
			found = found || value == field
		}
		if !found {
			t.Fatalf("required field %q missing", field)
		}
	}
	compact := strings.ReplaceAll(strings.ReplaceAll(string(body), " ", ""), "\n", "")
	for _, contract := range []string{`"const":0`, `"minimum":1`, `"required":["source_hash"]`, `"pattern":"^[0-9a-f]{64}$"`, `"not":{"required":["source_hash"]}`} {
		if !strings.Contains(compact, contract) {
			t.Fatalf("cursor condition %s missing", contract)
		}
	}
	for _, contract := range []string{`"cursor_boundary":{"type":"object","additionalProperties":false`, `"event":{"type":"object","additionalProperties":false`, `"required":["id","timestamp","jsonl_line","source_hash","kind","summary"]`} {
		if !strings.Contains(compact, contract) {
			t.Fatalf("strict nested schema contract %s missing", contract)
		}
	}
}
