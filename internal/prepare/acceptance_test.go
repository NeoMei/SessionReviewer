package prepare

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/cursor"
	"github.com/neomei/SessionReviewer/internal/evidence"
	"github.com/neomei/SessionReviewer/internal/project"
)

const (
	foundationProjectID = "project-1010101010101010"
	foundationSessionID = "foundation-session"
)

type foundationFixture struct {
	root        string
	sessions    string
	data        string
	projectRoot string
	vaultRoot   string
	sessionPath string
	outputPath  string
	now         time.Time
}

func newFoundationFixture(t *testing.T) foundationFixture {
	t.Helper()
	root := t.TempDir()
	fixture := foundationFixture{
		root:        root,
		sessions:    filepath.Join(root, "sessions"),
		data:        filepath.Join(root, "data"),
		projectRoot: filepath.Join(root, "project"),
		vaultRoot:   filepath.Join(root, "vault"),
		sessionPath: filepath.Join(root, "sessions", "rollout.jsonl"),
		outputPath:  filepath.Join(root, "output", "evidence.json"),
		now:         time.Date(2026, 8, 22, 10, 2, 0, 0, time.UTC),
	}
	for _, directory := range []string{fixture.sessions, fixture.data, fixture.projectRoot, fixture.vaultRoot} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: foundationProjectID, Root: fixture.projectRoot, VaultRoot: fixture.vaultRoot,
	}}}
	if err := config.Save(filepath.Join(fixture.data, "config.toml"), cfg); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture foundationFixture) options(mode string) Options {
	return Options{
		Mode: mode, SessionsRoot: fixture.sessions, SessionID: foundationSessionID,
		CWD: fixture.projectRoot, DataDir: fixture.data, Output: fixture.outputPath,
		GOOS: runtime.GOOS, Now: fixture.now, AmbiguityWindow: time.Second,
	}
}

