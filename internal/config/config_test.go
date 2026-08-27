package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/platform"
	"github.com/pelletier/go-toml/v2"
)

func TestProjectFragmentPublishMergeAndSavePreserveSharedBytes(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	legacy := ProjectMapping{ID: "project-1111111111111111", Root: "/legacy", VaultRoot: "/legacy-vault"}
	shared := []byte("# user formatting must survive init\nversion = 1\n\n[[projects]]\nid = '" + legacy.ID + "'\nroot = '" + legacy.Root + "'\nvault_root = '" + legacy.VaultRoot + "'\n")
	if err := os.WriteFile(configPath, shared, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	fragment := ProjectMapping{
		ID: "project-2a2a2a2a2a2a2a2a", Root: "/fragment", VaultRoot: "/fragment-vault",
		VaultReviewPath: "Projects/fragment--2a2a2a2a/Session Review", VaultCaseMode: platform.CaseSensitive,
	}
	created, err := PublishProjectFragmentRoot(root, fragment, nil)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	got, err := Load(configPath)
	if err != nil || len(got.Projects) != 2 || !reflect.DeepEqual(got.Projects[1], fragment) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	toSave := Config{Version: 1, Projects: []ProjectMapping{fragment, legacy}}
	beforeSave := Config{Version: 1, Projects: append([]ProjectMapping(nil), toSave.Projects...)}
	if err := Save(configPath, toSave); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(toSave, beforeSave) {
		t.Fatalf("Save mutated caller config: got=%+v want=%+v", toSave, beforeSave)
	}
	if after, err := os.ReadFile(filepath.Join(dir, ProjectFragmentsDir, fragment.ID+".toml")); err != nil || len(after) == 0 {
		t.Fatalf("fragment missing after Save: body=%q err=%v", after, err)
	}
	roundTrip, err := Load(configPath)
	if err != nil || len(roundTrip.Projects) != 2 || !reflect.DeepEqual(roundTrip.Projects[1], fragment) {
		t.Fatalf("roundTrip=%+v err=%v", roundTrip, err)
	}
	rawShared, err := os.ReadFile(configPath)
	if err != nil || bytes.Contains(rawShared, []byte(fragment.ID)) {
		t.Fatalf("Save copied fragment into shared config: %q err=%v", rawShared, err)
	}
}

func TestProjectFragmentPublishIsIdempotentAndNeverReplaces(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	mapping := ProjectMapping{
		ID: "project-2a2a2a2a2a2a2a2a", Root: "/one", VaultRoot: "/vault",
		VaultReviewPath: "Projects/one--2a2a2a2a/Session Review", VaultCaseMode: platform.CaseSensitive,
	}
	created, err := PublishProjectFragmentRoot(root, mapping, nil)
	if err != nil || !created {
		t.Fatalf("first created=%v err=%v", created, err)
	}
	fragmentPath := filepath.Join(dir, ProjectFragmentsDir, mapping.ID+".toml")
	before, err := os.ReadFile(fragmentPath)
	if err != nil {
		t.Fatal(err)
	}
	created, err = PublishProjectFragmentRoot(root, mapping, nil)
	if err != nil || created {
		t.Fatalf("repeat created=%v err=%v", created, err)
	}
	conflict := mapping
	conflict.Root = "/different"
	created, err = PublishProjectFragmentRoot(root, conflict, nil)
	if err == nil || created || !errors.Is(err, ErrProjectFragmentConflict) {
		t.Fatalf("conflict created=%v err=%v", created, err)
	}
	after, readErr := os.ReadFile(fragmentPath)
	if readErr != nil || !bytes.Equal(after, before) {
		t.Fatalf("fragment replaced: before=%q after=%q err=%v", before, after, readErr)
	}
}

func TestProjectFragmentStrictValidationAndLegacyCompletion(t *testing.T) {
	dir := t.TempDir()
	id := "project-2a2a2a2a2a2a2a2a"
	legacy := ProjectMapping{ID: id, Root: "/work", VaultRoot: "/vault"}
	if err := Save(filepath.Join(dir, "config.toml"), Config{Version: 1, Projects: []ProjectMapping{legacy}}); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	completed := legacy
	completed.VaultReviewPath = "Projects/work--2a2a2a2a/Session Review"
	completed.VaultCaseMode = platform.CaseSensitive
	if _, err := PublishProjectFragmentRoot(root, completed, nil); err != nil {
		t.Fatal(err)
	}
	got, err := Load(filepath.Join(dir, "config.toml"))
	if err != nil || len(got.Projects) != 1 || !reflect.DeepEqual(got.Projects[0], completed) {
		t.Fatalf("completed=%+v err=%v", got, err)
	}

	fragmentPath := filepath.Join(dir, ProjectFragmentsDir, id+".toml")
	body, err := os.ReadFile(fragmentPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fragmentPath, append(body, []byte("unknown = true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(dir, "config.toml")); err == nil {
		t.Fatal("fragment with unknown field was accepted")
	}
}

func TestProjectFragmentRejectsFilenameIdentityAndLegacyCollisions(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	mapping := ProjectMapping{
		ID: "project-2a2a2a2a2a2a2a2a", Root: "/work", VaultRoot: "/vault",
		VaultReviewPath: "Projects/work--2a2a2a2a/Session Review", VaultCaseMode: platform.CaseSensitive,
	}
	if _, err := PublishProjectFragmentRoot(root, mapping, nil); err != nil {
		t.Fatal(err)
	}
	fragmentPath := filepath.Join(dir, ProjectFragmentsDir, mapping.ID+".toml")
	body, err := os.ReadFile(fragmentPath)
	if err != nil {
		t.Fatal(err)
	}
	wrong := filepath.Join(dir, ProjectFragmentsDir, "project-3333333333333333.toml")
	if err := os.WriteFile(wrong, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(dir, "config.toml")); err == nil {
		t.Fatal("filename/payload identity mismatch was accepted")
	}
	if err := os.Remove(wrong); err != nil {
		t.Fatal(err)
	}
	conflictingLegacy := Config{Version: 1, Projects: []ProjectMapping{{ID: mapping.ID, Root: "/other", VaultRoot: mapping.VaultRoot}}}
	if err := Save(filepath.Join(t.TempDir(), "config.toml"), conflictingLegacy); err != nil {
		t.Fatal(err)
	}
	shared, err := toml.Marshal(conflictingLegacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), shared, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(dir, "config.toml")); err == nil {
		t.Fatal("legacy/fragment root collision was accepted")
	}
}

func TestConfigRejectsPortableProjectAndVaultDestinationCollisions(t *testing.T) {
	tests := []struct {
		name   string
		first  ProjectMapping
		second ProjectMapping
	}{
		{
			name: "same project root",
			first: ProjectMapping{ID: "project-1111111111111111", Root: "/work/same", VaultRoot: "/vault/one",
				VaultReviewPath: "Projects/one--11111111/Session Review", VaultCaseMode: platform.CaseSensitive},
			second: ProjectMapping{ID: "project-2222222222222222", Root: "/work/same", VaultRoot: "/vault/two",
				VaultReviewPath: "Projects/two--22222222/Session Review", VaultCaseMode: platform.CaseSensitive},
		},
		{
			name: "exact vault target",
			first: ProjectMapping{ID: "project-1111111111111111", Root: "/work/one", VaultRoot: "/vault",
				VaultReviewPath: "Projects/shared/Session Review", VaultCaseMode: platform.CaseSensitive},
			second: ProjectMapping{ID: "project-2222222222222222", Root: "/work/two", VaultRoot: "/vault",
				VaultReviewPath: "Projects/shared/Session Review", VaultCaseMode: platform.CaseSensitive},
		},
		{
			name: "case-only insensitive vault target",
			first: ProjectMapping{ID: "project-1111111111111111", Root: "/work/one", VaultRoot: "/Vault",
				VaultReviewPath: "Projects/Shared/Session Review", VaultCaseMode: platform.CaseInsensitive},
			second: ProjectMapping{ID: "project-2222222222222222", Root: "/work/two", VaultRoot: "/vault",
				VaultReviewPath: "Projects/shared/Session Review", VaultCaseMode: platform.CaseInsensitive},
		},
		{
			name: "nfc-equivalent vault target",
			first: ProjectMapping{ID: "project-1111111111111111", Root: "/work/one", VaultRoot: "/vault/Café",
				VaultReviewPath: "Projects/Café/Session Review", VaultCaseMode: platform.CaseSensitive},
			second: ProjectMapping{ID: "project-2222222222222222", Root: "/work/two", VaultRoot: "/vault/Café",
				VaultReviewPath: "Projects/Café/Session Review", VaultCaseMode: platform.CaseSensitive},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			err := Save(path, Config{Version: 1, Projects: []ProjectMapping{test.first, test.second}})
			if err == nil || !strings.Contains(err.Error(), "mapped more than once") {
				t.Fatalf("collision accepted: err=%v", err)
			}
		})
	}
}

func TestProjectFragmentPublishRejectsLegacyAndFragmentVaultCollision(t *testing.T) {
	dir := t.TempDir()
	legacy := ProjectMapping{ID: "project-1111111111111111", Root: "/work/one", VaultRoot: "/Vault",
		VaultReviewPath: "Projects/Shared/Session Review", VaultCaseMode: platform.CaseInsensitive}
	if err := Save(filepath.Join(dir, "config.toml"), Config{Version: 1, Projects: []ProjectMapping{legacy}}); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	fragment := ProjectMapping{ID: "project-2222222222222222", Root: "/work/two", VaultRoot: "/vault",
		VaultReviewPath: "Projects/shared/Session Review", VaultCaseMode: platform.CaseInsensitive}
	created, err := PublishProjectFragmentRoot(root, fragment, nil)
	if err == nil || created {
		t.Fatalf("vault collision published: created=%v err=%v", created, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ProjectFragmentsDir, fragment.ID+".toml")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("colliding fragment exists: %v", statErr)
	}
}

func TestLoadRejectsTwoFragmentsWithNFCEquivalentVaultDestination(t *testing.T) {
	dir := t.TempDir()
	firstDir := t.TempDir()
	firstRoot, err := os.OpenRoot(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	first := ProjectMapping{ID: "project-1111111111111111", Root: "/work/one", VaultRoot: "/vault/Café",
		VaultReviewPath: "Projects/Café/Session Review", VaultCaseMode: platform.CaseSensitive}
	if _, err := PublishProjectFragmentRoot(firstRoot, first, nil); err != nil {
		t.Fatal(err)
	}
	if err := firstRoot.Close(); err != nil {
		t.Fatal(err)
	}
	secondDir := t.TempDir()
	secondRoot, err := os.OpenRoot(secondDir)
	if err != nil {
		t.Fatal(err)
	}
	second := ProjectMapping{ID: "project-2222222222222222", Root: "/work/two", VaultRoot: "/vault/Café",
		VaultReviewPath: "Projects/Café/Session Review", VaultCaseMode: platform.CaseSensitive}
	if _, err := PublishProjectFragmentRoot(secondRoot, second, nil); err != nil {
		t.Fatal(err)
	}
	if err := secondRoot.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ProjectFragmentsDir), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{
		filepath.Join(firstDir, ProjectFragmentsDir, first.ID+".toml"),
		filepath.Join(secondDir, ProjectFragmentsDir, second.ID+".toml"),
	} {
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ProjectFragmentsDir, filepath.Base(source)), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Load(filepath.Join(dir, "config.toml")); err == nil {
		t.Fatal("NFC-equivalent fragment vault destinations were accepted")
	}
}

func TestProjectFragmentCommitRejectsLiveRootAndDirectoryChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, data, moved string)
	}{
		{name: "config root replacement", mutate: func(t *testing.T, data, moved string) {
			if err := os.Rename(data, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(data, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fragments directory replacement", mutate: func(t *testing.T, data, moved string) {
			fragments := filepath.Join(data, ProjectFragmentsDir)
			if err := os.Rename(fragments, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(fragments, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fragments directory mode broadened", mutate: func(t *testing.T, data, _ string) {
			if runtime.GOOS == "windows" {
				t.Skip("Windows privacy is verified by the native ACL test")
			}
			if err := os.Chmod(filepath.Join(data, ProjectFragmentsDir), 0o777); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			data := filepath.Join(parent, "data")
			moved := filepath.Join(parent, "moved")
			if err := os.Mkdir(data, 0o700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(data)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			mapping := ProjectMapping{ID: "project-2a2a2a2a2a2a2a2a", Root: "/work", VaultRoot: "/vault",
				VaultReviewPath: "Projects/work--2a2a2a2a/Session Review", VaultCaseMode: platform.CaseSensitive}
			created, err := PublishProjectFragmentRoot(root, mapping, func() error {
				test.mutate(t, data, moved)
				return nil
			})
			if err == nil || created {
				t.Fatalf("changed live directory committed: created=%v err=%v", created, err)
			}
			for _, base := range []string{data, moved} {
				if _, statErr := os.Stat(filepath.Join(base, ProjectFragmentsDir, mapping.ID+".toml")); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("fragment published below %s: %v", base, statErr)
				}
			}
		})
	}
}

func TestSaveRootUsesSelectedBackupBaseWithFragments(t *testing.T) {
	for _, invalidPrimary := range []bool{false, true} {
		name := "backup-only"
		if invalidPrimary {
			name = "invalid-primary"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			id := "project-2a2a2a2a2a2a2a2a"
			legacy := ProjectMapping{ID: id, Root: "/work", VaultRoot: "/vault"}
			association := SessionAssociation{SessionID: "session-one", ProjectID: id}
			if err := Save(path, Config{Version: 1, Projects: []ProjectMapping{legacy}, SessionAssociations: []SessionAssociation{association}}); err != nil {
				t.Fatal(err)
			}
			backupPath := atomicfile.BackupPath(path)
			if err := os.Rename(path, backupPath); err != nil {
				t.Fatal(err)
			}
			if invalidPrimary {
				if err := os.WriteFile(path, []byte("invalid = ["), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			completed := legacy
			completed.VaultReviewPath = "Projects/work--2a2a2a2a/Session Review"
			completed.VaultCaseMode = platform.CaseSensitive
			if _, err := PublishProjectFragmentRoot(root, completed, nil); err != nil {
				t.Fatal(err)
			}
			fragmentPath := filepath.Join(dir, ProjectFragmentsDir, id+".toml")
			fragmentBefore, err := os.ReadFile(fragmentPath)
			if err != nil {
				t.Fatal(err)
			}
			merged, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			callerBefore := Config{Version: merged.Version, Projects: append([]ProjectMapping(nil), merged.Projects...), SessionAssociations: append([]SessionAssociation(nil), merged.SessionAssociations...)}
			if err := Save(path, merged); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(merged, callerBefore) {
				t.Fatalf("Save mutated caller: got=%+v want=%+v", merged, callerBefore)
			}
			fragmentAfter, err := os.ReadFile(fragmentPath)
			if err != nil || !bytes.Equal(fragmentBefore, fragmentAfter) {
				t.Fatalf("fragment changed: err=%v", err)
			}
			roundTrip, err := Load(path)
			if err != nil || len(roundTrip.Projects) != 1 || !reflect.DeepEqual(roundTrip.Projects[0], completed) || !reflect.DeepEqual(roundTrip.SessionAssociations, []SessionAssociation{association}) {
				t.Fatalf("roundTrip=%+v err=%v", roundTrip, err)
			}
			if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("backup remains after converged save: %v", err)
			}
			raw, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(raw, []byte(id)) || bytes.Contains(raw, []byte("vault_review_path")) {
				t.Fatalf("selected legacy base not preserved: %q err=%v", raw, err)
			}
		})
	}
}

func TestLoadUsesValidBackupWhenPrimaryMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	want := Config{Version: 1, Projects: []ProjectMapping{{ID: "project-1111111111111111", Root: "/one", VaultRoot: "/vault-one"}, {ID: "project-2222222222222222", Root: "/two", VaultRoot: "/vault-two"}}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".session-reviewer-backup"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || len(got.Projects) != 2 || !reflect.DeepEqual(got.Projects[1], want.Projects[1]) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestLoadPrefersValidPrimaryOverBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	primary := "version = 1\n\n[[projects]]\nid = 'project-1111111111111111'\nroot = '/primary'\nvault_root = '/vault'\n"
	backup := "version = 1\n\n[[projects]]\nid = 'project-2222222222222222'\nroot = '/backup'\nvault_root = '/vault'\n"
	if err := os.WriteFile(path, []byte(primary), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".session-reviewer-backup", []byte(backup), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || len(got.Projects) != 1 || got.Projects[0].Root != "/primary" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestLoadFailsClosedForInvalidPrimaryAndBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("version = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".session-reviewer-backup", []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || strings.Contains(err.Error(), "version = 2") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadUsesValidBackupWhenPrimaryInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("version = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := "version = 1\n\n[[projects]]\nid = 'project-1111111111111111'\nroot = '/kept'\nvault_root = '/vault'\n"
	if err := os.WriteFile(path+".session-reviewer-backup", []byte(backup), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || len(got.Projects) != 1 || got.Projects[0].Root != "/kept" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestLoadMissingConfigReturnsVersionOne(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 || len(cfg.Projects) != 0 {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestSaveAndLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	want := Config{Version: 1, Projects: []ProjectMapping{{
		ID: "project-2a2a2a2a2a2a2a2a", Root: "/work/项目", VaultRoot: "/notes/知识库",
	}}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Projects) != 1 || !reflect.DeepEqual(got.Projects[0], want.Projects[0]) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if info.Mode().Perm()&0o200 == 0 {
			t.Fatalf("mode=%o, want writable", info.Mode().Perm())
		}
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o, want 600", info.Mode().Perm())
	}
}

func TestLoadRejectsNonPrivateConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX owner-only modes")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "version = 1\n\n[[projects]]\nid = 'project-1111111111111111'\nroot = '/work'\nvault_root = '/vault'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("non-private configuration accepted")
	}
}

func TestSaveFailsClosedAndPreservesUntrustedBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	backup := path + ".session-reviewer-backup"
	if err := os.WriteFile(backup, []byte("untrusted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(backup, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(path, Config{Version: 1, Projects: []ProjectMapping{{ID: "project-1111111111111111", Root: "/work", VaultRoot: "/vault"}}}); err == nil || !strings.Contains(err.Error(), "recovery backup") {
		t.Fatalf("err=%v", err)
	}
	if got, readErr := os.ReadFile(backup); readErr != nil || string(got) != "untrusted" {
		t.Fatalf("untrusted backup was changed: body=%q err=%v", got, readErr)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("primary was unexpectedly written: %v", statErr)
	}
}

func TestSaveCleansOnlyByteIdenticalStaleBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	old := Config{Version: 1, Projects: []ProjectMapping{{ID: "project-1111111111111111", Root: "/old", VaultRoot: "/vault"}}}
	if err := Save(path, old); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backup := path + ".session-reviewer-backup"
	if err := os.WriteFile(backup, body, 0o600); err != nil {
		t.Fatal(err)
	}
	next := Config{Version: 1, Projects: []ProjectMapping{{ID: "project-1111111111111111", Root: "/new", VaultRoot: "/vault"}}}
	if err := Save(path, next); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.Projects[0].Root != "/new" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("converged backup remains: %v", err)
	}
}

func TestConfigUnionRoundTripPreservesEveryField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	want := Config{
		Version: 1,
		Projects: []ProjectMapping{{
			ID:               "project-2a2a2a2a2a2a2a2a",
			Root:             "/work/项目",
			VaultRoot:        "/notes/知识库",
			VaultReviewPath:  "Projects/项目--2a2a2a2a/Session Review",
			VaultCaseMode:    platform.CaseInsensitive,
			RemoteIdentities: []string{"origin:https://example.invalid/repo.git"},
			CommonDirs:       []string{"/work/common"},
			Aliases:          []string{"reviewer", "会话审查"},
		}},
		SessionAssociations: []SessionAssociation{{SessionID: "session-1", ProjectID: "project-2a2a2a2a2a2a2a2a"}},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestConfigAllowsLegacyMappingOnlyWhenBothVaultFieldsAreEmpty(t *testing.T) {
	legacy := Config{Version: 1, Projects: []ProjectMapping{{ID: "project-1111111111111111", Root: "/work", VaultRoot: "/vault"}}}
	if err := Save(filepath.Join(t.TempDir(), "config.toml"), legacy); err != nil {
		t.Fatal(err)
	}
	tests := []ProjectMapping{
		{ID: "project-1111111111111111", Root: "/work", VaultRoot: "/vault", VaultReviewPath: "Projects/work--11111111/Session Review"},
		{ID: "project-1111111111111111", Root: "/work", VaultRoot: "/vault", VaultCaseMode: platform.CaseSensitive},
	}
	for _, mapping := range tests {
		if err := Save(filepath.Join(t.TempDir(), "config.toml"), Config{Version: 1, Projects: []ProjectMapping{mapping}}); err == nil {
			t.Fatalf("accepted partial mapping: %+v", mapping)
		}
	}
}

func TestConfigRejectsUnsafeVaultReviewMapping(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		caseMode platform.CaseMode
	}{
		{name: "unknown case mode", path: "Projects/work--11111111/Session Review", caseMode: "unknown"},
		{name: "not below projects", path: "Elsewhere/work--11111111/Session Review", caseMode: platform.CaseSensitive},
		{name: "projects root only", path: "Projects/Session Review", caseMode: platform.CaseSensitive},
		{name: "wrong leaf", path: "Projects/work--11111111/Review", caseMode: platform.CaseSensitive},
		{name: "backslash", path: `Projects\work--11111111\Session Review`, caseMode: platform.CaseSensitive},
		{name: "dot", path: "Projects/./work--11111111/Session Review", caseMode: platform.CaseSensitive},
		{name: "dotdot", path: "Projects/work--11111111/../Session Review", caseMode: platform.CaseSensitive},
		{name: "absolute", path: "/Projects/work--11111111/Session Review", caseMode: platform.CaseSensitive},
		{name: "drive", path: "C:/Projects/work--11111111/Session Review", caseMode: platform.CaseSensitive},
		{name: "UNC", path: "//server/share/Projects/work--11111111/Session Review", caseMode: platform.CaseSensitive},
		{name: "device", path: `\\?\C:\Projects\work--11111111\Session Review`, caseMode: platform.CaseSensitive},
		{name: "reserved", path: "Projects/CON/Session Review", caseMode: platform.CaseSensitive},
		{name: "trailing dot", path: "Projects/work./Session Review", caseMode: platform.CaseSensitive},
		{name: "trailing space", path: "Projects/work /Session Review", caseMode: platform.CaseSensitive},
		{name: "NUL", path: "Projects/work\x00name/Session Review", caseMode: platform.CaseSensitive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapping := ProjectMapping{
				ID: "project-1111111111111111", Root: "/work", VaultRoot: "/vault",
				VaultReviewPath: test.path, VaultCaseMode: test.caseMode,
			}
			if err := Save(filepath.Join(t.TempDir(), "config.toml"), Config{Version: 1, Projects: []ProjectMapping{mapping}}); err == nil {
				t.Fatalf("accepted unsafe mapping path %q", test.path)
			}
		})
	}
}

func TestLoadRejectsUnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("version = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unsupported version error")
	}
}

