package project

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/neomei/SessionReviewer/internal/reviewv2"
)

func TestInitCreatesReviewV2(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	result, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data,
		Now:    func() time.Time { return time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC) },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectID != "project-2a2a2a2a2a2a2a2a" {
		t.Fatalf("project ID=%q", result.ProjectID)
	}
	for _, relative := range []string{
		reviewv2.ReviewRelativePath,
		reviewv2.HistoryRelativePath,
		reviewv2.MachineLedgerRelativePath,
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("missing %s: %v", relative, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "session-review", "project-overview.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy overview exists after v2 init: %v", err)
	}
	accepted, err := reviewv2.Load(root)
	if err != nil || accepted.State.Review.ProjectID != result.ProjectID || accepted.State.Review.Revision != 1 {
		t.Fatalf("accepted=%+v err=%v", accepted.State.Review, err)
	}
}

func TestInitializeNormalizesTrailingSpaceProjectDirectoryName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Win32 cannot represent a directory leaf with a trailing space")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "AgentWiki ")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	vault, data := t.TempDir(), t.TempDir()
	result, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data,
		Now:    func() time.Time { return time.Date(2026, 8, 30, 8, 31, 11, 517436000, time.UTC) },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x51}, 16)),
	})
	if err != nil {
		t.Fatalf("initialize trailing-space project directory: %v", err)
	}
	accepted, err := reviewv2.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.State.Review.Name != "AgentWiki" {
		t.Fatalf("review name=%q want %q", accepted.State.Review.Name, "AgentWiki")
	}
	reviewBody, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(reviewv2.ReviewRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(reviewBody, []byte("# AgentWiki\n")) {
		t.Fatalf("review heading missing canonical name:\n%s", reviewBody)
	}
	if bytes.Contains(reviewBody, []byte("# AgentWiki \n")) {
		t.Fatal("review heading retained trailing space")
	}
	cfg, err := config.Load(result.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("projects=%+v, want one mapping", cfg.Projects)
	}
	wantVaultPath := "Projects/AgentWiki--51515151/Session Review"
	if cfg.Projects[0].VaultReviewPath != wantVaultPath {
		t.Fatalf("vault review path=%q want %q", cfg.Projects[0].VaultReviewPath, wantVaultPath)
	}
}

func TestInitializeReassociatesCompleteReviewV2WithoutChangingIdentity(t *testing.T) {
	root, vault, firstData, secondData := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	first, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: firstData,
		Now:    func() time.Time { return time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC) },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := reviewv2.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: secondData,
		Random: errorReader{},
	})
	if err != nil || second.ProjectID != first.ProjectID {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	after, err := reviewv2.Load(root)
	if err != nil || !reflect.DeepEqual(before.State, after.State) {
		t.Fatalf("review changed during reassociation err=%v", err)
	}
	assertSingleMapping(t, secondData, first.ProjectID)
}

func TestInitializeRejectsMixedOrIncompleteReviewVersions(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, root string)
	}{
		{name: "mixed legacy and v2", mutate: func(t *testing.T, root string) {
			writeTestOverview(t, root, "project-2a2a2a2a2a2a2a2a")
		}},
		{name: "incomplete v2", mutate: func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(reviewv2.HistoryRelativePath))); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
			if _, err := Initialize(InitOptions{
				ProjectRoot: root, VaultRoot: vault, DataDir: data,
				Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
			}); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, root)
			_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, Random: errorReader{}})
			if !errors.Is(err, ErrConflictingInitializationIdentity) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestInitializeRecoversInterruptionAfterEachReviewV2File(t *testing.T) {
	for stopAfter := 1; stopAfter <= 3; stopAfter++ {
		t.Run(fmt.Sprintf("file-%d", stopAfter), func(t *testing.T) {
			root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
			sentinel := errors.New("interrupt review v2 initialization")
			written := 0
			_, err := Initialize(InitOptions{
				ProjectRoot: root, VaultRoot: vault, DataDir: data,
				Now:    func() time.Time { return time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC) },
				Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
				afterReviewV2File: func(string) error {
					written++
					if written == stopAfter {
						return sentinel
					}
					return nil
				},
			})
			if !errors.Is(err, sentinel) {
				t.Fatalf("err=%v", err)
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(initialReviewV2JournalPath))); err != nil {
				t.Fatalf("recovery journal missing: %v", err)
			}
			journalBody, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(initialReviewV2JournalPath)))
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"在这里记录项目目标", "准备第一次 session review", "review_sha256", "history_sha256", "content"} {
				if bytes.Contains(journalBody, []byte(forbidden)) {
					t.Fatalf("initialization journal contains review content/bytes field %q: %s", forbidden, journalBody)
				}
			}
			result, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, Random: errorReader{}})
			if err != nil || result.ProjectID != "project-2a2a2a2a2a2a2a2a" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if _, err := reviewv2.Load(root); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(initialReviewV2JournalPath))); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("recovery journal remains: %v", err)
			}
		})
	}
}

func TestInitializePartialReviewV2NeverOverwritesForeignBytes(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	sentinel := errors.New("interrupt review v2 initialization")
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
		afterReviewV2File: func(string) error {
			return sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	partial := filepath.Join(root, filepath.FromSlash(reviewv2.HistoryRelativePath))
	foreign := []byte("foreign bytes that do not belong to the init journal\n")
	if err := os.WriteFile(partial, foreign, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, Random: errorReader{}})
	if !errors.Is(err, ErrConflictingInitializationIdentity) {
		t.Fatalf("err=%v", err)
	}
	got, err := os.ReadFile(partial)
	if err != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("foreign bytes changed: %q err=%v", got, err)
	}
}

func TestInitializeConcurrentPartialReviewV2RecoveryConverges(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	sentinel := errors.New("interrupt review v2 initialization")
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
		afterReviewV2File: func(string) error {
			return sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	type outcome struct {
		result InitResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			result, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, Random: errorReader{}})
			outcomes <- outcome{result: result, err: err}
		}()
	}
	for range 2 {
		got := <-outcomes
		if got.err != nil || got.result.ProjectID != "project-2a2a2a2a2a2a2a2a" {
			t.Fatalf("outcome=%+v", got)
		}
	}
	if _, err := reviewv2.Load(root); err != nil {
		t.Fatal(err)
	}
	assertSingleMapping(t, data, "project-2a2a2a2a2a2a2a2a")
}

func TestInitialReviewV2RecoveryUsesPlatformPermissionSemantics(t *testing.T) {
	if !initialReviewV2ModeMatches("windows", 0o666, 0o600) || !initialReviewV2ModeMatches("windows", 0o666, 0o644) {
		t.Fatal("Windows writable regular-file modes must not be compared as Unix permission bits")
	}
	if initialReviewV2ModeMatches("darwin", 0o666, 0o600) || !initialReviewV2ModeMatches("darwin", 0o600, 0o600) {
		t.Fatal("POSIX recovery must preserve exact initialization permissions")
	}
}

func TestEnsureRootDirectoryPropagatesDurableCreatorFailure(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	durabilityErr := errors.New("injected durable directory failure")
	err = ensureRootDirectoryWith(root, "docs", 0o755, func(*os.Root, string, fs.FileMode) error {
		return durabilityErr
	})
	if !errors.Is(err, durabilityErr) {
		t.Fatalf("error=%v want=%v", err, durabilityErr)
	}
}

