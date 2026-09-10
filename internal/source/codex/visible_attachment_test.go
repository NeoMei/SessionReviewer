package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/conversationchain"
	"github.com/neomei/SessionReviewer/internal/memory"
)

func TestVisibleImageMessageRetainsTextAndOriginalCoordinates(t *testing.T) {
	const id = "12345678-1234-4234-8234-123456789abc"
	for _, size := range []int{285 << 10, 2 << 20} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			root := t.TempDir()
			question := "这里的UI 设计简直丑爆了，怎么好意思做成这样的?"
			meta := encodedRecord(t, "2026-09-08T01:00:00Z", "session_meta", map[string]any{"id": id})
			user := encodedRecord(t, "2026-09-08T01:00:01Z", "response_item", map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": question}, map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", size)}}})
			answer := encodedRecord(t, "2026-09-08T01:00:02Z", "response_item", map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "我会修正界面层级。"}}})
			body := meta + user + answer
			sum := sha256.Sum256([]byte(body))
			record := memory.SourceRecord{Provider: "codex", SessionID: id, SourceIdentity: "source-test", FrozenBoundary: memory.FrozenBoundary{Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 3, ByteOffset: int64(len(body))}}, SourceHash: hex.EncodeToString(sum[:])}}
			path := filepath.Join(root, "rollout-2026-09-08T01-00-00-"+id+".jsonl")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			messages, coverage, err := ReadPublishedVisible(context.Background(), root, record)
			if err != nil || len(messages) != 2 || messages[0].Text != question || coverage.OversizedRecords != 0 {
				t.Fatalf("messages=%d coverage=%+v err=%v", len(messages), coverage, err)
			}
			expected := sha256.Sum256([]byte(strings.TrimSpace(user)))
			if messages[0].RecordOrdinal != 2 || messages[0].RecordHash != hex.EncodeToString(expected[:]) || messages[1].RecordOrdinal != 3 {
				t.Fatal("source coordinates/hash changed")
			}
			for _, rule := range []string{conversationchain.LegacySegmentationRuleVersion, conversationchain.NotificationSegmentationRuleVersion, conversationchain.InterruptionSegmentationRuleVersion} {
				old, cov, err := ReadPublishedVisibleVersion(context.Background(), root, record, rule)
				if err != nil || len(old) != 1 || cov.OversizedRecords != 1 {
					t.Fatalf("historical policy %s changed: %d %+v %v", rule, len(old), cov, err)
				}
			}
			// Image bytes still authenticate the message even though they are not displayed.
			tampered := strings.Replace(body, "base64,A", "base64,B", 1)
			os.WriteFile(path, []byte(tampered), 0600)
			if _, _, err := ReadPublishedVisible(context.Background(), root, record); err == nil {
				t.Fatal("image mutation escaped prefix authentication")
			}
		})
	}
}

func TestVisibleAttachmentLimitsDoNotAdmitOtherLargeRecords(t *testing.T) {
	const id = "12345678-1234-4234-8234-123456789abc"
	for _, tc := range []struct {
		name, role, text string
		image            int
	}{
		{"text-only", "user", strings.Repeat("x", 70<<10), 0},
		{"too-much-text", "user", strings.Repeat("x", 70<<10), 100 << 10},
		{"too-large-image", "user", "bounded text", 4 << 20},
		{"assistant-image", "assistant", "bounded text", 100 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := []any{map[string]any{"type": "input_text", "text": tc.text}}
			if tc.image > 0 {
				content = append(content, map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", tc.image)})
			}
			meta := encodedRecord(t, "2026-09-08T01:00:00Z", "session_meta", map[string]any{"id": id})
			oversized := encodedRecord(t, "2026-09-08T01:00:01Z", "response_item", map[string]any{"type": "message", "role": tc.role, "content": content})
			later := encodedRecord(t, "2026-09-08T01:00:02Z", "response_item", map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "later question"}}})
			body := meta + oversized + later
			sum := sha256.Sum256([]byte(body))
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "rollout-2026-09-08T01-00-00-"+id+".jsonl"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			record := memory.SourceRecord{Provider: "codex", SessionID: id, SourceIdentity: "source-test", FrozenBoundary: memory.FrozenBoundary{Location: memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 3, ByteOffset: int64(len(body))}}, SourceHash: hex.EncodeToString(sum[:])}}
			messages, cov, err := ReadPublishedVisible(context.Background(), root, record)
			if err != nil || len(messages) != 1 || messages[0].Text != "later question" || messages[0].RecordOrdinal != 3 || cov.OversizedRecords != 1 {
				t.Fatalf("messages=%+v coverage=%+v err=%v", messages, cov, err)
			}
		})
	}
}
