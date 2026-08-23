package sync

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/neomei/SessionReviewer/internal/pathguard"
)

type fakeRetryClock struct {
	sleeps []time.Duration
	err    error
}

func (clock *fakeRetryClock) Sleep(_ context.Context, delay time.Duration) error {
	clock.sleeps = append(clock.sleeps, delay)
	return clock.err
}

func TestRootedWriterRetriesSharingViolationThenSucceeds(t *testing.T) {
	project, vault := openWriterRoots(t)
	clock := &fakeRetryClock{}
	attempts := 0
	writer := RootedWriter{
		Project: project,
		Vault:   vault,
		Retry:   RetryPolicy{Initial: 10 * time.Millisecond, Max: 40 * time.Millisecond, InlineAttempts: 4, QueueAttempts: 8},
		Sleep:   clock.Sleep,
		write: func(side Side, relative string, content []byte, mode fs.FileMode) error {
			attempts++
			if side != SideVault || relative != "decisions/d1.md" || !bytes.Equal(content, []byte("safe")) || mode != 0o644 {
				t.Fatalf("write side=%q path=%q content=%q mode=%o", side, relative, content, mode)
			}
			if attempts < 4 {
				return ErrSharingViolation
			}
			return nil
		},
	}
	if err := writer.Write(context.Background(), SideVault, "decisions/d1.md", []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if attempts != 4 || !reflect.DeepEqual(clock.sleeps, []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}) {
		t.Fatalf("attempts=%d sleeps=%v", attempts, clock.sleeps)
	}
}

func TestRootedWriterReturnsTypedFinalTransientFailureWithoutContent(t *testing.T) {
	project, vault := openWriterRoots(t)
	clock := &fakeRetryClock{}
	attempts := 0
	writer := RootedWriter{
		Project: project, Vault: vault,
		Retry: RetryPolicy{Initial: time.Millisecond, Max: 2 * time.Millisecond, InlineAttempts: 3, QueueAttempts: 4},
		Sleep: clock.Sleep,
		write: func(Side, string, []byte, fs.FileMode) error {
			attempts++
			return errors.Join(ErrLockViolation, errors.New("CANARY-CONTENT"))
		},
	}
	err := writer.Write(context.Background(), SideProject, "decisions/d1.md", []byte("CANARY-CONTENT"), 0o600)
	if !errors.Is(err, ErrTransientWrite) || bytes.Contains([]byte(err.Error()), []byte("CANARY-CONTENT")) {
		t.Fatalf("error=%v", err)
	}
	if attempts != 3 || !reflect.DeepEqual(clock.sleeps, []time.Duration{time.Millisecond, 2 * time.Millisecond}) {
		t.Fatalf("attempts=%d sleeps=%v", attempts, clock.sleeps)
	}
}

func TestRootedWriterDoesNotRetryPermanentFailures(t *testing.T) {
	project, vault := openWriterRoots(t)
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "permission", err: os.ErrPermission},
		{name: "disk full", err: syscall.ENOSPC},
		{name: "invalid name", err: syscall.EINVAL},
		{name: "path", err: os.ErrNotExist},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := &fakeRetryClock{}
			attempts := 0
			writer := RootedWriter{
				Project: project, Vault: vault, Retry: DefaultRetryPolicy(), Sleep: clock.Sleep,
				write: func(Side, string, []byte, fs.FileMode) error { attempts++; return test.err },
			}
			err := writer.Write(context.Background(), SideProject, "decisions/d1.md", nil, 0o644)
			if !errors.Is(err, test.err) || errors.Is(err, ErrTransientWrite) {
				t.Fatalf("error=%v", err)
			}
			if attempts != 1 || len(clock.sleeps) != 0 {
				t.Fatalf("attempts=%d sleeps=%v", attempts, clock.sleeps)
			}
		})
	}
}

func TestRootedWriterHonorsContextCancellationDuringBackoff(t *testing.T) {
	project, vault := openWriterRoots(t)
	clock := &fakeRetryClock{err: context.Canceled}
	attempts := 0
	writer := RootedWriter{
		Project: project, Vault: vault,
		Retry: RetryPolicy{Initial: 3 * time.Millisecond, Max: 10 * time.Millisecond, InlineAttempts: 4, QueueAttempts: 8},
		Sleep: clock.Sleep,
		write: func(Side, string, []byte, fs.FileMode) error { attempts++; return ErrSharingViolation },
	}
	err := writer.Write(context.Background(), SideVault, "decisions/d1.md", nil, 0o644)
	if !errors.Is(err, context.Canceled) || attempts != 1 || !reflect.DeepEqual(clock.sleeps, []time.Duration{3 * time.Millisecond}) {
		t.Fatalf("error=%v attempts=%d sleeps=%v", err, attempts, clock.sleeps)
	}
}

