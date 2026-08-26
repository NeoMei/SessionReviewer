package sync

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/syncdoc"
)

var conflictTime = time.Date(2026, time.August, 23, 1, 2, 3, 456, time.FixedZone("test", 8*60*60))

func TestBuildConflictUsesDeterministicUTCIDAndExactHashes(t *testing.T) {
	t.Parallel()

	base := []byte("BASE")
	project := []byte("PROJECT")
	vault := []byte("VAULT")
	wantDigest := sha256.Sum256([]byte(syncdoc.ContentHash(base) + "|" + syncdoc.ContentHash(project) + "|" + syncdoc.ContentHash(vault)))
	wantID := fmt.Sprintf("conflict-decision-1-%x", wantDigest[:6])

	first, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Base: base, Project: project, Vault: vault,
		Suggested: []byte("SUGGESTED"), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Base: base, Project: project, Vault: vault,
		Suggested: []byte("SUGGESTED"), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Record == nil || first.Repair != nil || second.Record == nil {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if first.Record.ID != wantID || first.Record.CreatedAt.Location() != time.UTC {
		t.Fatalf("id=%q created=%s want=%q UTC", first.Record.ID, first.Record.CreatedAt, wantID)
	}
	if first.Record.BaseHash != syncdoc.ContentHash(base) || first.Record.ProjectHash != syncdoc.ContentHash(project) || first.Record.VaultHash != syncdoc.ContentHash(vault) {
		t.Fatalf("record hashes=%+v", first.Record)
	}
	if !bytes.Equal(first.Notes.Project.Content, second.Notes.Project.Content) || first.Notes.Project.RelativePath != second.Notes.Project.RelativePath {
		t.Fatal("same input did not produce deterministic note bytes and path")
	}
}

func TestBuildConflictIdentityDoesNotChurnWhenClockAdvances(t *testing.T) {
	t.Parallel()

	input := ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Base: []byte("BASE"), Project: []byte("PROJECT"), Vault: []byte("VAULT"),
		CreatedAt: conflictTime,
	}
	first, err := BuildConflict(input)
	if err != nil {
		t.Fatal(err)
	}
	input.CreatedAt = conflictTime.Add(24 * time.Hour)
	second, err := BuildConflict(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Record == nil || second.Record == nil || first.Record.ID != second.Record.ID {
		t.Fatalf("conflict identity churned with clock: first=%+v second=%+v", first.Record, second.Record)
	}
}

func TestRenderConflictUsesBoundedJSONAndMirrorsExactBytes(t *testing.T) {
	t.Parallel()

	project := []byte("---\ntitle: candidate\n---\n\n`````\n# not a heading to the note\n`````\n")
	vault := []byte("<script>not markup</script>\n")
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictArchiveEdit,
		RelativePath: "decisions/decision-1.md", Base: nil, Project: project, Vault: vault,
		Suggested: []byte("suggested without final newline"), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Record == nil {
		t.Fatalf("artifact=%+v", artifact)
	}
	note, err := RenderConflict(*artifact.Record)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(note, artifact.Notes.Project.Content) || !bytes.Equal(note, artifact.Notes.Vault.Content) || artifact.Notes.Project.RelativePath != artifact.Notes.Vault.RelativePath {
		t.Fatal("conflict notes are not byte-identical mirrors")
	}
	if artifact.Notes.Project.RelativePath != ".session-reviewer/conflicts/"+artifact.Record.ID+".json" {
		t.Fatalf("path=%q", artifact.Notes.Project.RelativePath)
	}
	if !json.Valid(note) || len(note) > MaxConflictRecordBytes {
		t.Fatalf("record is not bounded JSON: bytes=%d", len(note))
	}
	parsed, err := ParseConflictRecord(note)
	if err != nil || !bytes.Equal(parsed.Project, project) || !bytes.Equal(parsed.Vault, vault) || !bytes.Equal(parsed.Suggested, artifact.Record.Suggested) {
		t.Fatalf("candidate bytes were not preserved: parsed=%+v err=%v", parsed, err)
	}
	for _, want := range []string{artifact.Record.BaseHash, artifact.Record.ProjectHash, artifact.Record.VaultHash, `"resolution_status": "open"`} {
		if !strings.Contains(string(note), want) {
			t.Fatalf("record missing %q:\n%s", want, note)
		}
	}
}

func TestParseConflictRecordRejectsTrailingJSONAndGarbage(t *testing.T) {
	t.Parallel()

	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Project: []byte("PROJECT"), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range [][]byte{[]byte("{}"), []byte("not-json")} {
		body := append(bytes.Clone(artifact.Notes.Project.Content), suffix...)
		if _, err := ParseConflictRecord(body); !errors.Is(err, ErrInvalidConflict) {
			t.Fatalf("suffix=%q err=%v", suffix, err)
		}
	}
}

func TestParseConflictRecordRejectsDuplicateSecurityFields(t *testing.T) {
	t.Parallel()

	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Project: []byte("PROJECT"), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical := artifact.Notes.Project.Content
	for _, tc := range []struct {
		name, field, shadow string
	}{
		{name: "id", field: "id", shadow: `"shadow-id"`},
		{name: "path", field: "relative_path", shadow: `"shadow/path.md"`},
		{name: "hash", field: "project_hash", shadow: `"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`},
		{name: "candidate", field: "project", shadow: `"SHADOW-CANDIDATE"`},
		{name: "resolution", field: "resolution_status", shadow: `"resolved"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			needle := []byte(`"` + tc.field + `": `)
			index := bytes.Index(canonical, needle)
			if index < 0 {
				t.Fatalf("canonical conflict lacks %q", tc.field)
			}
			duplicate := append(bytes.Clone(canonical[:index]), []byte(`"`+tc.field+`": `+tc.shadow+",\n  ")...)
			duplicate = append(duplicate, canonical[index:]...)
			if _, err := ParseConflictRecord(duplicate); !errors.Is(err, ErrInvalidConflict) {
				t.Fatalf("duplicate %s accepted: err=%v\n%s", tc.field, err, duplicate)
			}
		})
	}
}

func TestParseConflictRecordRejectsCaseAliasesBeforeTypedDecode(t *testing.T) {
	t.Parallel()

	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Project: []byte("PROJECT"), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical := artifact.Notes.Project.Content
	for _, tc := range []struct {
		name, field, alias, shadow string
	}{
		{name: "id", field: "id", alias: "ID", shadow: `"shadow-id"`},
		{name: "path", field: "relative_path", alias: "RELATIVE_PATH", shadow: `"shadow/path.md"`},
		{name: "hash", field: "project_hash", alias: "PROJECT_HASH", shadow: `"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`},
		{name: "candidate", field: "project", alias: "PROJECT", shadow: `"SHADOW-CANDIDATE"`},
		{name: "resolution", field: "resolution_status", alias: "RESOLUTION_STATUS", shadow: `"resolved"`},
	} {
		t.Run(tc.name+"/alias-only", func(t *testing.T) {
			aliasOnly := bytes.Replace(canonical, []byte(`"`+tc.field+`"`), []byte(`"`+tc.alias+`"`), 1)
			if bytes.Equal(aliasOnly, canonical) {
				t.Fatalf("canonical conflict lacks %q", tc.field)
			}
			if _, err := ParseConflictRecord(aliasOnly); !errors.Is(err, ErrInvalidConflict) {
				t.Fatalf("alias-only %s accepted: err=%v\n%s", tc.alias, err, aliasOnly)
			}
		})
		t.Run(tc.name+"/canonical-and-alias", func(t *testing.T) {
			needle := []byte(`"` + tc.field + `": `)
			index := bytes.Index(canonical, needle)
			if index < 0 {
				t.Fatalf("canonical conflict lacks %q", tc.field)
			}
			both := append(bytes.Clone(canonical[:index]), []byte(`"`+tc.alias+`": `+tc.shadow+",\n  ")...)
			both = append(both, canonical[index:]...)
			if _, err := ParseConflictRecord(both); !errors.Is(err, ErrInvalidConflict) {
				t.Fatalf("canonical+alias %s accepted: err=%v\n%s", tc.alias, err, both)
			}
		})
	}
}

func TestParseConflictRecordRejectsObjectValuesOutsideExactWireTree(t *testing.T) {
	t.Parallel()
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Project: []byte("PROJECT"), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, replacement := range [][]byte{
		[]byte(`"project": {"content":"first","Content":"second"}`),
		[]byte(`"resolution_status": {"status":"open","Status":"resolved"}`),
	} {
		body := bytes.Replace(artifact.Notes.Project.Content, []byte(`"project": "PROJECT"`), replacement, 1)
		if bytes.Equal(body, artifact.Notes.Project.Content) && bytes.Contains(replacement, []byte("resolution_status")) {
			body = bytes.Replace(artifact.Notes.Project.Content, []byte(`"resolution_status": "open"`), replacement, 1)
		}
		if _, err := ParseConflictRecord(body); !errors.Is(err, ErrInvalidConflict) {
			t.Fatalf("nested object accepted: err=%v\n%s", err, body)
		}
	}
}

func TestParseConflictRecordAcceptsLegalNonCanonicalFormatting(t *testing.T) {
	t.Parallel()
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Project: []byte("PROJECT"), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(artifact.Notes.Project.Content, &wire); err != nil {
		t.Fatal(err)
	}
	compact, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseConflictRecord(compact)
	if err != nil || parsed.ID != artifact.Record.ID || !bytes.Equal(parsed.Project, artifact.Record.Project) {
		t.Fatalf("parsed=%+v err=%v", parsed, err)
	}
}

func TestParseConflictRecordRejectsInvalidUTF8BeforeCandidateAuthentication(t *testing.T) {
	t.Parallel()
	replacementCandidate := []byte("PRO\xef\xbf\xbdJECT")
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Project: replacementCandidate, CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	invalid := bytes.Replace(artifact.Notes.Project.Content, replacementCandidate, []byte("PRO\xffJECT"), 1)
	if bytes.Equal(invalid, artifact.Notes.Project.Content) {
		t.Fatal("replacement candidate was not present in rendered conflict")
	}
	if _, err := ParseConflictRecord(invalid); !errors.Is(err, ErrInvalidConflict) {
		t.Fatalf("invalid UTF-8 candidate wire was accepted: err=%v", err)
	}
}

func TestParseConflictRecordRejectsUnpairedSurrogateCandidateEscape(t *testing.T) {
	t.Parallel()
	replacementCandidate := []byte("PRO\xef\xbf\xbdJECT")
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Project: replacementCandidate, CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	unpaired := bytes.Replace(artifact.Notes.Project.Content, replacementCandidate, []byte(`PRO\ud800JECT`), 1)
	if bytes.Equal(unpaired, artifact.Notes.Project.Content) {
		t.Fatal("replacement candidate was not present in rendered conflict")
	}
	if _, err := ParseConflictRecord(unpaired); !errors.Is(err, ErrInvalidConflict) {
		t.Fatalf("unpaired surrogate candidate wire was accepted: err=%v", err)
	}
}

func TestParseConflictRecordAcceptsPairedSurrogateCandidateEscape(t *testing.T) {
	t.Parallel()
	candidate := []byte("A😀B")
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Project: candidate, CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	escaped := bytes.Replace(artifact.Notes.Project.Content, []byte("😀"), []byte(`\ud83d\ude00`), 1)
	parsed, err := ParseConflictRecord(escaped)
	if err != nil || !bytes.Equal(parsed.Project, candidate) {
		t.Fatalf("paired surrogate parsed=%q err=%v", parsed.Project, err)
	}
}

func TestParseConflictRecordRequiresEveryUnconditionalWireField(t *testing.T) {
	t.Parallel()
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"version", "id", "entity_id", "project_id", "kind", "relative_path",
		"base_hash", "project_hash", "vault_hash", "base", "project", "vault", "suggested",
		"created_at", "resolution_status",
	} {
		t.Run(field, func(t *testing.T) {
			omitted := conflictWireWithMutation(t, artifact.Notes.Project.Content, func(wire map[string]any) { delete(wire, field) })
			if _, err := ParseConflictRecord(omitted); !errors.Is(err, ErrInvalidConflict) {
				t.Fatalf("missing required field %q accepted: err=%v\n%s", field, err, omitted)
			}
		})
	}
	parsed, err := ParseConflictRecord(artifact.Notes.Project.Content)
	if err != nil || len(parsed.Base) != 0 || len(parsed.Project) != 0 || len(parsed.Vault) != 0 || len(parsed.Suggested) != 0 {
		t.Fatalf("required empty candidate fields were not preserved: parsed=%+v err=%v", parsed, err)
	}
}

func TestParseConflictRecordRequiresResolvedWireFieldsConditionally(t *testing.T) {
	t.Parallel()
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Project: []byte("PROJECT"), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := MarkConflictResolved(*artifact.Record, AcceptProject, artifact.Record.ProjectHash, conflictTime.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderConflict(resolved)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"resolution_action", "resolved_hash", "resolved_at"} {
		t.Run(field, func(t *testing.T) {
			omitted := conflictWireWithMutation(t, body, func(wire map[string]any) { delete(wire, field) })
			if _, err := ParseConflictRecord(omitted); !errors.Is(err, ErrInvalidConflict) {
				t.Fatalf("resolved record missing %q was accepted: err=%v", field, err)
			}
		})
	}
	for _, field := range []string{"resolution_action", "resolved_hash", "resolved_at"} {
		t.Run("open-empty-"+field, func(t *testing.T) {
			presentEmpty := conflictWireWithMutation(t, artifact.Notes.Project.Content, func(wire map[string]any) { wire[field] = "" })
			if _, err := ParseConflictRecord(presentEmpty); !errors.Is(err, ErrInvalidConflict) {
				t.Fatalf("open record with empty optional %q was accepted: err=%v", field, err)
			}
		})
	}
}

func TestParseConflictRecordDistinguishesOptionalPathsFromEmptyOrNull(t *testing.T) {
	t.Parallel()
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", BasePath: "decisions/base.md",
		ProjectPath: "decisions/project.md", VaultPath: "decisions/vault.md", CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseConflictRecord(artifact.Notes.Project.Content); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"base_path", "project_path", "vault_path"} {
		t.Run(field+"/absent", func(t *testing.T) {
			omitted := conflictWireWithMutation(t, artifact.Notes.Project.Content, func(wire map[string]any) { delete(wire, field) })
			if _, err := ParseConflictRecord(omitted); err != nil {
				t.Fatalf("optional path %q omission rejected: %v", field, err)
			}
		})
		for _, value := range []any{"", nil} {
			name := "empty"
			if value == nil {
				name = "null"
			}
			t.Run(field+"/"+name, func(t *testing.T) {
				mutated := conflictWireWithMutation(t, artifact.Notes.Project.Content, func(wire map[string]any) { wire[field] = value })
				if _, err := ParseConflictRecord(mutated); !errors.Is(err, ErrInvalidConflict) {
					t.Fatalf("optional path %q with %s accepted: err=%v", field, name, err)
				}
			})
		}
	}
	for _, field := range []string{"base", "project", "vault", "suggested"} {
		t.Run("required-null-"+field, func(t *testing.T) {
			mutated := conflictWireWithMutation(t, artifact.Notes.Project.Content, func(wire map[string]any) { wire[field] = nil })
			if _, err := ParseConflictRecord(mutated); !errors.Is(err, ErrInvalidConflict) {
				t.Fatalf("required field %q accepted null: err=%v", field, err)
			}
		})
	}
}

