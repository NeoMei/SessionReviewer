package modelpricewatch

import (
	"context"
	"errors"
	"os"
	"time"
)

type cacheLock struct{ file *os.File }

func acquireCacheLock(ctx context.Context, path string, timeout time.Duration) (*cacheLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		locked, err := tryCachePlatformLock(file)
		if err != nil {
			file.Close()
			return nil, err
		}
		if locked {
			return &cacheLock{file: file}, nil
		}
		if time.Now().After(deadline) {
			file.Close()
			return nil, errors.New("timed out waiting for modelpricewatch cache lock")
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func (l *cacheLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockCachePlatformLock(l.file)
	return errors.Join(err, l.file.Close())
}
