package sync

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// A scan admits up to 4096 entities. Durable state may temporarily contain a
// primary and rollback backup for both project/vault queue targets, plus one
// writer temporary and one lock. Keep that legitimate recovery envelope
// bounded without making the entity limit unreachable.
const maxSyncStateDirectoryEntries = 2*2*4_096 + 2

func readBoundedSyncStateEntries(root *os.Root, limit int, label string) ([]fs.DirEntry, error) {
	if root == nil || limit <= 0 {
		return nil, errors.New("invalid sync state directory budget")
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("cannot inspect %s", label)
	}
	entries, readErr := directory.ReadDir(limit + 1)
	closeErr := directory.Close()
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("cannot inspect %s", label)
	}
	if len(entries) > limit {
		return nil, fmt.Errorf("%s exceeds entry limit", label)
	}
	return entries, nil
}
