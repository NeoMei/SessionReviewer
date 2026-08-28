package evidence

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/redact"
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

func TestEvidenceV2SchemaWarningAndEventIDContractsMatchRuntime(t *testing.T) {
	body, err := os.ReadFile("../../schemas/evidence-v2.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
		Defs       map[string]struct {
			Properties map[string]map[string]any `json:"properties"`
			OneOf      []map[string]any          `json:"oneOf"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatal(err)
	}

	eventIDPattern, _ := schema.Defs["event"].Properties["id"]["pattern"].(string)
	if eventIDPattern != EventIDPattern {
		t.Fatalf("event id pattern=%q want runtime %q", eventIDPattern, EventIDPattern)
	}

	warnings, _ := schema.Properties["warnings"].(map[string]any)
	items, _ := warnings["items"].(map[string]any)
	if got := items["$ref"]; got != "#/$defs/warning" {
		t.Fatalf("warnings item ref=%v", got)
	}
	patterns := make([]string, 0, len(schema.Defs["warning"].OneOf))
	for _, variant := range schema.Defs["warning"].OneOf {
		pattern, _ := variant["pattern"].(string)
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)
	rules := redact.KnownRuleNames()
	quoted := make([]string, len(rules))
	for index, rule := range rules {
		quoted[index] = regexp.QuoteMeta(rule)
	}
	wantPatterns := []string{
		"^malformed_jsonl_lines:[1-9][0-9]*$",
		"^redacted:(" + strings.Join(quoted, "|") + "):[1-9][0-9]*$",
	}
	sort.Strings(wantPatterns)
	if !reflect.DeepEqual(patterns, wantPatterns) {
		t.Fatalf("warning patterns=%v want runtime vocabulary %v", patterns, wantPatterns)
	}
}

func TestEvidenceV2SchemaCopiesCannotDrift(t *testing.T) {
	canonicalPath := filepath.Join("..", "..", "schemas", "evidence-v2.schema.json")
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	copies := 0
	err = filepath.WalkDir(filepath.Join("..", ".."), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".codegraph", ".superpowers", "dist", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "evidence-v2.schema.json" {
			return nil
		}
		copies++
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(body, canonical) {
			t.Errorf("evidence schema copy %q differs from %q", path, canonicalPath)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if copies == 0 {
		t.Fatal("authoritative evidence schema was not discovered")
	}
}