func TestFindProjectUsesTargetWindowsPathSemantics(t *testing.T) {
	cfg := Config{Version: 1, Projects: []ProjectMapping{{
		ID: "project-1", Root: `C:\Projects\SessionReviewer`, VaultRoot: `D:\Vault`,
	}}}
	got, ok := cfg.FindProject("windows", `c:/projects/./sessionreviewer`)
	if !ok || got.ID != "project-1" {
		t.Fatalf("got=%+v ok=%v", got, ok)
	}
}

func TestConfigRejectsDuplicateProjectIDAcrossRoots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	err := Save(path, Config{Version: 1, Projects: []ProjectMapping{
		{ID: "project-1111111111111111", Root: "/work/one", VaultRoot: "/vault/one"},
		{ID: "project-1111111111111111", Root: "/work/two", VaultRoot: "/vault/two"},
	}})
	if err == nil || !strings.Contains(err.Error(), "project ID is mapped more than once") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadRejectsDuplicateProjectIDAcrossRoots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "version = 1\n\n[[projects]]\nid = 'project-1111111111111111'\nroot = '/one'\nvault_root = '/v1'\n\n[[projects]]\nid = 'project-1111111111111111'\nroot = '/two'\nvault_root = '/v2'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "configuration state and recovery backup are invalid") {
		t.Fatalf("err=%v", err)
	}
}