func conflictWireWithMutation(t *testing.T, body []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	mutate(wire)
	mutated, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return mutated
}

func TestConflictDuplicateKeyScannerRecurses(t *testing.T) {
	t.Parallel()
	for _, body := range [][]byte{
		[]byte(`{"outer":{"candidate":"first","candidate":"second"}}`),
		[]byte(`{"outer":[{"hash":"first","hash":"second"}]}`),
	} {
		if err := rejectDuplicateConflictJSONKeys(body); err == nil {
			t.Fatalf("nested duplicate accepted: %s", body)
		}
	}
}

func TestBuildConflictReturnsDefensiveCopies(t *testing.T) {
	t.Parallel()

	project := []byte("PROJECT")
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Project: project, CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantNote := bytes.Clone(artifact.Notes.Vault.Content)
	project[0] = 'X'
	artifact.Record.Project[0] = 'Y'
	artifact.Notes.Project.Content[0] = 'Z'
	if !bytes.Equal(artifact.Notes.Vault.Content, wantNote) {
		t.Fatal("artifact aliases caller, record, or sibling note bytes")
	}
}

func TestBuildConflictConvertsEverySecretBearingCandidateToContentFreeRepair(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"base", "project", "vault", "suggested"} {
		field := field
		t.Run(field, func(t *testing.T) {
			canary := "api_key=sk-abcdefghijklmnopqrstuvwxyz012345"
			record := ConflictRecord{
				Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
				RelativePath: "decisions/decision-1.md", Base: []byte("safe-base"), Project: []byte("safe-project"), Vault: []byte("safe-vault"), Suggested: []byte("safe-suggested"), CreatedAt: conflictTime,
			}
			switch field {
			case "base":
				record.Base = []byte(canary)
			case "project":
				record.Project = []byte(canary)
			case "vault":
				record.Vault = []byte(canary)
			case "suggested":
				record.Suggested = []byte(canary)
			}
			artifact, err := BuildConflict(record)
			if err != nil {
				t.Fatalf("safe error expected, got %v", err)
			}
			if artifact.Record != nil || artifact.Repair == nil || artifact.Repair.IssueCode != syncdoc.IssueSensitive {
				t.Fatalf("artifact=%+v", artifact)
			}
			wantSide := SideProject
			if field == "vault" {
				wantSide = SideVault
			}
			if artifact.Repair.Side != wantSide {
				t.Fatalf("repair side=%q field=%q", artifact.Repair.Side, field)
			}
			visible := string(artifact.Notes.Project.Content) + string(artifact.Notes.Vault.Content) + artifact.Notes.Project.RelativePath + fmt.Sprintf("%+v", *artifact.Repair)
			if strings.Contains(visible, canary) || strings.Contains(visible, "REDACTED") {
				t.Fatalf("repair persisted candidate-derived text: %s", visible)
			}
			if !bytes.Equal(artifact.Notes.Project.Content, artifact.Notes.Vault.Content) || artifact.Notes.Project.RelativePath != artifact.Notes.Vault.RelativePath {
				t.Fatal("repair note was not mirrored identically")
			}
		})
	}
}

