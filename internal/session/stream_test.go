package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamFromLinePreservesOffsetsHashesAndUnterminatedEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	first := `{"timestamp":"2026-08-22T10:00:00Z","type":"one","payload":{}}`
	second := `{"timestamp":"2026-08-22T10:01:00Z","type":"two","payload":{}}`
	if err := os.WriteFile(path, []byte(first+"\n"+second), 0o600); err != nil {
		t.Fatal(err)
	}
	var records []Record
	summary, err := Stream(path, DecodeOptions{FromLine: 2}, func(record Record) error { records = append(records, record); return nil })
	if err != nil || summary.Lines != 2 || len(records) != 1 {
		t.Fatalf("summary=%+v records=%+v err=%v", summary, records, err)
	}
	if records[0].Line != 2 || records[0].ByteOffset != int64(len(first)+1) || records[0].SourceHash == "" {
		t.Fatalf("record=%+v", records[0])
	}
}

func TestStreamReturnsVisitorError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"event","payload":{}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := errors.New("visitor stopped")
	_, err := Stream(path, DecodeOptions{}, func(Record) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

func TestStreamReaderRejectsNilVisitor(t *testing.T) {
	if _, err := StreamReader(strings.NewReader("{\"type\":\"event\"}\n"), DecodeOptions{}, nil); err == nil {
		t.Fatal("nil visitor accepted")
	}
}

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
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("oversized error lacks source path: %v", err)
	}
}