func TestEnsureRootDirectoryRetryResyncsExistingDirectory(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	firstErr := errors.New("injected project directory creation sync failure")
	err = ensureRootDirectoryWith(root, "docs", 0o755, func(root *os.Root, path string, perm fs.FileMode) error {
		if err := atomicfile.EnsureRootDir(root, path, perm); err != nil {
			return err
		}
		return firstErr
	})
	if !errors.Is(err, firstErr) {
		t.Fatalf("first error=%v", err)
	}
	retryErr := errors.New("injected existing project directory sync failure")
	calls := 0
	err = ensureRootDirectoryWith(root, "docs", 0o755, func(*os.Root, string, fs.FileMode) error {
		calls++
		return retryErr
	})
	if !errors.Is(err, retryErr) || calls != 1 {
		t.Fatalf("retry error=%v calls=%d", err, calls)
	}
}

func TestOpenOrCreateDirectoryPropagatesDurableCreatorFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine", "projects")
	durabilityErr := errors.New("injected durable machine directory failure")
	directory, err := openOrCreateDirectoryWith(path, 0o700, func(*os.Root, string, fs.FileMode) error {
		return durabilityErr
	})
	if directory != nil {
		_ = directory.Close()
		t.Fatal("returned directory after durability failure")
	}
	if !errors.Is(err, durabilityErr) {
		t.Fatalf("error=%v want=%v", err, durabilityErr)
	}
}

func TestOpenOrCreateDirectoryResyncsExistingFinalDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("injected existing final directory sync failure")
	calls := 0
	directory, err := openOrCreateDirectoryWith(path, 0o700, func(_ *os.Root, name string, _ fs.FileMode) error {
		calls++
		if name != "machine" {
			t.Fatalf("ensure name=%q", name)
		}
		return syncErr
	})
	if directory != nil {
		_ = directory.Close()
		t.Fatal("returned directory after existing publication sync failure")
	}
	if !errors.Is(err, syncErr) || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestInitializeCreatesStableProjectAndMapping(t *testing.T) {
	root := filepath.Join(t.TempDir(), "项目")
	vault := filepath.Join(t.TempDir(), "知识库")
	data := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data,
		Now:    func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
	}
	first, err := Initialize(opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Initialize(opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProjectID != second.ProjectID {
		t.Fatalf("ids differ: %q %q", first.ProjectID, second.ProjectID)
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(reviewv2.ReviewRelativePath)))
	if err != nil || !strings.Contains(string(b), "project-2a2a2a2a2a2a2a2a") {
		t.Fatalf("review=%q err=%v", b, err)
	}
	cfg, err := config.Load(first.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("projects=%+v, want one mapping", cfg.Projects)
	}
}

func TestDefaultVaultReviewPathIsStableAndCrossPlatformSafe(t *testing.T) {
	got, err := DefaultVaultReviewPath(`会话:审查. `, "project-2a2a2a2a2a2a2a2a")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.ToSlash(filepath.Join("Projects", "会话-审查--2a2a2a2a", "Session Review"))
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestDefaultVaultReviewPathSanitizesReservedEmptyUnicodeAndLongNames(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "CON.txt", want: "_CON.txt--2a2a2a2a"},
		{name: "\x00<>:\\|?*... ", want: "Project--2a2a2a2a"},
		{name: "Cafe\u0301", want: "Café--2a2a2a2a"},
		{name: strings.Repeat("界", 70), want: strings.Repeat("界", 64) + "--2a2a2a2a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := DefaultVaultReviewPath(test.name, "project-2a2a2a2a2a2a2a2a")
			if err != nil {
				t.Fatal(err)
			}
			want := "Projects/" + test.want + "/Session Review"
			if got != want {
				t.Fatalf("got=%q want=%q", got, want)
			}
		})
	}
	if _, err := DefaultVaultReviewPath("project", "not-a-project-id"); err == nil {
		t.Fatal("expected invalid project ID error")
	}
}

func TestInitializeBackfillsStableVaultMappingOnce(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: id, Root: root, VaultRoot: vault}}}); err != nil {
		t.Fatal(err)
	}
	writeTestOverview(t, root, id)
	opts := InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: runtime.GOOS, Random: errorReader{}}
	if _, err := Initialize(opts); err != nil {
		t.Fatal(err)
	}
	first, err := config.Load(filepath.Join(data, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(opts); err != nil {
		t.Fatal(err)
	}
	second, err := config.Load(filepath.Join(data, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Projects) != 1 || first.Projects[0].VaultReviewPath == "" || first.Projects[0].VaultCaseMode == "" {
		t.Fatalf("first=%+v", first)
	}
	if !reflect.DeepEqual(first.Projects[0], second.Projects[0]) {
		t.Fatalf("mapping changed: first=%+v second=%+v", first.Projects[0], second.Projects[0])
	}
}

func TestInitializePreservesNonemptyVaultMappingInsteadOfRecomputing(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	want := config.ProjectMapping{
		ID: id, Root: root, VaultRoot: vault,
		VaultReviewPath: "Projects/Old Name--2a2a2a2a/Session Review",
		VaultCaseMode:   platform.CaseSensitive,
	}
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{want}}); err != nil {
		t.Fatal(err)
	}
	writeTestOverview(t, root, id)
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: runtime.GOOS, Random: errorReader{},
		caseDetector: func(*os.Root) (platform.CaseMode, error) {
			t.Fatal("case mode must not be reprobed for a complete mapping")
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(filepath.Join(data, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Projects) != 1 || !reflect.DeepEqual(got.Projects[0], want) {
		t.Fatalf("got=%+v want=%+v", got.Projects, want)
	}
}

func TestDetectCaseModeUsesPinnedVaultAndCleansProbe(t *testing.T) {
	vault := t.TempDir()
	root, err := os.OpenRoot(vault)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := os.ReadDir(vault)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := detectCaseMode(root)
	if err != nil {
		t.Fatal(err)
	}
	if mode != platform.CaseSensitive && mode != platform.CaseInsensitive {
		t.Fatalf("mode=%q", mode)
	}
	after, err := os.ReadDir(vault)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("case probe left entries: before=%v after=%v", before, after)
	}
}

func TestInitializeInconclusiveCaseProbeDoesNotMutateConfig(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	configPath := filepath.Join(data, "config.toml")
	if err := config.Save(configPath, config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: id, Root: root, VaultRoot: vault}}}); err != nil {
		t.Fatal(err)
	}
	writeTestOverview(t, root, id)
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	probeErr := errors.New("inconclusive case probe")
	_, err = Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "darwin", Random: errorReader{},
		caseDetector: func(*os.Root) (platform.CaseMode, error) { return "", probeErr },
	})
	if !errors.Is(err, probeErr) {
		t.Fatalf("err=%v", err)
	}
	after, readErr := os.ReadFile(configPath)
	if readErr != nil || !bytes.Equal(before, after) {
		t.Fatalf("config mutated after failed probe: readErr=%v before=%q after=%q", readErr, before, after)
	}
	if _, statErr := os.Stat(filepath.Join(data, "projects")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("project state exists after failed probe: %v", statErr)
	}
}