func TestBuildRepairNoteNeverPersistsRawPathOrSourceBytes(t *testing.T) {
	t.Parallel()

	canary := "MALFORMED-CONTENT-CANARY"
	absolute := "/Users/private/secret-project/decisions/bad.md"
	artifact, err := BuildRepair(RepairInput{
		CreatedAt: conflictTime, ProjectID: "project-1", EntityID: "", Side: SideVault,
		IssueCode: syncdoc.IssueMalformed, SourcePath: absolute, Source: []byte(canary),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := sha256.Sum256([]byte(absolute))
	wantID := fmt.Sprintf("repair-20260822t170203z-%x", wantSuffix[:6])
	if artifact.Repair == nil || artifact.Record != nil || artifact.Repair.ID != wantID || artifact.Repair.SourceHash != syncdoc.ContentHash([]byte(canary)) {
		t.Fatalf("artifact=%+v", artifact)
	}
	visible := artifact.Notes.Project.RelativePath + string(artifact.Notes.Project.Content) + fmt.Sprintf("%+v", *artifact.Repair)
	if strings.Contains(visible, canary) || strings.Contains(visible, absolute) || strings.Contains(visible, "/Users/") {
		t.Fatalf("repair leaked source material: %s", visible)
	}
	if !json.Valid(artifact.Notes.Project.Content) {
		t.Fatalf("repair is not JSON: %s", artifact.Notes.Project.Content)
	}
	for _, want := range []string{"malformed", "vault", syncdoc.ContentHash([]byte(canary))} {
		if !strings.Contains(string(artifact.Notes.Project.Content), want) {
			t.Fatalf("repair note missing %q:\n%s", want, artifact.Notes.Project.Content)
		}
	}
}

func TestRenderConflictRejectsSensitiveRecordWithoutEcho(t *testing.T) {
	t.Parallel()

	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Project: []byte("safe"), CreatedAt: conflictTime,
	})
	if err != nil || artifact.Record == nil {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}
	record := *artifact.Record
	canary := "password=secret-value-canary"
	record.Project = []byte(canary)
	record.ProjectHash = syncdoc.ContentHash(record.Project)
	digest := sha256.Sum256([]byte(record.BaseHash + "|" + record.ProjectHash + "|" + record.VaultHash))
	record.ID = fmt.Sprintf("conflict-%s-%s-%x", record.CreatedAt.Format("20060102T150405Z"), record.EntityID, digest[:6])
	_, err = RenderConflict(record)
	if !errors.Is(err, ErrSensitiveContent) || strings.Contains(err.Error(), canary) {
		t.Fatalf("err=%v", err)
	}
}

