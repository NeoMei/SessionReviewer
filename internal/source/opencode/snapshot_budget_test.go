package opencode

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

func TestSnapshotBudgetConfiguration(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int64
		valid bool
	}{
		{"", 512 << 20, true}, {"2048", 2048 << 20, true}, {"4096", 4096 << 20, true},
		{"0", 0, false}, {"-1", 0, false}, {"4097", 0, false}, {"1.5", 0, false}, {" 512 ", 0, false},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("SESSION_REVIEWER_OPENCODE_SNAPSHOT_MIB", test.value)
			got, err := snapshotByteLimit()
			valid := test.valid && uint64(test.want) <= uint64(^uint(0)>>1)
			if (err == nil) != valid || valid && got != test.want {
				t.Fatalf("limit=%d err=%v", got, err)
			}
		})
	}
}

func TestSnapshotConfiguredBudgetAppliesToCapture(t *testing.T) {
	path, _ := snapshotWALFixture(t)
	f, err := os.OpenFile(path+"-wal", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(2 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Setenv("SESSION_REVIEWER_OPENCODE_SNAPSHOT_MIB", "1")
	called := false
	err = withSnapshot(context.Background(), path, func(*sql.DB) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("budget ignored: called=%v err=%v", called, err)
	}
}

func TestSnapshotInvalidBudgetDoesNotRunCallback(t *testing.T) {
	path, _ := snapshotWALFixture(t)
	t.Setenv("SESSION_REVIEWER_OPENCODE_SNAPSHOT_MIB", "invalid")
	called := false
	err := withSnapshot(context.Background(), path, func(*sql.DB) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("invalid budget accepted: called=%v err=%v", called, err)
	}
}