func TestInitializeCreatesPrivateIdempotentSyncStateWithoutVaultDirectories(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	opts := InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows",
		Now:    func() time.Time { return time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC) },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
	}
	result, err := Initialize(opts)
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(data, "projects", result.ProjectID)
	paths := []string{"merge-bases", "queue", "transactions", "locks"}
	identities := make(map[string]os.FileInfo, len(paths)+1)
	for _, relative := range paths {
		path := filepath.Join(stateRoot, relative)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("state dir %s info=%v err=%v", relative, info, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("state dir %s mode=%o", relative, info.Mode().Perm())
		}
		identities[relative] = info
	}
	lockPath := filepath.Join(stateRoot, "locks", "sync.lock")
	lockInfo, err := os.Lstat(lockPath)
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("sync lock info=%v err=%v", lockInfo, err)
	}
	if runtime.GOOS != "windows" && lockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("sync lock mode=%o", lockInfo.Mode().Perm())
	}
	identities["locks/sync.lock"] = lockInfo
	if _, err := Initialize(opts); err != nil {
		t.Fatal(err)
	}
	for relative, first := range identities {
		second, err := os.Lstat(filepath.Join(stateRoot, filepath.FromSlash(relative)))
		if err != nil || !os.SameFile(first, second) {
			t.Fatalf("state identity changed for %s: err=%v", relative, err)
		}
	}
	if entries, err := os.ReadDir(vault); err != nil || len(entries) != 0 {
		t.Fatalf("vault was populated during init: entries=%v err=%v", entries, err)
	}
}

func TestInitializeRejectsRedirectedSyncStateWithoutMutatingConfig(t *testing.T) {
	tests := []string{"projects", "project", "merge-bases", "queue", "transactions", "locks", "sync.lock"}
	for _, redirected := range tests {
		t.Run(redirected, func(t *testing.T) {
			root, vault, data, outside := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
			id := "project-2a2a2a2a2a2a2a2a"
			configPath := filepath.Join(data, "config.toml")
			if err := config.Save(configPath, config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: id, Root: root, VaultRoot: vault}}}); err != nil {
				t.Fatal(err)
			}
			writeTestOverview(t, root, id)
			projectState := filepath.Join(data, "projects", id)
			var target string
			switch redirected {
			case "projects":
				target = filepath.Join(data, "projects")
			case "project":
				if err := os.Mkdir(filepath.Join(data, "projects"), 0o700); err != nil {
					t.Fatal(err)
				}
				target = projectState
			case "sync.lock":
				if err := os.MkdirAll(filepath.Join(projectState, "locks"), 0o700); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(projectState, "locks", "sync.lock")
			default:
				if err := os.MkdirAll(projectState, 0o700); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(projectState, redirected)
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: runtime.GOOS, Random: errorReader{}})
			if err == nil || (!strings.Contains(err.Error(), "redirect") && !strings.Contains(err.Error(), "regular") && !strings.Contains(err.Error(), "directory")) {
				t.Fatalf("err=%v", err)
			}
			after, readErr := os.ReadFile(configPath)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("config mutated: readErr=%v", readErr)
			}
		})
	}
}

func TestInitializeNewReviewContainsReservedV2Identity(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows",
		Now:    func() time.Time { return time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC) },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 16)),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(reviewv2.ReviewRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	want := "---\nid: project-overview\nentity_type: project_review\nproject_id: project-2a2a2a2a2a2a2a2a\nschema_version: 2\nrevision: 1\n---\n"
	if !strings.HasPrefix(string(body), want) {
		t.Fatalf("review=%q want prefix=%q", body, want)
	}
}

func TestInitializeMigratesOldOverviewWithoutClobberingUnknownContent(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	overviewPath := filepath.Join(root, "docs", "session-review", "project-overview.md")
	if err := os.MkdirAll(filepath.Dir(overviewPath), 0o755); err != nil {
		t.Fatal(err)
	}
	old := []byte("---\n# identity comment\nproject_id: " + id + "\nplugin_key:\n  nested: true\n---\n\nPreamble.\n\n## Plugin Section\n```query\n# not a heading\n```\n")
	if err := os.WriteFile(overviewPath, old, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: runtime.GOOS, Random: errorReader{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectID != id {
		t.Fatalf("project id=%q", result.ProjectID)
	}
	got, err := os.ReadFile(overviewPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"id: project-overview", "entity_type: project_overview", "revision: 1", "sync_status: synced", "# identity comment", "plugin_key:", "nested: true", "Preamble.", "## Plugin Section", "# not a heading"} {
		if !bytes.Contains(got, []byte(want)) {
			t.Fatalf("missing %q in migrated overview:\n%s", want, got)
		}
	}
	if cfg, err := config.Load(result.ConfigPath); err != nil || len(cfg.Projects) != 1 || cfg.Projects[0].ID != id {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
}

func TestInitializeOverviewMigrationConflictLeavesOverviewAndConfigUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra string
	}{
		{"conflicting id", "id: another-overview\n"},
		{"conflicting type", "entity_type: decision\n"},
		{"conflicting revision", "revision: 9\n"},
		{"conflicting status", "sync_status: conflicted\n"},
		{"duplicate key", "project_id: project-3333333333333333\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
			overviewPath := filepath.Join(root, "docs", "session-review", "project-overview.md")
			if err := os.MkdirAll(filepath.Dir(overviewPath), 0o755); err != nil {
				t.Fatal(err)
			}
			before := []byte("---\nproject_id: project-2a2a2a2a2a2a2a2a\n" + tc.extra + "---\n\n# Project\n")
			if err := os.WriteFile(overviewPath, before, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: runtime.GOOS}); !errors.Is(err, ErrConflictingInitializationIdentity) {
				t.Fatalf("err=%v", err)
			}
			after, err := os.ReadFile(overviewPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("overview changed: err=%v before=%q after=%q", err, before, after)
			}
			if _, err := os.Stat(filepath.Join(data, "config.toml")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("config changed: %v", err)
			}
		})
	}
}

func TestInitializePublishedOverviewMigrationSurvivesLaterFailureAndRetry(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	overviewPath := filepath.Join(root, "docs", "session-review", "project-overview.md")
	if err := os.MkdirAll(filepath.Dir(overviewPath), 0o755); err != nil {
		t.Fatal(err)
	}
	old := []byte("---\nproject_id: " + id + "\nunknown: keep\n---\n\n# Project\n")
	if err := os.WriteFile(overviewPath, old, 0o644); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("stop before config")
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: runtime.GOOS, Random: errorReader{},
		beforeConfigWrite: func() error { return sentinel },
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	migrated, err := os.ReadFile(overviewPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(migrated, old) || !bytes.Contains(migrated, []byte("id: project-overview")) || !bytes.Contains(migrated, []byte("unknown: keep")) {
		t.Fatalf("published migration was lost: %s", migrated)
	}
	result, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: runtime.GOOS, Random: errorReader{}})
	if err != nil || result.ProjectID != id {
		t.Fatalf("retry result=%+v err=%v", result, err)
	}
	after, err := os.ReadFile(overviewPath)
	if err != nil || !bytes.Equal(after, migrated) {
		t.Fatalf("retry changed migration: err=%v before=%q after=%q", err, migrated, after)
	}
}

func TestInitializeOverviewMigrationWaitsForMappingConflictPreflight(t *testing.T) {
	root, requestedVault, mappedVault, data := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	overviewPath := filepath.Join(root, "docs", "session-review", "project-overview.md")
	if err := os.MkdirAll(filepath.Dir(overviewPath), 0o755); err != nil {
		t.Fatal(err)
	}
	beforeOverview := []byte("---\nproject_id: " + id + "\nunknown: keep\n---\n\n# Project\n")
	if err := os.WriteFile(overviewPath, beforeOverview, 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(data, "config.toml")
	if err := config.Save(configPath, config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: id, Root: root, VaultRoot: mappedVault}}}); err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Initialize(InitOptions{ProjectRoot: root, VaultRoot: requestedVault, DataDir: data, GOOS: runtime.GOOS, Random: errorReader{}})
	if !errors.Is(err, ErrConflictingInitializationIdentity) {
		t.Fatalf("err=%v", err)
	}
	afterOverview, overviewErr := os.ReadFile(overviewPath)
	afterConfig, configErr := os.ReadFile(configPath)
	if overviewErr != nil || !bytes.Equal(afterOverview, beforeOverview) {
		t.Fatalf("overview changed before conflict preflight: err=%v before=%q after=%q", overviewErr, beforeOverview, afterOverview)
	}
	if configErr != nil || !bytes.Equal(afterConfig, beforeConfig) {
		t.Fatalf("config changed before conflict preflight: err=%v before=%q after=%q", configErr, beforeConfig, afterConfig)
	}
}