func TestSelectResolutionRendersLiveCandidateAndDoesNotTrustClaimedHash(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	projectDocument := editFrontmatterUnit(t, base, "title", "Project title\n")
	vaultDocument := editFrontmatterUnit(t, base, "tags", "[vault]\n")
	record := conflictRecordForDocuments(t, relative, &base, &projectDocument, &vaultDocument)

	liveProject := candidate(relative, projectDocument)
	liveProject.Hash = "not-the-live-hash"
	selected, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: AcceptProject}, liveProject, candidate(relative, vaultDocument), nil)
	if err != nil {
		t.Fatal(err)
	}
	selectedUnits := selected.Units()
	if string(selectedUnits[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "title"}].Value) != "Project title\n" || string(selectedUnits[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}].Value) != "4\n" {
		t.Fatalf("selected units=%+v", selectedUnits)
	}

	drifted := editFrontmatterUnit(t, projectDocument, "title", "Live drift\n")
	liveProject = candidate(relative, drifted)
	liveProject.Hash = record.ProjectHash
	if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: AcceptProject}, liveProject, candidate(relative, vaultDocument), nil); !errors.Is(err, ErrStaleConflict) {
		t.Fatalf("stale err=%v", err)
	}
}

func TestSelectResolutionValidatesEmbeddedIdentityAndReservedChanges(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	for _, tc := range []struct {
		name, field, value string
		want               error
	}{
		{name: "entity-id", field: "id", value: "decision-other\n", want: syncdoc.ErrReservedField},
		{name: "project-id", field: "project_id", value: "project-other\n", want: syncdoc.ErrReservedField},
		{name: "entity-type", field: "entity_type", value: "open_loop\n", want: syncdoc.ErrReservedField},
		{name: "sync-status", field: "sync_status", value: "conflicted\n", want: syncdoc.ErrReservedField},
		{name: "revision", field: "revision", value: "99\n", want: syncdoc.ErrProtectedProvenance},
		{name: "source-sessions", field: "source_sessions", value: "[session-other]\n", want: syncdoc.ErrProtectedProvenance},
		{name: "evidence-source-hash", field: "evidence", value: "- evidence_id: evidence-1\n  session_id: session-1\n  jsonl_line: 7\n  source_hash: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n  summary: changed\n", want: syncdoc.ErrProtectedProvenance},
		{name: "supersedes", field: "supersedes", value: "[decision-other]\n", want: syncdoc.ErrProtectedProvenance},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edited := editFrontmatterUnit(t, base, tc.field, tc.value)
			record := conflictRecordForDocuments(t, relative, &base, &edited, &base)
			_, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: AcceptProject}, candidate(relative, edited), candidate(relative, base), nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestSelectResolutionManualFailsClosedForNilPathIdentityDomainAndSecrets(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	projectDocument := editFrontmatterUnit(t, base, "title", "Project title\n")
	vaultDocument := editFrontmatterUnit(t, base, "tags", "[vault]\n")
	record := conflictRecordForDocuments(t, relative, &base, &projectDocument, &vaultDocument)
	liveProject, liveVault := candidate(relative, projectDocument), candidate(relative, vaultDocument)

	if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "manual.md"}, liveProject, liveVault, nil); !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("nil manual err=%v", err)
	}
	manual := projectDocument
	for _, invalid := range []string{"", "  ", "bad\x00path"} {
		if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: invalid}, liveProject, liveVault, &manual); !errors.Is(err, ErrInvalidResolution) {
			t.Fatalf("manual path %q err=%v", invalid, err)
		}
	}

	wrongIdentity := editFrontmatterUnit(t, projectDocument, "project_id", "project-other\n")
	if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "manual.md"}, liveProject, liveVault, &wrongIdentity); !errors.Is(err, syncdoc.ErrReservedField) {
		t.Fatalf("identity err=%v", err)
	}
	invalidDomain := editFrontmatterUnit(t, projectDocument, "status", "impossible\n")
	if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "manual.md"}, liveProject, liveVault, &invalidDomain); !errors.Is(err, syncdoc.ErrInvalidDocument) {
		t.Fatalf("domain err=%v", err)
	}
	secret := editSectionUnit(t, projectDocument, sectionKeyBySuffix(t, projectDocument, " / Alternatives#1").Name, "api_key=sk-abcdefghijklmnopqrstuvwxyz012345\n")
	_, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "manual.md"}, liveProject, liveVault, &secret)
	if !errors.Is(err, ErrSensitiveContent) || strings.Contains(err.Error(), "abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("secret err=%v", err)
	}
}

