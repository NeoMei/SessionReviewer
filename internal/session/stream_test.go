package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamPreservesProvenanceAndWarnsOnMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	content := "{\"timestamp\":\"2026-08-22T10:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"s1\"}}\nnot-json\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	var records []Record
	summary, err := Stream(path, DecodeOptions{MaxRecordBytes: 1 << 20}, func(r Record) error {
		records = append(records, r)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Line != 1 || records[0].SourceHash == "" {
		t.Fatalf("records=%+v", records)
	}
	if summary.MalformedLines != 1 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestStreamRejectsOversizedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.jsonl")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 1025)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Stream(path, DecodeOptions{MaxRecordBytes: 1024}, func(Record) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "exceeds 1024 bytes") {
		t.Fatalf("err=%v", err)
	}
}