func TestInitializeRestoresCanonicalPrimaryFromOverviewBackup(t *testing.T) {
	for _, invalidPrimary := range []bool{false, true} {
		name := "backup-only"
		if invalidPrimary {
			name = "invalid-primary"
		}
		t.Run(name, func(t *testing.T) {
			root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
			id := "project-2a2a2a2a2a2a2a2a"
			overviewPath := filepath.Join(root, "docs", "session-review", "project-overview.md")
			if err := os.MkdirAll(filepath.Dir(overviewPath), 0o755); err != nil {
				t.Fatal(err)
			}
			backup := []byte("---\nproject_id: " + id + "\nbackup_unknown: keep\n---\n\n# Backup Project\n")
			if err := os.WriteFile(atomicfile.BackupPath(overviewPath), backup, 0o644); err != nil {
				t.Fatal(err)
			}
			if invalidPrimary {
				if err := os.WriteFile(overviewPath, []byte("not valid frontmatter\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			sentinel := errors.New("stop after backup recovery")
			_, err := Initialize(InitOptions{
				ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: runtime.GOOS, Random: errorReader{},
				beforeConfigWrite: func() error { return sentinel },
			})
			if !errors.Is(err, sentinel) {
				t.Fatalf("recovery failure err=%v", err)
			}
			primary, err := os.ReadFile(overviewPath)
			if err != nil {
				t.Fatalf("canonical primary missing: %v", err)
			}
			for _, want := range []string{"id: project-overview", "entity_type: project_overview", "project_id: " + id, "revision: 1", "sync_status: synced", "backup_unknown: keep", "# Backup Project"} {
				if !bytes.Contains(primary, []byte(want)) {
					t.Fatalf("missing %q in restored primary:\n%s", want, primary)
				}
			}
			result, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: runtime.GOOS, Random: errorReader{}})
			if err != nil || result.ProjectID != id {
				t.Fatalf("retry result=%+v err=%v", result, err)
			}
			after, err := os.ReadFile(overviewPath)
			if err != nil || !bytes.Equal(after, primary) {
				t.Fatalf("retry changed primary: err=%v before=%q after=%q", err, primary, after)
			}
		})
	}
}

func TestInitializeFailureBeforeOverviewLeavesNoIdentityOrState(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	sentinel := errors.New("stop before overview")
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows",
		Random:              bytes.NewReader(bytes.Repeat([]byte{0x2a}, 8)),
		beforeOverviewWrite: func() error { return sentinel },
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	assertNewInitializationAbsent(t, root, data, "project-2a2a2a2a2a2a2a2a")
}

func TestInitializeRealOverviewWriteFailureLeavesNoIdentityOrState(t *testing.T) {
	root, vault, data, outside := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows",
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 8)),
		beforeOverviewWrite: func() error {
			return os.Symlink(outside, filepath.Join(root, "docs"))
		},
	})
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("err=%v", err)
	}
	assertNewInitializationAbsent(t, root, data, "project-2a2a2a2a2a2a2a2a")
	if _, err := os.Stat(filepath.Join(outside, "session-review", "project-overview.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overview escaped through redirect: %v", err)
	}
}

func TestInitializeFailureAfterOverviewBeforeStateRecoversSameIdentity(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	sentinel := errors.New("stop after overview")
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows",
		Random:             bytes.NewReader(bytes.Repeat([]byte{0x2a}, 8)),
		afterOverviewWrite: func() error { return sentinel },
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	assertInitializedV2Identity(t, root, id)
	assertPathMissing(t, filepath.Join(data, "projects", id))
	assertPathMissing(t, filepath.Join(data, "config.toml"))

	result, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows", Random: errorReader{}})
	if err != nil || result.ProjectID != id {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertExactInitializationScaffold(t, data, id)
	assertSingleMapping(t, data, id)
}

func TestInitializeFailureAfterStateBeforeConfigRecoversSameIdentity(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	sentinel := errors.New("stop before config")
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows",
		Random:            bytes.NewReader(bytes.Repeat([]byte{0x2a}, 8)),
		beforeConfigWrite: func() error { return sentinel },
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	assertInitializedV2Identity(t, root, id)
	assertExactInitializationScaffold(t, data, id)
	assertPathMissing(t, filepath.Join(data, "config.toml"))

	result, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows", Random: errorReader{}})
	if err != nil || result.ProjectID != id {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertSingleMapping(t, data, id)
}

func TestInitializeFailureDuringStateRecoversExactScaffold(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	sentinel := errors.New("stop during state")
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows",
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 8)),
		afterStateComponent: func(name string) error {
			if name == "queue" {
				return sentinel
			}
			return nil
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	assertInitializedV2Identity(t, root, id)
	assertPathMissing(t, filepath.Join(data, "config.toml"))

	result, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows", Random: errorReader{}})
	if err != nil || result.ProjectID != id {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertExactInitializationScaffold(t, data, id)
	assertSingleMapping(t, data, id)
}

func TestInitializeOverviewRecoveryRevalidatesEarlierStateComponents(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	writeTestOverview(t, root, id)
	marker := filepath.Join(data, "projects", id, "merge-bases", "late-marker")

	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows", Random: errorReader{},
		afterStateComponent: func(name string) error {
			if name != "queue" {
				return nil
			}
			return os.WriteFile(marker, []byte("preserve"), 0o600)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected content") {
		t.Fatalf("err=%v", err)
	}
	assertOverviewIdentity(t, root, id)
	body, readErr := os.ReadFile(marker)
	if readErr != nil || string(body) != "preserve" {
		t.Fatalf("marker=%q readErr=%v", body, readErr)
	}
	assertPathMissing(t, filepath.Join(data, "config.toml"))
}

func TestInitializeOverviewRecoveryRevalidatesEarlierStateModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission-bit mutation is not portable to Windows")
	}
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	writeTestOverview(t, root, id)
	queue := filepath.Join(data, "projects", id, "queue")

	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows", Random: errorReader{},
		afterStateComponent: func(name string) error {
			if name != "sync.lock" {
				return nil
			}
			return os.Chmod(queue, 0o755)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("err=%v", err)
	}
	assertOverviewIdentity(t, root, id)
	assertPathMissing(t, filepath.Join(data, "config.toml"))
}

func TestInitializeConfigPostPublicationAmbiguityRecovers(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	sentinel := errors.New("config publication result ambiguous")
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows",
		Random:           bytes.NewReader(bytes.Repeat([]byte{0x2a}, 8)),
		afterConfigWrite: func() error { return sentinel },
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	assertInitializedV2Identity(t, root, id)
	assertExactInitializationScaffold(t, data, id)
	assertSingleMapping(t, data, id)

	result, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows", Random: errorReader{}})
	if err != nil || result.ProjectID != id {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertSingleMapping(t, data, id)
}

func TestInitializeDoesNotDeletePreexistingGeneratedProjectState(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	stateRoot := filepath.Join(data, "projects", id)
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(stateRoot, "user-state")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows",
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 8)),
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err=%v", err)
	}
	body, readErr := os.ReadFile(marker)
	if readErr != nil || string(body) != "keep" {
		t.Fatalf("preexisting state changed: body=%q err=%v", body, readErr)
	}
	assertPathMissing(t, filepath.Join(root, "docs", "session-review", "project-overview.md"))
	assertPathMissing(t, filepath.Join(data, "config.toml"))
}