func TestSelectResolutionManualReturnsValidatedDocument(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	project := editFrontmatterUnit(t, base, "title", "Project title\n")
	vault := editFrontmatterUnit(t, base, "tags", "[vault]\n")
	manual := editFrontmatterUnit(t, base, "title", "Manual title\n")
	record := conflictRecordForDocuments(t, relative, &base, &project, &vault)
	selected, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "/already/safely/opened/manual.md"}, candidate(relative, project), candidate(relative, vault), &manual)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := selected.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rendered, []byte("title: Manual title")) || !bytes.Contains(rendered, []byte("sync_status: synced")) {
		t.Fatalf("selected=%s", rendered)
	}
}

func TestSelectResolutionFinalizesExistingHumanMergeExactlyOnce(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	project := editFrontmatterUnit(t, base, "title", "Project title\n")
	project = editFrontmatterUnit(t, project, "plugin_key", "project-unknown\n")
	vault := editFrontmatterUnit(t, base, "tags", "[vault]\n")
	manual := editFrontmatterUnit(t, base, "title", "Manual title\n")
	manual = editFrontmatterUnit(t, manual, "plugin_key", "manual-unknown\n")
	record := conflictRecordForDocuments(t, relative, &base, &project, &vault)

	for _, tc := range []struct {
		name, title, unknown string
		resolution           Resolution
		manual               *syncdoc.Document
	}{
		{name: "accept-project", title: "Project title\n", unknown: "project-unknown\n", resolution: Resolution{ConflictID: record.ID, Action: AcceptProject}},
		{name: "manual", title: "Manual title\n", unknown: "manual-unknown\n", resolution: Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "/already/safely/opened/manual.md"}, manual: &manual},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, err := SelectResolution(record, tc.resolution, candidate(relative, project), candidate(relative, vault), tc.manual)
			if err != nil {
				t.Fatal(err)
			}
			units := selected.Units()
			assertUnitValue := func(name, want string) {
				t.Helper()
				if got := string(units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: name}].Value); got != want {
					t.Fatalf("%s=%q want=%q", name, got, want)
				}
			}
			assertUnitValue("revision", "4\n")
			assertUnitValue("sync_status", "synced\n")
			assertUnitValue("title", tc.title)
			assertUnitValue("plugin_key", tc.unknown)
			baseUnits := base.Units()
			for _, name := range []string{"source_sessions", "evidence", "supersedes"} {
				key := syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: name}
				if !reflect.DeepEqual(units[key], baseUnits[key]) {
					t.Fatalf("protected %s changed: got=%+v want=%+v", name, units[key], baseUnits[key])
				}
			}
		})
	}
}

func TestSelectResolutionExistingNoSemanticChangeDoesNotIncrementRevision(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	vault := editFrontmatterUnit(t, base, "tags", "[vault]\n")
	record := conflictRecordForDocuments(t, relative, &base, &base, &vault)

	for _, tc := range []struct {
		name       string
		resolution Resolution
		manual     *syncdoc.Document
	}{
		{name: "accept-project", resolution: Resolution{ConflictID: record.ID, Action: AcceptProject}},
		{name: "manual", resolution: Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "/already/safely/opened/manual.md"}, manual: &base},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, err := SelectResolution(record, tc.resolution, candidate(relative, base), candidate(relative, vault), tc.manual)
			if err != nil {
				t.Fatal(err)
			}
			units := selected.Units()
			if got := string(units[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}].Value); got != "3\n" {
				t.Fatalf("revision=%q want=3", got)
			}
		})
	}
}

func TestV2SelectResolutionUsesCompactFinalizationAndConverges(t *testing.T) {
	base := v2HistoryWithTwoEvents(t)
	project := replaceV2Source(t, "项目历史.md", base, "信任链与 dry-run 边界修复", "project 标题")
	vault := replaceV2Source(t, "项目历史.md", base, "信任链与 dry-run 边界修复", "vault 标题")
	manual := replaceV2Source(t, "项目历史.md", base, "信任链与 dry-run 边界修复", "manual 标题")
	record := v2ConflictRecordForDocuments(t, &base, &project, &vault)

	for _, tc := range []struct {
		name, title string
		resolution  Resolution
		manual      *syncdoc.Document
	}{
		{name: "project", title: "project 标题", resolution: Resolution{ConflictID: record.ID, Action: AcceptProject}},
		{name: "vault", title: "vault 标题", resolution: Resolution{ConflictID: record.ID, Action: AcceptObsidian}},
		{name: "manual", title: "manual 标题", resolution: Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "/validated/manual.md"}, manual: &manual},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, err := SelectResolution(record, tc.resolution, candidate("项目历史.md", project), candidate("项目历史.md", vault), tc.manual)
			if err != nil {
				t.Fatal(err)
			}
			rendered := renderDocument(t, selected)
			if !bytes.Contains(rendered, []byte(tc.title)) {
				t.Fatalf("selected wrong revision:\n%s", rendered)
			}
			assertV2VisibleFrontmatter(t, rendered, 2)
			for _, forbidden := range []string{"sync_status:", "sync_hash:", "base_hash:", "project_hash:", "vault_hash:"} {
				if bytes.Contains(rendered, []byte(forbidden)) {
					t.Fatalf("compact selection leaked %q:\n%s", forbidden, rendered)
				}
			}
			followup := Merge(v2MergeInput("project-history", "项目历史.md", selected, selected, selected))
			if followup.Kind != MergeNoop {
				t.Fatalf("selected resolution did not converge: %+v", followup)
			}
		})
	}
}