func (fixture foundationFixture) writeSession(t *testing.T, records ...any) {
	t.Helper()
	file, err := os.OpenFile(fixture.sessionPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(file, 64<<10)
	writeJSONLine(t, writer, map[string]any{
		"timestamp": "2026-08-22T10:00:00Z",
		"type":      "session_meta",
		"payload": map[string]any{
			"id": foundationSessionID, "cwd": fixture.projectRoot, "source": "test",
		},
	})
	for _, record := range records {
		writeJSONLine(t, writer, record)
	}
	if err := writer.Flush(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeJSONLine(t *testing.T, writer io.Writer, value any) int {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if _, err := writer.Write(encoded); err != nil {
		t.Fatal(err)
	}
	return len(encoded)
}

func responseItem(itemType, id string, fields map[string]any) map[string]any {
	payload := map[string]any{"type": itemType, "id": id}
	for key, value := range fields {
		payload[key] = value
	}
	return map[string]any{
		"timestamp": "2026-08-22T10:01:00Z",
		"type":      "response_item",
		"payload":   payload,
	}
}

func messageRecord(id, role, text string) map[string]any {
	return responseItem("message", id, map[string]any{
		"role": role,
		"content": []map[string]any{{
			"type": "input_text", "text": text,
		}},
	})
}

func TestFoundationLargeSessionReachesBoundedPacketAfterStreamingPast20MiB(t *testing.T) {
	fixture := newFoundationFixture(t)
	file, err := os.OpenFile(fixture.sessionPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(file, 64<<10)
	written := int64(writeJSONLine(t, writer, map[string]any{
		"timestamp": "2026-08-22T10:00:00Z",
		"type":      "session_meta",
		"payload": map[string]any{
			"id": foundationSessionID, "cwd": fixture.projectRoot, "source": "test",
		},
	}))

	seededCanaries := []string{
		"DEVELOPER-FOUNDATION-CANARY",
		"SYSTEM-FOUNDATION-CANARY",
		"REASONING-FOUNDATION-CANARY",
		"ENCRYPTED-CONTENT-FOUNDATION-CANARY",
		"COMPACTION-FOUNDATION-CANARY",
		"UNKNOWN-FOUNDATION-CANARY",
		"sk-foundation-canary-123456789012345678901234567890",
	}
	excluded := []any{
		messageRecord("developer", "developer", seededCanaries[0]),
		messageRecord("system", "system", seededCanaries[1]),
		responseItem("reasoning", "reasoning", map[string]any{"summary": seededCanaries[2]}),
		responseItem("encrypted_content", "encrypted", map[string]any{"encrypted_content": seededCanaries[3]}),
		responseItem("compaction", "compaction", map[string]any{"content": seededCanaries[4]}),
		responseItem("future_unknown", "unknown", map[string]any{"content": seededCanaries[5]}),
	}
	for _, record := range excluded {
		written += int64(writeJSONLine(t, writer, record))
	}

	largeUnknown := responseItem("future_unknown", "bulk", map[string]any{
		"content": strings.Repeat("x", 256<<10) + " " + seededCanaries[5],
	})
	largeLine, err := json.Marshal(largeUnknown)
	if err != nil {
		t.Fatal(err)
	}
	largeLine = append(largeLine, '\n')
	const minimumFixtureBytes = int64(20 << 20)
	bulkRecords := 0
	for written <= minimumFixtureBytes {
		count, err := writer.Write(largeLine)
		if err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		written += int64(count)
		bulkRecords++
	}
	prefixBytes := written
	if prefixBytes <= minimumFixtureBytes {
		t.Fatalf("pre-evidence prefix_bytes=%d, want >%d", prefixBytes, minimumFixtureBytes)
	}
	tailStartLine := 1 + len(excluded) + bulkRecords + 1
	const tailRecords = 400
	for index := range tailRecords {
		writeJSONLine(t, writer, responseItem("custom_tool_call_output", fmt.Sprintf("tail-%03d", index), map[string]any{
			"output": "OPENAI_API_KEY=" + seededCanaries[6] + "; result=" + strings.Repeat("allowlisted-tail-", 100),
		}))
	}
	if err := writer.Flush(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(fixture.sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= minimumFixtureBytes {
		t.Fatalf("fixture_bytes=%d, want >%d", info.Size(), minimumFixtureBytes)
	}

	runtime.GC()
	var memoryBefore, memoryAfter runtime.MemStats
	runtime.ReadMemStats(&memoryBefore)
	started := time.Now()
	packet, err := Run(fixture.options("checkpoint"))
	duration := time.Since(started)
	runtime.ReadMemStats(&memoryAfter)
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(fixture.outputPath)
	if err != nil {
		t.Fatal(err)
	}

	limits := evidence.DefaultLimits()
	trimmedOutput := bytes.TrimSuffix(output, []byte{'\n'})
	marshaledPacket, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(trimmedOutput, marshaledPacket) {
		t.Fatal("persisted packet bytes differ from the returned packet")
	}
	outputRunes := utf8.RuneCount(trimmedOutput)
	if outputRunes > limits.MaxPacketRunes || len(output) > limits.MaxPacketRunes*utf8.UTFMax+1 {
		t.Fatalf("output_runes=%d output_bytes=%d max_packet_runes=%d", outputRunes, len(output), limits.MaxPacketRunes)
	}
	if outputRunes < limits.MaxPacketRunes-2*limits.MaxSummaryRunes || len(output) < 250000 {
		t.Fatalf("packet did not materially exercise its bound: runes=%d bytes=%d", outputRunes, len(output))
	}
	for index, canary := range seededCanaries {
		if bytes.Contains(output, []byte(canary)) {
			t.Fatalf("seeded canary %d leaked into evidence output", index)
		}
	}
	if !packet.HasMore || len(packet.Events) < 100 {
		t.Fatalf("packet did not reach a substantial full segment: has_more=%v events=%d", packet.HasMore, len(packet.Events))
	}
	firstEvent := packet.Events[0]
	lastEvent := packet.Events[len(packet.Events)-1]
	totalLines := 1 + len(excluded) + bulkRecords + tailRecords
	if packet.FromCursor != 1 || firstEvent.JSONLLine != tailStartLine || lastEvent.JSONLLine != packet.ToCursor || packet.ToCursor >= totalLines {
		t.Fatalf("unexpected late-tail provenance: from=%d first_line=%d last_line=%d to=%d total=%d", packet.FromCursor, firstEvent.JSONLLine, lastEvent.JSONLLine, packet.ToCursor, totalLines)
	}
	if firstEvent.Kind != "tool_result" || firstEvent.Summary == "" ||
		!strings.Contains(firstEvent.Summary, "[REDACTED:NAMED_SECRET]") ||
		!strings.Contains(firstEvent.Summary, "…[TRUNCATED]") {
		t.Fatalf("first allowlisted summary is not bounded and redacted: %q", firstEvent.Summary)
	}
	wantWarning := fmt.Sprintf("redacted:named_secret:%d", len(packet.Events))
	wantWarnings := []string{"redacted:high_entropy_token:1", wantWarning}
	if !reflect.DeepEqual(packet.Warnings, wantWarnings) {
		t.Fatalf("warnings=%v want=%v", packet.Warnings, wantWarnings)
	}
	for index, event := range packet.Events {
		wantLine := tailStartLine + index
		if event.Kind != "tool_result" || event.JSONLLine != wantLine || len(event.SourceHash) != 64 || event.ID == "" ||
			!strings.Contains(event.Summary, "[REDACTED:NAMED_SECRET]") || !strings.Contains(event.Summary, "…[TRUNCATED]") {
			t.Fatalf("event %d has invalid allowlist/provenance fields: %+v", index, event)
		}
	}
	allocated := memoryAfter.TotalAlloc - memoryBefore.TotalAlloc
	t.Logf("fixture_bytes=%d prefix_bytes=%d output_bytes=%d output_runes=%d events=%d to_cursor=%d duration=%s approx_total_alloc_bytes=%d", info.Size(), prefixBytes, len(output), outputRunes, len(packet.Events), packet.ToCursor, duration.Round(time.Millisecond), allocated)
}

type cursorFileSnapshot struct {
	Mode    os.FileMode
	ModTime time.Time
	Body    string
}

func snapshotDirectory(t *testing.T, directory string) map[string]cursorFileSnapshot {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := make(map[string]cursorFileSnapshot, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		snapshot[entry.Name()] = cursorFileSnapshot{Mode: info.Mode(), ModTime: info.ModTime(), Body: string(body)}
	}
	return snapshot
}

func TestFoundationPrepareFromStartIsByteStableAndCursorSideEffectFree(t *testing.T) {
	fixture := newFoundationFixture(t)
	fixture.writeSession(t,
		messageRecord("user", "user", "continue the project"),
		responseItem("custom_tool_call", "call", map[string]any{"name": "exec_command", "input": `{"cmd":"go test ./..."}`}),
	)
	cursorDir := filepath.Join(fixture.data, "projects", foundationProjectID, "cursors")
	if err := os.MkdirAll(cursorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		foundationSessionID + ".json":                         "{corrupt-primary",
		foundationSessionID + ".json.session-reviewer-backup": "{corrupt-backup",
		"." + foundationSessionID + ".lock":                   "untouched-lock-sentinel",
		".session-reviewer-stale-temp":                        "untouched-temp-sentinel",
	} {
		if err := os.WriteFile(filepath.Join(cursorDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	beforeCursorState := snapshotDirectory(t, cursorDir)
	opts := fixture.options("review")
	opts.FromStart = true
	firstPacket, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(fixture.outputPath)
	if err != nil {
		t.Fatal(err)
	}
	secondPacket, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(fixture.outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || !reflect.DeepEqual(firstPacket, secondPacket) {
		t.Fatal("unchanged input produced different prepare bytes or packet values")
	}
	if firstPacket.FromCursor != 1 || firstPacket.ToCursor != 3 {
		t.Fatalf("packet=%+v", firstPacket)
	}
	afterCursorState := snapshotDirectory(t, cursorDir)
	if !reflect.DeepEqual(beforeCursorState, afterCursorState) {
		t.Fatalf("from-start changed cursor, repair, temp, or lock state:\nbefore=%+v\nafter=%+v", beforeCursorState, afterCursorState)
	}
}

func TestFoundationPacketFullResumesAtFirstRejectedEvent(t *testing.T) {
	fixture := newFoundationFixture(t)
	fixture.writeSession(t,
		messageRecord("first", "user", "first"),
		messageRecord("second", "user", "second"),
		messageRecord("third", "user", "third"),
	)
	projectData := filepath.Join(fixture.data, "projects", foundationProjectID)
	if err := os.MkdirAll(projectData, 0o700); err != nil {
		t.Fatal(err)
	}
	store := cursor.Store{Root: projectData}
	expectedCursor := cursor.Cursor{}
	for index, wantSummary := range []string{"first", "second", "third"} {
		opts := fixture.options("checkpoint")
		opts.Limits = evidence.DefaultLimits()
		opts.Limits.MaxEvents = 1
		var cursorBytes []byte
		cursorPath := filepath.Join(projectData, "cursors", foundationSessionID+".json")
		if index > 0 {
			var readErr error
			cursorBytes, readErr = os.ReadFile(cursorPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
		}
		packet, err := Run(opts)
		if err != nil {
			t.Fatal(err)
		}
		if index > 0 {
			after, err := os.ReadFile(cursorPath)
			if err != nil || !bytes.Equal(cursorBytes, after) {
				t.Fatalf("prepare changed accepted cursor: before=%q after=%q err=%v", cursorBytes, after, err)
			}
		}
		wantLine := index + 2
		wantFrom := wantLine
		if index == 0 {
			wantFrom = 1
		}
		if packet.FromCursor != wantFrom || packet.ToCursor != wantLine || len(packet.Events) != 1 || packet.Events[0].Summary != wantSummary {
			t.Fatalf("segment %d packet=%+v", index+1, packet)
		}
		if packet.HasMore != (index < 2) {
			t.Fatalf("segment %d has_more=%v", index+1, packet.HasMore)
		}
		next := cursor.Cursor{
			SessionID: foundationSessionID,
			LastLine:  packet.ToCursor,
			LastHash:  packet.Events[0].SourceHash,
			UpdatedAt: fixture.now.Add(time.Duration(index) * time.Second),
		}
		if err := store.Commit(foundationSessionID, expectedCursor, next); err != nil {
			t.Fatal(err)
		}
		expectedCursor = next
	}
}

func TestFoundationInitializeIsIdempotentAndRejectsNestedRoots(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "project")
	vaultRoot := filepath.Join(t.TempDir(), "vault")
	dataRoot := t.TempDir()
	for _, directory := range []string{projectRoot, vaultRoot} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	opts := project.InitOptions{
		ProjectRoot: projectRoot,
		VaultRoot:   vaultRoot,
		DataDir:     dataRoot,
		GOOS:        runtime.GOOS,
		Now: func() time.Time {
			return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
		},
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
	}
	first, err := project.Initialize(opts)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dataRoot, "config.toml")
	overviewPath := filepath.Join(projectRoot, "docs", "session-review", "project-overview.md")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	overviewBefore, err := os.ReadFile(overviewPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := project.Initialize(opts)
	if err != nil {
		t.Fatal(err)
	}
	configAfter, _ := os.ReadFile(configPath)
	overviewAfter, _ := os.ReadFile(overviewPath)
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(loaded.Projects) != 1 || !bytes.Equal(configBefore, configAfter) || !bytes.Equal(overviewBefore, overviewAfter) {
		t.Fatalf("initialization is not idempotent: first=%+v second=%+v projects=%d", first, second, len(loaded.Projects))
	}

	for _, test := range []struct {
		name    string
		project string
		vault   string
	}{
		{name: "vault inside project", project: filepath.Join(t.TempDir(), "project"), vault: "vault-child"},
		{name: "project inside vault", project: "project-child", vault: filepath.Join(t.TempDir(), "vault")},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectPath, vaultPath := test.project, test.vault
			if test.name == "vault inside project" {
				vaultPath = filepath.Join(projectPath, vaultPath)
			} else {
				projectPath = filepath.Join(vaultPath, projectPath)
			}
			for _, directory := range []string{projectPath, vaultPath} {
				if err := os.MkdirAll(directory, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			_, err := project.Initialize(project.InitOptions{
				ProjectRoot: projectPath, VaultRoot: vaultPath, DataDir: t.TempDir(), GOOS: runtime.GOOS,
			})
			if err == nil || !strings.Contains(err.Error(), "must not contain") {
				t.Fatalf("nested roots accepted: %v", err)
			}
		})
	}
}