func TestInitializeOverviewRecoveryRejectsNonemptyInitializationScaffold(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	writeTestOverview(t, root, id)
	stateRoot := filepath.Join(data, "projects", id)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := os.MkdirAll(filepath.Join(stateRoot, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(stateRoot, "queue", "user-state")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows", Random: errorReader{}})
	if err == nil || !strings.Contains(err.Error(), "unexpected content") {
		t.Fatalf("err=%v", err)
	}
	body, readErr := os.ReadFile(marker)
	if readErr != nil || string(body) != "keep" {
		t.Fatalf("marker changed: body=%q err=%v", body, readErr)
	}
	assertPathMissing(t, filepath.Join(data, "config.toml"))
}

func TestInitializeOverviewRecoveryRejectsMalformedInitializationScaffold(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, stateRoot string)
	}{
		{
			name: "extra root entry",
			mutate: func(t *testing.T, stateRoot string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(stateRoot, "marker"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "nonempty sync lock",
			mutate: func(t *testing.T, stateRoot string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(stateRoot, "locks", "sync.lock"), []byte("owner"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	if runtime.GOOS != "windows" {
		tests = append(tests, struct {
			name   string
			mutate func(t *testing.T, stateRoot string)
		}{
			name: "wrong private mode",
			mutate: func(t *testing.T, stateRoot string) {
				t.Helper()
				if err := os.Chmod(filepath.Join(stateRoot, "queue"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
			id := "project-2a2a2a2a2a2a2a2a"
			writeTestOverview(t, root, id)
			stateRoot := writeExactTestScaffold(t, data, id)
			test.mutate(t, stateRoot)

			_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, GOOS: "windows", Random: errorReader{}})
			if err == nil {
				t.Fatal("expected malformed recovery scaffold rejection")
			}
			assertPathMissing(t, filepath.Join(data, "config.toml"))
		})
	}
}

func assertNewInitializationAbsent(t *testing.T, projectRoot, dataRoot, projectID string) {
	t.Helper()
	assertPathMissing(t, filepath.Join(dataRoot, "projects", projectID))
	assertPathMissing(t, filepath.Join(projectRoot, "docs", "session-review", "project-overview.md"))
	assertPathMissing(t, filepath.Join(projectRoot, filepath.FromSlash(reviewv2.ReviewRelativePath)))
	assertPathMissing(t, filepath.Join(projectRoot, filepath.FromSlash(reviewv2.HistoryRelativePath)))
	assertPathMissing(t, filepath.Join(projectRoot, filepath.FromSlash(reviewv2.MachineLedgerRelativePath)))
	assertPathMissing(t, filepath.Join(dataRoot, "config.toml"))
	assertPathMissing(t, filepath.Join(dataRoot, "config.toml.session-reviewer-backup"))
}

func assertInitializedV2Identity(t *testing.T, projectRoot, projectID string) {
	t.Helper()
	accepted, err := reviewv2.Load(projectRoot)
	if err != nil || accepted.State.Review.ProjectID != projectID {
		t.Fatalf("review v2 project_id=%q err=%v", accepted.State.Review.ProjectID, err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %s exists or is inaccessible: %v", path, err)
	}
}

func assertOverviewIdentity(t *testing.T, projectRoot, projectID string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(projectRoot, "docs", "session-review", "project-overview.md"))
	if err != nil || !strings.Contains(string(body), "project_id: "+projectID+"\n") {
		t.Fatalf("overview=%q err=%v", body, err)
	}
}

func assertExactInitializationScaffold(t *testing.T, dataRoot, projectID string) {
	t.Helper()
	stateRoot := filepath.Join(dataRoot, "projects", projectID)
	stateInfo, err := os.Lstat(stateRoot)
	if err != nil || !stateInfo.IsDir() || stateInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("state root info=%v err=%v", stateInfo, err)
	}
	if runtime.GOOS != "windows" && stateInfo.Mode().Perm() != 0o700 {
		t.Fatalf("state root mode=%o", stateInfo.Mode().Perm())
	}
	entries, err := os.ReadDir(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	stateRootHandle, err := os.OpenRoot(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if atomicfile.IsRootDirectoryLockName(entry.Name()) {
			if err := atomicfile.ValidateRootDirectoryLock(stateRootHandle, entry.Name()); err != nil {
				_ = stateRootHandle.Close()
				t.Fatal(err)
			}
			continue
		}
		filtered = append(filtered, entry)
	}
	if err := stateRootHandle.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"locks", "merge-bases", "queue", "transactions"}
	if got := entryNames(filtered); !reflect.DeepEqual(got, want) {
		t.Fatalf("state entries=%q want=%q", got, want)
	}
	for _, name := range []string{"merge-bases", "queue", "transactions"} {
		path := filepath.Join(stateRoot, name)
		info, statErr := os.Lstat(path)
		contents, readErr := os.ReadDir(path)
		if statErr != nil || readErr != nil || !info.IsDir() || len(contents) != 0 {
			t.Fatalf("state dir %s info=%v contents=%v statErr=%v readErr=%v", name, info, contents, statErr, readErr)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("state dir %s mode=%o", name, info.Mode().Perm())
		}
	}
	locks := filepath.Join(stateRoot, "locks")
	locksInfo, err := os.Lstat(locks)
	if err != nil || !locksInfo.IsDir() || locksInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("locks info=%v err=%v", locksInfo, err)
	}
	if runtime.GOOS != "windows" && locksInfo.Mode().Perm() != 0o700 {
		t.Fatalf("locks mode=%o", locksInfo.Mode().Perm())
	}
	lockEntries, err := os.ReadDir(locks)
	if err != nil || len(lockEntries) != 1 || lockEntries[0].Name() != "sync.lock" {
		t.Fatalf("lock entries=%v err=%v", lockEntries, err)
	}
	lockInfo, err := os.Lstat(filepath.Join(locks, "sync.lock"))
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Size() != 0 {
		t.Fatalf("lock info=%v err=%v", lockInfo, err)
	}
	if runtime.GOOS != "windows" && lockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode=%o", lockInfo.Mode().Perm())
	}
}

func writeExactTestScaffold(t *testing.T, dataRoot, projectID string) string {
	t.Helper()
	stateRoot := filepath.Join(dataRoot, "projects", projectID)
	for _, name := range []string{"merge-bases", "queue", "transactions", "locks"} {
		if err := os.MkdirAll(filepath.Join(stateRoot, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "locks", "sync.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return stateRoot
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}

func assertSingleMapping(t *testing.T, dataRoot, projectID string) {
	t.Helper()
	cfg, err := config.Load(filepath.Join(dataRoot, "config.toml"))
	if err != nil || len(cfg.Projects) != 1 || cfg.Projects[0].ID != projectID {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestPreviewInitializationDoesNotCreateDataOrLedger(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	vault := filepath.Join(base, "vault")
	data := filepath.Join(base, "machine")
	for _, path := range []string{root, vault} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	preview, err := PreviewInitialization(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Action != "create" || preview.LedgerRoot != filepath.Join(root, "docs", "session-review") {
		t.Fatalf("preview=%+v", preview)
	}
	for _, path := range []string{data, filepath.Join(root, "docs")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists: %v", path, err)
		}
	}
}

func TestPreviewInitializationClassifiesActionableFailures(t *testing.T) {
	t.Run("invalid root", func(t *testing.T) {
		_, err := PreviewInitialization(InitOptions{
			ProjectRoot: filepath.Join(t.TempDir(), "missing"),
			VaultRoot:   t.TempDir(),
			DataDir:     t.TempDir(),
		})
		if !errors.Is(err, ErrInvalidInitializationRoot) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("nested roots", func(t *testing.T) {
		root := t.TempDir()
		vault := filepath.Join(root, "vault")
		if err := os.Mkdir(vault, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := PreviewInitialization(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: t.TempDir()})
		if !errors.Is(err, ErrNestedInitializationRoots) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("corrupt config", func(t *testing.T) {
		root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(data, "config.toml"), []byte("not = [valid"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := PreviewInitialization(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data})
		if !errors.Is(err, ErrCorruptInitializationConfig) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestInitializeRevalidatesRootsAfterAcquiringTransactionLock(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	moved := filepath.Join(base, "moved")
	outside := filepath.Join(base, "outside")
	for _, path := range []string{root, outside} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := PreviewInitialization(InitOptions{ProjectRoot: root, VaultRoot: outside, DataDir: filepath.Join(base, "data")}); err != nil {
		t.Fatal(err)
	}

	_, err := Initialize(InitOptions{
		ProjectRoot: root,
		VaultRoot:   outside,
		DataDir:     filepath.Join(base, "data"),
		afterLock: func() error {
			if err := os.Rename(root, moved); err != nil {
				return err
			}
			return os.Symlink(outside, root)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "symlink or reparse point") {
		t.Fatalf("err=%v", err)
	}
	if !errors.Is(err, ErrInitializationStateChanged) {
		t.Fatalf("err=%v does not classify the preview/write race", err)
	}
	for _, path := range []string{filepath.Join(moved, "docs"), filepath.Join(outside, "docs")} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("write escaped revalidation at %s: %v", path, statErr)
		}
	}
}

func TestInitializeClassifiesConflictingIdentity(t *testing.T) {
	firstRoot, root, firstVault, vault, data := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	const projectID = "project-1111111111111111"
	writeTestOverview(t, root, projectID)
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: projectID, Root: firstRoot, VaultRoot: firstVault,
	}}}); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data})
	if !errors.Is(err, ErrConflictingInitializationIdentity) {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeRejectsNestedVaultAndProject(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "must not contain") {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeRejectsSymlinkedRoot(t *testing.T) {
	realRoot := t.TempDir()
	linkedRoot := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := Initialize(InitOptions{ProjectRoot: linkedRoot, VaultRoot: t.TempDir(), DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "symlink or reparse point") {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeRejectsRedirectedDocsWithoutWritingOutside(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "docs")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "redirected") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "session-review", "project-overview.md")); !os.IsNotExist(statErr) {
		t.Fatalf("outside overview exists or could not be inspected: %v", statErr)
	}
}

func TestInitializeRejectsEqualProjectAndVaultRoots(t *testing.T) {
	root := t.TempDir()
	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: root, DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "must not contain") {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeRejectsPhysicalContainmentThroughAliasedAncestor(t *testing.T) {
	realBase := t.TempDir()
	aliasBase := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realBase, aliasBase); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root := filepath.Join(aliasBase, "project")
	vault := filepath.Join(realBase, "project", "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: t.TempDir()})
	if err == nil || (!strings.Contains(err.Error(), "must not contain") && !strings.Contains(err.Error(), "symlink or reparse point")) {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeRejectsCanonicalRootAlias(t *testing.T) {
	realBase := t.TempDir()
	aliasBase := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realBase, aliasBase); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	realRoot := filepath.Join(realBase, "project")
	aliasRoot := filepath.Join(aliasBase, "project")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	vault := t.TempDir()
	data := t.TempDir()
	_, err := Initialize(InitOptions{ProjectRoot: realRoot, VaultRoot: vault, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Initialize(InitOptions{ProjectRoot: aliasRoot, VaultRoot: vault, DataDir: data, Random: errorReader{}})
	if err == nil || !strings.Contains(err.Error(), "symlink or reparse point") {
		t.Fatalf("err=%v", err)
	}
	cfg, err := config.Load(filepath.Join(data, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("projects=%+v, want one canonical mapping", cfg.Projects)
	}
}

func TestInitializeRejectsDifferentVaultForExistingProject(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	data := t.TempDir()
	if _, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data}); err != nil {
		t.Fatal(err)
	}

	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: t.TempDir(), DataDir: data})
	if err == nil || !strings.Contains(err.Error(), "different vault") {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeConcurrentSameProjectUsesOneIdentity(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	data := t.TempDir()
	const workers = 8
	start := make(chan struct{})
	type outcome struct {
		result InitResult
		err    error
	}
	outcomes := make(chan outcome, workers)
	for worker := 0; worker < workers; worker++ {
		worker := worker
		go func() {
			<-start
			result, err := Initialize(InitOptions{
				ProjectRoot: root,
				VaultRoot:   vault,
				DataDir:     data,
				Random:      slowByteReader{value: byte(worker + 1)},
			})
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	var projectID string
	for range workers {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if projectID == "" {
			projectID = outcome.result.ProjectID
		}
		if outcome.result.ProjectID != projectID {
			t.Fatalf("split project IDs: first=%q got=%q", projectID, outcome.result.ProjectID)
		}
	}
	cfg, err := config.Load(filepath.Join(data, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].ID != projectID {
		t.Fatalf("cfg=%+v projectID=%q", cfg, projectID)
	}
}

func TestInitializeConcurrentDifferentProjectsKeepsEveryMapping(t *testing.T) {
	data := t.TempDir()
	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		root := t.TempDir()
		vault := t.TempDir()
		worker := worker
		go func() {
			<-start
			_, err := Initialize(InitOptions{
				ProjectRoot: root,
				VaultRoot:   vault,
				DataDir:     data,
				Random:      slowByteReader{value: byte(worker + 1)},
			})
			errs <- err
		}()
	}
	close(start)
	for range workers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(filepath.Join(data, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != workers {
		t.Fatalf("projects=%+v, want %d mappings", cfg.Projects, workers)
	}
}

func TestInitializeRecoversOverviewWithoutMapping(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	data := t.TempDir()
	wantID := "project-1111111111111111"
	writeTestOverview(t, root, wantID)

	result, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, Random: errorReader{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectID != wantID {
		t.Fatalf("projectID=%q want=%q", result.ProjectID, wantID)
	}
	cfg, err := config.Load(result.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].ID != wantID {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestInitializeRejectsOverviewIDClaimedByAnotherRoot(t *testing.T) {
	first, second, firstVault, secondVault, data := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	wantID := "project-1111111111111111"
	writeTestOverview(t, second, wantID)
	if err := config.Save(filepath.Join(data, "config.toml"), config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: wantID, Root: first, VaultRoot: firstVault,
	}}}); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(InitOptions{ProjectRoot: second, VaultRoot: secondVault, DataDir: data})
	if err == nil || !strings.Contains(err.Error(), "already belongs to another project root") {
		t.Fatalf("err=%v", err)
	}
	if got, _ := config.Load(filepath.Join(data, "config.toml")); len(got.Projects) != 1 {
		t.Fatalf("projects=%+v", got.Projects)
	}
}

func TestInitializeRecoversMappingWithoutOverview(t *testing.T) {
	root := t.TempDir()
	vault := t.TempDir()
	data := t.TempDir()
	wantID := "project-2222222222222222"
	configPath := filepath.Join(data, "config.toml")
	if err := config.Save(configPath, config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: wantID, Root: root, VaultRoot: vault,
	}}}); err != nil {
		t.Fatal(err)
	}

	result, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: vault, DataDir: data, Random: errorReader{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectID != wantID {
		t.Fatalf("projectID=%q want=%q", result.ProjectID, wantID)
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(reviewv2.ReviewRelativePath)))
	if err != nil || !strings.Contains(string(b), "project_id: "+wantID) {
		t.Fatalf("review=%q err=%v", b, err)
	}
}

func TestInitializeRecoversBackupOnlyConfigWithoutLosingMappings(t *testing.T) {
	firstRoot, secondRoot, vault, data := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	configPath := filepath.Join(data, "config.toml")
	want := config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: "project-1111111111111111", Root: firstRoot, VaultRoot: vault}}}
	if err := config.Save(configPath, want); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(configPath, configPath+".session-reviewer-backup"); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(InitOptions{ProjectRoot: secondRoot, VaultRoot: t.TempDir(), DataDir: data, Random: bytes.NewReader(bytes.Repeat([]byte{0x22}, 16))}); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Projects) != 2 || !reflect.DeepEqual(got.Projects[0], want.Projects[0]) {
		t.Fatalf("mappings=%+v", got.Projects)
	}
}

func TestInitializeUsesBackupOnlyOverviewIdentity(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	wantID := "project-3333333333333333"
	writeTestOverview(t, root, wantID)
	path := filepath.Join(root, "docs", "session-review", "project-overview.md")
	if err := os.Rename(path, path+".session-reviewer-backup"); err != nil {
		t.Fatal(err)
	}
	result, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, Random: errorReader{}})
	if err != nil || result.ProjectID != wantID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestInitializeUsesValidOverviewBackupWhenPrimaryIsInvalid(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	wantID := "project-4444444444444444"
	writeTestOverview(t, root, wantID)
	path := filepath.Join(root, "docs", "session-review", "project-overview.md")
	if err := os.Rename(path, path+".session-reviewer-backup"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nproject_id: invalid\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data, Random: errorReader{}})
	if err != nil || result.ProjectID != wantID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestInitializeRejectsRedirectedAncestor(t *testing.T) {
	base, realBase := t.TempDir(), t.TempDir()
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(realBase, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root := filepath.Join(alias, "project")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: t.TempDir(), DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "symlink or reparse point") {
		t.Fatalf("err=%v", err)
	}
}

func TestInitializeProjectRootReplacementCannotRedirectOverviewWrite(t *testing.T) {
	base, root, moved, outside := t.TempDir(), "", "", ""
	root = filepath.Join(base, "project")
	moved = filepath.Join(base, "moved")
	outside = filepath.Join(base, "outside")
	data := t.TempDir()
	for _, dir := range []string{root, outside} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: t.TempDir(), DataDir: data, beforeOverviewWrite: func() error {
		if err := os.Rename(root, moved); err != nil {
			return err
		}
		return os.Symlink(outside, root)
	}})
	if err == nil || !errors.Is(err, ErrInitializationStateChanged) {
		t.Fatalf("replacement returned err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(moved, filepath.FromSlash(reviewv2.ReviewRelativePath))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "docs")); !os.IsNotExist(err) {
		t.Fatalf("outside write: %v", err)
	}
	cfg, err := config.Load(filepath.Join(data, "config.toml"))
	if err != nil || len(cfg.Projects) != 0 {
		t.Fatalf("replacement mapping was published: cfg=%+v err=%v", cfg, err)
	}
}

func TestInitializeProjectRootIdentityChangeBeforeConfigPublicationFailsClosed(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	moved := filepath.Join(base, "moved")
	data := t.TempDir()
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: t.TempDir(), DataDir: data,
		beforeConfigWrite: func() error {
			if err := os.Rename(root, moved); err != nil {
				return err
			}
			return os.Mkdir(root, 0o755)
		},
	})
	if err == nil || !errors.Is(err, ErrInitializationStateChanged) {
		t.Fatalf("identity replacement returned err=%v", err)
	}
	cfg, loadErr := config.Load(filepath.Join(data, "config.toml"))
	if loadErr != nil || len(cfg.Projects) != 0 {
		t.Fatalf("identity replacement mapping was published: cfg=%+v err=%v", cfg, loadErr)
	}
	if _, statErr := os.Stat(filepath.Join(moved, filepath.FromSlash(reviewv2.ReviewRelativePath))); statErr != nil {
		t.Fatalf("pinned root did not retain recoverable v2 files: %v", statErr)
	}
}

func TestInitializeProjectRootIdentityChangeAfterFragmentCommitIsExternalChange(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	moved := filepath.Join(base, "moved")
	data := t.TempDir()
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: t.TempDir(), DataDir: data,
		afterConfigWrite: func() error {
			if err := os.Rename(root, moved); err != nil {
				return err
			}
			return os.Mkdir(root, 0o755)
		},
	})
	if err != nil || result.ProjectID == "" {
		t.Fatalf("post-commit identity replacement result=%+v err=%v", result, err)
	}
	cfg, loadErr := config.Load(filepath.Join(data, "config.toml"))
	if loadErr != nil || len(cfg.Projects) != 1 || cfg.Projects[0].ID != result.ProjectID {
		t.Fatalf("committed mapping missing: cfg=%+v err=%v", cfg, loadErr)
	}
	if _, statErr := os.Stat(filepath.Join(moved, filepath.FromSlash(reviewv2.ReviewRelativePath))); statErr != nil {
		t.Fatalf("pinned root did not retain recoverable v2 files: %v", statErr)
	}
}

func TestInitializePostCommitRootReplacementNeverChangesSeededConfigBytes(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	moved := filepath.Join(base, "moved")
	data := t.TempDir()
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	unrelatedRoot, unrelatedVault := t.TempDir(), t.TempDir()
	seeded := []byte("# preserve this exact user formatting\nversion = 1\n\n[[projects]]\nid = \"project-1111111111111111\"\nroot = " + fmt.Sprintf("%q", unrelatedRoot) + "\nvault_root = " + fmt.Sprintf("%q", unrelatedVault) + "\n")
	configPath := filepath.Join(data, "config.toml")
	if err := os.WriteFile(configPath, seeded, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: t.TempDir(), DataDir: data,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 8)),
		afterConfigWrite: func() error {
			if err := os.Rename(root, moved); err != nil {
				return err
			}
			return os.Mkdir(root, 0o755)
		},
	})
	if err != nil || result.ProjectID == "" {
		t.Fatalf("post-commit root replacement result=%+v err=%v", result, err)
	}
	if got, readErr := os.ReadFile(configPath); readErr != nil || !bytes.Equal(got, seeded) {
		t.Fatalf("seeded config bytes changed: got=%q want=%q err=%v", got, seeded, readErr)
	}
}

func TestInitializeConcurrentSharedConfigEditIsPreservedByteForByte(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	data := t.TempDir()
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	unrelatedRoot, unrelatedVault := t.TempDir(), t.TempDir()
	concurrentRoot, concurrentVault := t.TempDir(), t.TempDir()
	seededConfig := config.Config{Version: 1, Projects: []config.ProjectMapping{{
		ID: "project-1111111111111111", Root: unrelatedRoot, VaultRoot: unrelatedVault,
	}}}
	configPath := filepath.Join(data, "config.toml")
	if err := config.Save(configPath, seededConfig); err != nil {
		t.Fatal(err)
	}
	var concurrentBytes []byte
	result, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: t.TempDir(), DataDir: data,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 8)),
		beforeConfigWrite: func() error {
			concurrent := config.Config{Version: 1, Projects: append(append([]config.ProjectMapping(nil), seededConfig.Projects...), config.ProjectMapping{
				ID: "project-2222222222222222", Root: concurrentRoot, VaultRoot: concurrentVault,
			})}
			if err := config.Save(configPath, concurrent); err != nil {
				return err
			}
			var err error
			concurrentBytes, err = os.ReadFile(configPath)
			return err
		},
	})
	if err != nil || result.ProjectID == "" {
		t.Fatalf("concurrent config edit result=%+v err=%v", result, err)
	}
	got, readErr := os.ReadFile(configPath)
	if readErr != nil || !bytes.Equal(got, concurrentBytes) {
		t.Fatalf("concurrent config bytes were overwritten: got=%q want=%q err=%v", got, concurrentBytes, readErr)
	}
	current, loadErr := config.Load(configPath)
	if loadErr != nil || len(current.Projects) != 3 {
		t.Fatalf("concurrent config mappings=%+v err=%v", current.Projects, loadErr)
	}
}