func TestRootedWriterSanitizesNonContextSleepFailure(t *testing.T) {
	project, vault := openWriterRoots(t)
	canaryErr := errors.New("CANARY-CONTENT")
	clock := &fakeRetryClock{err: canaryErr}
	writer := RootedWriter{
		Project: project, Vault: vault,
		Retry: RetryPolicy{Initial: time.Millisecond, Max: time.Millisecond, InlineAttempts: 2, QueueAttempts: 2},
		Sleep: clock.Sleep,
		write: func(Side, string, []byte, fs.FileMode) error { return ErrSharingViolation },
	}
	err := writer.Write(context.Background(), SideProject, "decisions/d1.md", []byte("CANARY-CONTENT"), 0o644)
	if !errors.Is(err, canaryErr) || strings.Contains(err.Error(), "CANARY-CONTENT") {
		t.Fatalf("error=%v", err)
	}
}

func TestRootedWriterRejectsInvalidInputsBeforeWriteHook(t *testing.T) {
	project, vault := openWriterRoots(t)
	tests := []struct {
		name     string
		writer   RootedWriter
		side     Side
		relative string
		mode     fs.FileMode
	}{
		{name: "nil project", writer: RootedWriter{Vault: vault, Retry: DefaultRetryPolicy()}, side: SideProject, relative: "decisions/d1.md", mode: 0o644},
		{name: "nil vault", writer: RootedWriter{Project: project, Retry: DefaultRetryPolicy()}, side: SideVault, relative: "decisions/d1.md", mode: 0o644},
		{name: "invalid side", writer: RootedWriter{Project: project, Vault: vault, Retry: DefaultRetryPolicy()}, side: Side("other"), relative: "decisions/d1.md", mode: 0o644},
		{name: "absolute path", writer: RootedWriter{Project: project, Vault: vault, Retry: DefaultRetryPolicy()}, side: SideProject, relative: "/tmp/d1.md", mode: 0o644},
		{name: "parent path", writer: RootedWriter{Project: project, Vault: vault, Retry: DefaultRetryPolicy()}, side: SideProject, relative: "../d1.md", mode: 0o644},
		{name: "unsafe mode", writer: RootedWriter{Project: project, Vault: vault, Retry: DefaultRetryPolicy()}, side: SideProject, relative: "decisions/d1.md", mode: 0o666},
		{name: "invalid retry", writer: RootedWriter{Project: project, Vault: vault, Retry: RetryPolicy{Initial: 0, Max: time.Second, InlineAttempts: 1, QueueAttempts: 1}}, side: SideProject, relative: "decisions/d1.md", mode: 0o644},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := 0
			test.writer.write = func(Side, string, []byte, fs.FileMode) error { called++; return nil }
			if err := test.writer.Write(context.Background(), test.side, test.relative, nil, test.mode); err == nil {
				t.Fatal("accepted invalid input")
			}
			if called != 0 {
				t.Fatalf("write hook calls=%d", called)
			}
		})
	}
}

func TestRootedWriterRevalidatesParentBeforeEveryAttempt(t *testing.T) {
	projectPath, vaultPath := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(projectPath, "decisions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(vaultPath, "decisions"), 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := pathguard.Open(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	defer project.Close()
	vault, err := pathguard.Open(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	attempts := 0
	writer := RootedWriter{
		Project: project, Vault: vault,
		Retry: RetryPolicy{Initial: time.Millisecond, Max: time.Millisecond, InlineAttempts: 2, QueueAttempts: 2},
		Sleep: func(context.Context, time.Duration) error {
			if err := os.Rename(filepath.Join(vaultPath, "decisions"), filepath.Join(vaultPath, "moved")); err != nil {
				return err
			}
			return os.Symlink(t.TempDir(), filepath.Join(vaultPath, "decisions"))
		},
		write: func(Side, string, []byte, fs.FileMode) error { attempts++; return ErrSharingViolation },
	}
	err = writer.Write(context.Background(), SideVault, "decisions/d1.md", nil, 0o644)
	if err == nil || errors.Is(err, ErrTransientWrite) {
		t.Fatalf("error=%v", err)
	}
	if attempts != 1 {
		t.Fatalf("write attempts=%d", attempts)
	}
}

func TestRootedWriterWritesAtomicallyBelowSelectedPinnedRoot(t *testing.T) {
	project, vault := openWriterRoots(t)
	writer := RootedWriter{Project: project, Vault: vault, Retry: DefaultRetryPolicy()}
	if err := writer.Write(context.Background(), SideProject, "decisions/d1.md", []byte("accepted\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(project.Path, "decisions", "d1.md"))
	if err != nil || string(written) != "accepted\n" {
		t.Fatalf("written=%q err=%v", written, err)
	}
	info, err := os.Stat(filepath.Join(project.Path, "decisions", "d1.md"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(vault.Path, "decisions", "d1.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("vault write err=%v", err)
	}
}

func openWriterRoots(t *testing.T) (*pathguard.Directory, *pathguard.Directory) {
	t.Helper()
	projectPath, vaultPath := t.TempDir(), t.TempDir()
	for _, root := range []string{projectPath, vaultPath} {
		if err := os.Mkdir(filepath.Join(root, "decisions"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	project, err := pathguard.Open(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := pathguard.Open(vaultPath)
	if err != nil {
		_ = project.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = project.Close(); _ = vault.Close() })
	return project, vault
}
