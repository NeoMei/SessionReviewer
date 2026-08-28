package reviewjob

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanupPrivatePayloadsDeletesOnlyAuthenticatedExactPayloadsAndAtomicTemps(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	packetBody := []byte(`{"packet":"exact"}`)
	proposalBody := []byte(`{"proposal":"exact"}`)
	atomicTemp := ".session-reviewer-" + strings.Repeat("a", 32)
	lookalike := ".session-reviewer-" + strings.Repeat("b", 31)
	for name, body := range map[string][]byte{
		packetWorkName:   packetBody,
		proposalWorkName: proposalBody,
		atomicTemp:       []byte("bounded temporary bytes"),
		lookalike:        []byte("must remain"),
		"unrelated.txt":  []byte("must remain"),
	} {
		if err := os.WriteFile(filepath.Join(directory, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	job := Job{PacketDigest: digestPrivate(packetBody), ResultDigest: digestPrivate(proposalBody)}
	if err := cleanupPrivatePayloads(root, job); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{packetWorkName, proposalWorkName, atomicTemp} {
		if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Fatalf("authenticated cleanup retained %q: %v", name, err)
		}
	}
	for _, name := range []string{lookalike, "unrelated.txt"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("bounded cleanup removed unrelated %q: %v", name, err)
		}
	}
}

func TestCleanupPrivatePayloadsRetainsTamperedOrNonregularNames(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"packet":"original"}`)
	if err := os.WriteFile(filepath.Join(directory, packetWorkName), []byte(`{"packet":"tampered"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	unsafeTemp := ".session-reviewer-" + strings.Repeat("c", 32)
	if err := os.Mkdir(filepath.Join(directory, unsafeTemp), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := cleanupPrivatePayloads(root, Job{PacketDigest: digestPrivate(original)}); err == nil {
		t.Fatal("cleanup accepted tampered payload and a nonregular atomic temporary name")
	}
	for _, name := range []string{packetWorkName, unsafeTemp} {
		if _, err := os.Lstat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("unsafe cleanup target %q was removed: %v", name, err)
		}
	}
}