func TestInitializeConflictingSharedConfigEditBeforeCommitPublishesNoFragment(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	data := t.TempDir()
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(data, "config.toml")
	id := "project-2a2a2a2a2a2a2a2a"
	conflictingRoot, conflictingVault := t.TempDir(), t.TempDir()
	var concurrentBytes []byte
	_, err := Initialize(InitOptions{
		ProjectRoot: root, VaultRoot: t.TempDir(), DataDir: data,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 8)),
		beforeConfigWrite: func() error {
			if err := config.Save(configPath, config.Config{Version: 1, Projects: []config.ProjectMapping{{ID: id, Root: conflictingRoot, VaultRoot: conflictingVault}}}); err != nil {
				return err
			}
			var err error
			concurrentBytes, err = os.ReadFile(configPath)
			return err
		},
	})
	if err == nil || !errors.Is(err, config.ErrProjectFragmentConflict) {
		t.Fatalf("conflicting edit err=%v", err)
	}
	if got, readErr := os.ReadFile(configPath); readErr != nil || !bytes.Equal(got, concurrentBytes) {
		t.Fatalf("concurrent config changed: got=%q want=%q err=%v", got, concurrentBytes, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(data, config.ProjectFragmentsDir, id+".toml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("fragment was published before conflict: %v", statErr)
	}
}

func TestInitializeDataRootReplacementCannotRedirectConfigWrite(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	moved := filepath.Join(base, "moved")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{data, outside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	renameDenied := false
	_, err := Initialize(InitOptions{ProjectRoot: t.TempDir(), VaultRoot: t.TempDir(), DataDir: data, beforeConfigWrite: func() error {
		if err := os.Rename(data, moved); err != nil {
			renameDenied = runtime.GOOS == "windows" && (errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.Errno(32)))
			return err
		}
		return os.Symlink(outside, data)
	}})
	if renameDenied {
		if err == nil {
			t.Fatal("denied data-root replacement was accepted")
		}
		if _, statErr := os.Stat(filepath.Join(outside, config.ProjectFragmentsDir)); !os.IsNotExist(statErr) {
			t.Fatalf("outside write after denied replacement: %v", statErr)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "configuration root changed") {
		t.Fatalf("detached data-root publication was accepted: %v", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(moved, config.ProjectFragmentsDir))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".toml") {
			t.Fatalf("detached data root received mapping %q", entry.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(outside, config.ProjectFragmentsDir)); !os.IsNotExist(err) {
		t.Fatalf("outside write: %v", err)
	}
}

func TestInitializeLockReleasedAfterSubprocessCrash(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestInitSubprocessHelper$")
	cmd.Env = append(os.Environ(), "SESSION_REVIEWER_INIT_HELPER=crash", "SESSION_REVIEWER_INIT_PROJECT="+root, "SESSION_REVIEWER_INIT_VAULT="+vault, "SESSION_REVIEWER_INIT_DATA="+data)
	if err := cmd.Run(); err == nil {
		t.Fatal("helper did not crash")
	}
	if _, err := Initialize(InitOptions{ProjectRoot: root, VaultRoot: vault, DataDir: data}); err != nil {
		t.Fatalf("lock survived crashed owner: %v", err)
	}
}

func TestInitializeTwoProcessesSerializeOneMapping(t *testing.T) {
	root, vault, data := t.TempDir(), t.TempDir(), t.TempDir()
	commands := make([]*exec.Cmd, 2)
	for i := range commands {
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestInitSubprocessHelper$")
		commands[i].Env = append(os.Environ(), "SESSION_REVIEWER_INIT_HELPER=normal", "SESSION_REVIEWER_INIT_PROJECT="+root, "SESSION_REVIEWER_INIT_VAULT="+vault, "SESSION_REVIEWER_INIT_DATA="+data)
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(filepath.Join(data, "config.toml"))
	if err != nil || len(cfg.Projects) != 1 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestInitSubprocessHelper(t *testing.T) {
	mode := os.Getenv("SESSION_REVIEWER_INIT_HELPER")
	if mode == "" {
		return
	}
	opts := InitOptions{ProjectRoot: os.Getenv("SESSION_REVIEWER_INIT_PROJECT"), VaultRoot: os.Getenv("SESSION_REVIEWER_INIT_VAULT"), DataDir: os.Getenv("SESSION_REVIEWER_INIT_DATA")}
	if mode == "crash" {
		opts.afterLock = func() error { os.Exit(23); return nil }
	}
	if _, err := Initialize(opts); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireInitLockUsesPersistentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml.lock")
	if err := os.WriteFile(path, []byte("unknown-owner"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lock, err := acquireInitLock(root, "config.toml.lock", 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}
	b, readErr := os.ReadFile(path)
	if readErr != nil || string(b) != "unknown-owner" {
		t.Fatalf("lock=%q err=%v", b, readErr)
	}
}

func TestInsideUsesTargetWindowsPathSemantics(t *testing.T) {
	if !inside("windows", `C:\Projects\Repo`, `c:/projects/repo/docs`) {
		t.Fatal("expected Windows child path to be inside parent")
	}
	if inside("windows", `C:\Projects\Repo`, `c:/projects/repository`) {
		t.Fatal("path prefix without a component boundary must not count as containment")
	}
	if inside("windows", `C:\Projects\Repo`, `D:\Projects\Repo\docs`) {
		t.Fatal("different Windows drives must not count as containment")
	}
}

type slowByteReader struct {
	value byte
}

func (r slowByteReader) Read(buffer []byte) (int, error) {
	time.Sleep(40 * time.Millisecond)
	for index := range buffer {
		buffer[index] = r.value
	}
	return len(buffer), nil
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("random reader must not be used during recovery")
}

func writeTestOverview(t *testing.T, root, projectID string) {
	t.Helper()
	ledger := filepath.Join(root, "docs", "session-review")
	if err := os.MkdirAll(ledger, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nproject_id: " + projectID + "\ncreated_at: 2026-08-22T12:00:00Z\n---\n\n# project\n"
	if err := os.WriteFile(filepath.Join(ledger, "project-overview.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