func TestV2SelectResolutionAllowsDistinctStableIDsWithSameVisibleTitle(t *testing.T) {
	sameTitlesSource := bytes.Replace(renderDocument(t, v2ReviewWithTwoDecisions(t)), []byte("兼容性决策"), []byte("Skill + 本地 CLI"), 1)
	base, err := syncdoc.Parse("项目回顾.md", sameTitlesSource)
	if err != nil {
		t.Fatalf("authoritative parser rejected distinct IDs with same title: %v", err)
	}
	project := replaceV2Source(t, "项目回顾.md", base, "原始会话不上传。", "project reason")
	vault := replaceV2Source(t, "项目回顾.md", base, "原始会话不上传。", "vault reason")
	manual := replaceV2Source(t, "项目回顾.md", base, "原始会话不上传。", "manual reason")
	record := v2ReviewConflictRecordForDocuments(t, &base, &project, &vault)
	for _, tc := range []struct {
		name, reason string
		resolution   Resolution
		manual       *syncdoc.Document
	}{
		{name: "project", reason: "project reason", resolution: Resolution{ConflictID: record.ID, Action: AcceptProject}},
		{name: "vault", reason: "vault reason", resolution: Resolution{ConflictID: record.ID, Action: AcceptObsidian}},
		{name: "manual", reason: "manual reason", resolution: Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "/validated/manual.md"}, manual: &manual},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, err := SelectResolution(record, tc.resolution, candidate("项目回顾.md", project), candidate("项目回顾.md", vault), tc.manual)
			if err != nil {
				t.Fatal(err)
			}
			rendered := renderDocument(t, selected)
			if bytes.Count(rendered, []byte("### Skill + 本地 CLI\n")) != 2 || !bytes.Contains(rendered, []byte(tc.reason)) {
				t.Fatalf("selected wrong stable unit:\n%s", rendered)
			}
			assertV2VisibleFrontmatter(t, rendered, 2)
			followup := Merge(v2MergeInput("project-overview", "项目回顾.md", selected, selected, selected))
			if followup.Kind != MergeNoop {
				t.Fatalf("selection did not converge: %+v", followup)
			}
		})
	}

	t.Run("duplicate-stable-id-still-rejected", func(t *testing.T) {
		duplicate := bytes.Replace(sameTitlesSource, []byte("decision-compatibility"), []byte("decision-local-cli"), 1)
		if _, err := syncdoc.Parse("项目回顾.md", duplicate); err == nil {
			t.Fatal("duplicate stable marker ID was accepted")
		}
	})
}

func TestSelectResolutionRejectsMalformedDuplicateAndMissingCandidates(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	baseBytes, _ := base.Render()
	project := editFrontmatterUnit(t, base, "title", "Project title\n")
	projectBytes, _ := project.Render()
	vaultBytes := bytes.Clone(baseBytes)

	for _, tc := range []struct {
		name    string
		project []byte
	}{
		{name: "malformed", project: []byte("MALFORMED-CANARY")},
		{name: "duplicate-yaml", project: bytes.Replace(projectBytes, []byte("title: Project title\n"), []byte("title: Project title\ntitle: duplicate\n"), 1)},
		{name: "duplicate-section", project: append(bytes.Clone(projectBytes), []byte("\n## Alternatives\n\nduplicate\n")...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifact, err := BuildConflict(ConflictRecord{Version: 1, EntityID: "decision-sync", ProjectID: "project-1111111111111111", Kind: ConflictUnits, RelativePath: relative, BasePath: relative, ProjectPath: relative, VaultPath: relative, Base: baseBytes, Project: tc.project, Vault: vaultBytes, CreatedAt: conflictTime})
			if err != nil {
				t.Fatal(err)
			}
			liveDocument, parseErr := syncdoc.Parse(relative, tc.project)
			live := Candidate{}
			if parseErr == nil {
				live = candidate(relative, liveDocument)
			}
			_, selectErr := SelectResolution(*artifact.Record, Resolution{ConflictID: artifact.Record.ID, Action: AcceptProject}, live, candidate(relative, base), nil)
			if !errors.Is(selectErr, ErrInvalidConflict) || strings.Contains(selectErr.Error(), "CANARY") {
				t.Fatalf("err=%v", selectErr)
			}
		})
	}

	record := conflictRecordForDocuments(t, relative, &base, &project, &base)
	if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: AcceptProject}, Candidate{}, candidate(relative, base), nil); !errors.Is(err, ErrStaleConflict) {
		t.Fatalf("missing candidate err=%v", err)
	}
	if _, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ResolutionAction("unknown")}, candidate(relative, project), candidate(relative, base), nil); !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("invalid action err=%v", err)
	}
	if _, err := SelectResolution(record, Resolution{ConflictID: "wrong", Action: AcceptProject}, candidate(relative, project), candidate(relative, base), nil); !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("wrong conflict id err=%v", err)
	}
}

func TestSelectResolutionSupportsEmptyBaseNewEntity(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	newEntity := documentAtRevision(t, base, relative, 1)
	record := conflictRecordForDocuments(t, relative, nil, &newEntity, &newEntity)
	selected, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: AcceptObsidian}, candidate(relative, newEntity), candidate(relative, newEntity), nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := selected.Identity()
	if err != nil || identity.ID != "decision-sync" {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
}

func TestSelectResolutionEmptyBaseManualRequiresCompleteEmbeddedIdentity(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	decision := documentAtRevision(t, base, relative, 1)
	openLoop := parseInlineDocument(t, relative, "---\nid: decision-sync\nentity_type: open_loop\nproject_id: project-1111111111111111\nrevision: 1\nsync_status: synced\ntitle: Loop\nstatus: open\ntags: []\nsource_sessions: []\nevidence: []\n---\n\n# Loop\n\n## Attempted paths\n")

	for _, tc := range []struct {
		name           string
		project, vault *syncdoc.Document
		liveProject    Candidate
		liveVault      Candidate
	}{
		{name: "both-present", project: &decision, vault: &decision, liveProject: candidate(relative, decision), liveVault: candidate(relative, decision)},
		{name: "project-only", project: &decision, liveProject: candidate(relative, decision)},
		{name: "vault-only", vault: &decision, liveVault: candidate(relative, decision)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := conflictRecordForDocuments(t, relative, nil, tc.project, tc.vault)
			_, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "/already/safely/opened/manual.md"}, tc.liveProject, tc.liveVault, &openLoop)
			if !errors.Is(err, syncdoc.ErrReservedField) {
				t.Fatalf("identity err=%v", err)
			}
		})
	}
}

func TestSelectResolutionEmptyBaseRejectsMismatchedEmbeddedIdentities(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	decision := documentAtRevision(t, base, relative, 1)
	openLoop := parseInlineDocument(t, relative, "---\nid: decision-sync\nentity_type: open_loop\nproject_id: project-1111111111111111\nrevision: 1\nsync_status: synced\ntitle: Loop\nstatus: open\ntags: []\nsource_sessions: []\nevidence: []\n---\n\n# Loop\n\n## Attempted paths\n")
	record := conflictRecordForDocuments(t, relative, nil, &decision, &openLoop)
	_, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "/already/safely/opened/manual.md"}, candidate(relative, decision), candidate(relative, openLoop), &decision)
	if !errors.Is(err, syncdoc.ErrReservedField) {
		t.Fatalf("identity err=%v", err)
	}
}

