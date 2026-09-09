# Git Status Directory Records Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. One bounded correction after the active publication task closes; never edit concurrently with another implementation worker.

**Goal:** Count valid untracked directory records without misreporting malformed Git output or weakening path validation.

**Architecture:** Keep the existing approved porcelain-v1 NUL-delimited Git command and one-record counting. Permit the single terminal directory marker only for directory-capable `??` and `!!` records, then apply the existing strict relative-path validator to the remaining name. Rename/copy source and destination paths retain their existing stricter rules.

**Tech Stack:** Existing Go projectprobe parser and tests. No dependencies or new executable commands in production.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` ordinary deterministic scanning and honest coverage; the current projectprobe approved Git argument contract remains binding. The user requests correction of all worthwhile reproducible bugs, not new probe features.

## Global Constraints

- Ordinary scans start zero Agent processes. No network, daily-Vault mutation or release in these task commits.
- Keep `git status --porcelain=v1 -z` unchanged. Do not add `--untracked-files=all`, ignore actual dirty changes, or relax the scan ProjectViewDigest equality gate.
- A valid directory record counts once, just like the existing command emits it. Raw private paths must not be added to diagnostics or persisted state.
- Retain output/path bounds, UTF-8 validation, NUL framing, traversal/absolute-path rejection and rename/copy pair handling.

### Task 1: Accept valid directory markers at the porcelain record boundary

**Files:** Modify `internal/projectprobe/git.go`; create `internal/projectprobe/git_status_test.go`. Reuse `probe_test.go` fixture helpers without moving or rewriting that file.

**Interfaces:** Existing `parseStatus(output []byte) (int, bool)` and `validStatusPath(value []byte) bool` stay private and signature-compatible. A small record-aware validation helper is allowed; do not weaken `validStatusPath` globally because rename/copy parsing also uses it.

- [ ] Add owned table-test RED with file positive controls and directory failures:

```go
func TestParseStatusDirectoryRecords(t *testing.T) {
    cases := []struct { name, raw string; count int; malformed bool }{
        {"file", "?? docs.txt\x00", 1, false},
        {"directory", "?? docs/\x00", 1, false},
        {"unicode space directory", "?? 资料 空间/\x00", 1, false},
        {"ignored directory", "!! ignored/\x00", 1, false},
        {"tracked terminal slash", " M tracked/\x00", 0, true},
        {"root", "?? /\x00", 0, true},
        {"double slash", "?? docs//\x00", 0, true},
        {"traversal", "?? ../docs/\x00", 0, true},
        {"rename", "R  new.txt\x00old.txt\x00", 1, false},
        {"bad rename source", "R  new.txt\x00old/\x00", 0, true},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            count, malformed := parseStatus([]byte(tc.raw))
            if count != tc.count || malformed != tc.malformed {
                t.Fatalf("got (%d,%v), want (%d,%v)", count, malformed, tc.count, tc.malformed)
            }
        })
    }
}
```

- [ ] Add exact path-boundary cases (4096 accepted bytes including a directory marker, 4097 rejected), embedded control/invalid UTF-8, empty names, missing NUL, copy pairs and mixed valid/malformed records retaining only valid counts. Use `strings.Repeat` and `[]byte` construction in tests, not large literal fixtures.
- [ ] Add real temporary Git positive/negative integration using existing `newBinding`, `writeFile` and approved probe APIs: an untracked nested directory with two files yields one dirty record and no `git_status_malformed`; adding an independent untracked file raises count by one; public probe contains counts, not those raw names. No global Git configuration, hooks or host project mutation. Existing trusted Git fixture setup may be reused.
- [ ] Run `go test ./internal/projectprobe -run 'ParseStatusDirectory|UntrackedDirectory' -count=1` and record actual directory assertion failures before implementation. Controller external `TestControllerGitStatusUntrackedDirectory` independently fails with count0/malformedtrue (0.493s); owned tests must not depend on that external file.
- [ ] Implement the minimal record-aware normalization. Check the original byte bound before stripping one trailing slash. Only `??`/`!!` may take that branch; pass the trimmed name to unchanged `validStatusPath`, so empty, traversal, doubled slash, absolute and invalid names still reject. Existing ordinary-file and rename/copy branches are unchanged.

```go
// At the record boundary, after status code/framing validation:
name := record[3:]
directoryStatus := (record[0] == '?' && record[1] == '?') || (record[0] == '!' && record[1] == '!')
if directoryStatus && len(name) > 0 && name[len(name)-1] == '/' {
    // Check the original maximum length before this normalization.
    name = name[:len(name)-1]
}
// Then validate name with the existing strict path function.
```

- [ ] Run focused GREEN, complete `go test ./internal/projectprobe ./internal/scan ./internal/contextupdate -count=1`, scoped `go vet`, and `git diff --check`. The approved process/import capability surface should stay unchanged; if implementation adds one, remove the unnecessary capability rather than updating the baseline. Whole-product full Go/zero-token remains a subsequent branch gate.
- [ ] Commit exact owned paths and report the RED/GREEN commands/results. Independent task review must approve parser constraints and real Git accounting before closure. Controller repeats the original directory overlay and real scan lifecycle; no release claim follows from this bounded fix.

## Preflight

| Shared surface | Producer / consumer | Finding |
|---|---|---|
| Status record / path validator | directory marker / canonical relative path | normalize at record boundary only; rename/copy remain strict |
| Status parser / probe | count plus malformed flag / bounded project state | no raw-name persistence and no enumeration change |
| Probe / scan no-op | real state digest / exact identity gate | repair parsing, never conceal dirty changes |
| Single task tests / implementation | legal terminal marker plus hostile path controls | original full path bound must precede stripping |

Ruling: Treat a terminal slash on `??` and `!!` as Git's directory marker, not part of a canonical file path — preserve the existing command/count contract — cost if wrong is a local parser correction; no source or project mutation is authorized.
