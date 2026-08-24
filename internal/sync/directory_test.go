package sync

import (
	"os"
	"testing"
)

func TestReadBoundedSyncStateEntriesRejectsOverflow(t *testing.T) {
	path := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(path+string(os.PathSeparator)+name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := readBoundedSyncStateEntries(root, 1, "test state"); err == nil {
		t.Fatal("sync state directory overflow accepted")
	}
}