func TestSelectResolutionEmptyBaseManualAcceptsMatchingSingleSideIdentity(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	decision := documentAtRevision(t, base, relative, 1)
	record := conflictRecordForDocuments(t, relative, nil, &decision, nil)
	selected, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: ManualMerge, ManualFile: "/already/safely/opened/manual.md"}, candidate(relative, decision), Candidate{}, &decision)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := selected.Identity()
	if err != nil || identity != (syncdoc.Identity{ID: "decision-sync", EntityType: "decision", ProjectID: "project-1111111111111111"}) {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
	if revision := selected.Units()[syncdoc.UnitKey{Kind: syncdoc.UnitFrontmatter, Name: "revision"}]; string(revision.Value) != "1\n" {
		t.Fatalf("revision=%q want=1", revision.Value)
	}
}

func TestMarkConflictResolvedIsPureAndIdempotent(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	project := editFrontmatterUnit(t, base, "title", "Project title\n")
	record := conflictRecordForDocuments(t, relative, &base, &project, &base)
	projectBytes, _ := project.Render()
	resolvedAt := conflictTime.Add(time.Hour)

	resolved, err := MarkConflictResolved(record, AcceptProject, syncdoc.ContentHash(projectBytes), resolvedAt)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ResolutionStatus != ResolutionResolved || resolved.ResolutionAction != AcceptProject || resolved.ResolvedHash != syncdoc.ContentHash(projectBytes) || !resolved.ResolvedAt.Equal(resolvedAt) || resolved.ResolvedAt.Location() != time.UTC {
		t.Fatalf("resolved=%+v", resolved)
	}
	if record.ResolutionStatus != ResolutionOpen || record.ResolutionAction != "" || !record.ResolvedAt.IsZero() {
		t.Fatalf("input record was mutated: %+v", record)
	}
	note, err := RenderConflict(resolved)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"resolution_status": "resolved"`, `"resolution_action": "accept_project"`, resolved.ResolvedHash, resolvedAt.UTC().Format(time.RFC3339Nano)} {
		if !strings.Contains(string(note), want) {
			t.Fatalf("resolved note missing %q:\n%s", want, note)
		}
	}

	idempotent, err := MarkConflictResolved(resolved, AcceptProject, resolved.ResolvedHash, resolvedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !idempotent.ResolvedAt.Equal(resolved.ResolvedAt) {
		t.Fatalf("idempotent resolution rewrote timestamp: %s -> %s", resolved.ResolvedAt, idempotent.ResolvedAt)
	}
	if _, err := MarkConflictResolved(resolved, AcceptObsidian, resolved.ResolvedHash, resolvedAt); !errors.Is(err, ErrConflictResolved) {
		t.Fatalf("different action err=%v", err)
	}
	if _, err := MarkConflictResolved(resolved, AcceptProject, strings.Repeat("f", 64), resolvedAt); !errors.Is(err, ErrConflictResolved) {
		t.Fatalf("different hash err=%v", err)
	}
}

func TestMarkConflictResolvedRedactsEveryCandidateBeforeTrustingHashesOrID(t *testing.T) {
	t.Parallel()

	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits,
		RelativePath: "decisions/decision-1.md", Base: []byte("safe-base"), Project: []byte("safe-project"), Vault: []byte("safe-vault"), Suggested: []byte("safe-suggested"), CreatedAt: conflictTime,
	})
	if err != nil || artifact.Record == nil {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}
	for _, field := range []string{"base", "project", "vault", "suggested"} {
		field := field
		t.Run(field, func(t *testing.T) {
			canary := "api_key=sk-abcdefghijklmnopqrstuvwxyz012345-" + field
			tampered := cloneConflictRecord(*artifact.Record)
			switch field {
			case "base":
				tampered.Base = []byte(canary)
				tampered.BaseHash = strings.Repeat("f", 64)
			case "project":
				tampered.Project = []byte(canary)
				tampered.ProjectHash = strings.Repeat("f", 64)
			case "vault":
				tampered.Vault = []byte(canary)
				tampered.VaultHash = strings.Repeat("f", 64)
			case "suggested":
				tampered.Suggested = []byte(canary)
			}
			tampered.ID = "tampered-id"
			resolved, markErr := MarkConflictResolved(tampered, AcceptProject, strings.Repeat("a", 64), conflictTime.Add(time.Hour))
			if !errors.Is(markErr, ErrSensitiveContent) || strings.Contains(markErr.Error(), canary) {
				t.Fatalf("err=%v", markErr)
			}
			if strings.Contains(fmt.Sprintf("%+v", resolved), canary) {
				t.Fatal("failed resolution returned secret-bearing record")
			}
		})
	}
}

func TestSelectResolutionAlreadyResolvedAllowsOnlySameActionAndHash(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	project := editFrontmatterUnit(t, base, "title", "Project title\n")
	vault := editFrontmatterUnit(t, base, "tags", "[vault]\n")
	record := conflictRecordForDocuments(t, relative, &base, &project, &vault)
	finalProject, err := project.FinalizeHumanMerge(base, true)
	if err != nil {
		t.Fatal(err)
	}
	finalProject, err = finalProject.WithSyncStatus("synced")
	if err != nil {
		t.Fatal(err)
	}
	projectBytes, _ := finalProject.Render()
	resolved, err := MarkConflictResolved(record, AcceptProject, syncdoc.ContentHash(projectBytes), conflictTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SelectResolution(resolved, Resolution{ConflictID: resolved.ID, Action: AcceptProject}, candidate(relative, project), candidate(relative, vault), nil); err != nil {
		t.Fatalf("same resolution was not idempotent: %v", err)
	}
	if _, err := SelectResolution(resolved, Resolution{ConflictID: resolved.ID, Action: AcceptObsidian}, candidate(relative, project), candidate(relative, vault), nil); !errors.Is(err, ErrConflictResolved) {
		t.Fatalf("different resolution err=%v", err)
	}
}

func TestBuildConflictConvertsIsolationKindsToContentFreeRepair(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		kind ConflictKind
		want syncdoc.IssueKind
	}{
		{kind: ConflictMalformed, want: syncdoc.IssueMalformed},
		{kind: ConflictCollision, want: syncdoc.IssuePathCollision},
		{kind: ConflictReserved, want: syncdoc.IssueReservedEdit},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			canary := "ISOLATED-BYTES-CANARY"
			artifact, err := BuildConflict(ConflictRecord{
				Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: tc.kind,
				RelativePath: "decisions/decision-1.md", Project: []byte(canary), CreatedAt: conflictTime,
			})
			if err != nil {
				t.Fatal(err)
			}
			if artifact.Record != nil || artifact.Repair == nil || artifact.Repair.IssueCode != tc.want {
				t.Fatalf("artifact=%+v", artifact)
			}
			if strings.Contains(string(artifact.Notes.Project.Content), canary) || strings.Contains(fmt.Sprintf("%+v", *artifact.Repair), canary) {
				t.Fatal("repair leaked isolated bytes")
			}
		})
	}
}

func TestConflictRecordTamperingAndUnsafePathsFailClosed(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	project := editFrontmatterUnit(t, base, "title", "Project title\n")
	record := conflictRecordForDocuments(t, relative, &base, &project, &base)

	mutations := []struct {
		name   string
		mutate func(*ConflictRecord)
	}{
		{name: "id", mutate: func(value *ConflictRecord) { value.ID = "conflict-tampered" }},
		{name: "hash", mutate: func(value *ConflictRecord) { value.ProjectHash = strings.Repeat("f", 64) }},
		{name: "bytes", mutate: func(value *ConflictRecord) { value.Project = append(value.Project, 'x') }},
		{name: "non-utc", mutate: func(value *ConflictRecord) { value.CreatedAt = value.CreatedAt.In(time.FixedZone("local", 3600)) }},
		{name: "absolute-project-path", mutate: func(value *ConflictRecord) { value.ProjectPath = "/private/project.md" }},
		{name: "reserved-path", mutate: func(value *ConflictRecord) { value.VaultPath = "sync-conflicts/entity.md" }},
		{name: "invalid-resolution-state", mutate: func(value *ConflictRecord) { value.ResolutionAction = AcceptProject }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			tampered := cloneConflictRecord(record)
			tc.mutate(&tampered)
			if _, err := RenderConflict(tampered); !errors.Is(err, ErrInvalidConflict) {
				t.Fatalf("render err=%v", err)
			}
			_, err := SelectResolution(tampered, Resolution{ConflictID: tampered.ID, Action: AcceptProject}, candidate(relative, project), candidate(relative, base), nil)
			if !errors.Is(err, ErrInvalidConflict) {
				t.Fatalf("select err=%v", err)
			}
		})
	}
}

func TestBuildConflictRejectsPrepopulatedTamperFields(t *testing.T) {
	t.Parallel()

	for _, mutate := range []func(*ConflictRecord){
		func(record *ConflictRecord) { record.ID = "caller-id" },
		func(record *ConflictRecord) { record.BaseHash = strings.Repeat("f", 64) },
		func(record *ConflictRecord) { record.ProjectPath = "/absolute/project.md" },
		func(record *ConflictRecord) { record.ResolutionAction = AcceptProject },
		func(record *ConflictRecord) { record.ResolvedHash = strings.Repeat("f", 64) },
		func(record *ConflictRecord) { record.ResolvedAt = conflictTime },
	} {
		record := ConflictRecord{Version: 1, EntityID: "decision-1", ProjectID: "project-1", Kind: ConflictUnits, RelativePath: "decisions/decision-1.md", Project: []byte("PROJECT"), CreatedAt: conflictTime}
		mutate(&record)
		if _, err := BuildConflict(record); !errors.Is(err, ErrInvalidConflict) {
			t.Fatalf("err=%v record=%+v", err, record)
		}
	}
}

func TestSelectResolutionRejectsSecretEmbeddedRecordWithoutEcho(t *testing.T) {
	t.Parallel()

	const relative = "decisions/decision-sync.md"
	base := fixtureDocument(t, "base-decision.md", relative)
	project := editFrontmatterUnit(t, base, "title", "Project title\n")
	record := conflictRecordForDocuments(t, relative, &base, &project, &base)
	canary := "api_key=sk-abcdefghijklmnopqrstuvwxyz012345"
	record.Suggested = []byte(canary)
	_, err := SelectResolution(record, Resolution{ConflictID: record.ID, Action: AcceptProject}, candidate(relative, project), candidate(relative, base), nil)
	if !errors.Is(err, ErrSensitiveContent) || strings.Contains(err.Error(), canary) {
		t.Fatalf("err=%v", err)
	}
}

func conflictRecordForDocuments(t *testing.T, relative string, base, project, vault *syncdoc.Document) ConflictRecord {
	t.Helper()
	render := func(document *syncdoc.Document) []byte {
		if document == nil {
			return nil
		}
		content, err := document.Render()
		if err != nil {
			t.Fatal(err)
		}
		return content
	}
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "decision-sync", ProjectID: "project-1111111111111111", Kind: ConflictUnits,
		RelativePath: relative, BasePath: relative, ProjectPath: relative, VaultPath: relative,
		Base: render(base), Project: render(project), Vault: render(vault), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Record == nil {
		t.Fatalf("unexpected repair: %+v", artifact.Repair)
	}
	return *artifact.Record
}

func v2ConflictRecordForDocuments(t *testing.T, base, project, vault *syncdoc.Document) ConflictRecord {
	t.Helper()
	render := func(document *syncdoc.Document) []byte {
		if document == nil {
			return nil
		}
		return renderDocument(t, *document)
	}
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "project-history", ProjectID: "project-0123456789abcdef", Kind: ConflictUnits,
		RelativePath: "项目历史.md", BasePath: "项目历史.md", ProjectPath: "项目历史.md", VaultPath: "项目历史.md",
		Base: render(base), Project: render(project), Vault: render(vault), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Record == nil {
		t.Fatalf("unexpected repair: %+v", artifact.Repair)
	}
	return *artifact.Record
}

func v2ReviewConflictRecordForDocuments(t *testing.T, base, project, vault *syncdoc.Document) ConflictRecord {
	t.Helper()
	render := func(document *syncdoc.Document) []byte {
		if document == nil {
			return nil
		}
		return renderDocument(t, *document)
	}
	artifact, err := BuildConflict(ConflictRecord{
		Version: 1, EntityID: "project-overview", ProjectID: "project-0123456789abcdef", Kind: ConflictUnits,
		RelativePath: "项目回顾.md", BasePath: "项目回顾.md", ProjectPath: "项目回顾.md", VaultPath: "项目回顾.md",
		Base: render(base), Project: render(project), Vault: render(vault), CreatedAt: conflictTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Record == nil {
		t.Fatalf("unexpected repair: %+v", artifact.Repair)
	}
	return *artifact.Record
}
