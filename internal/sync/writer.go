package sync

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/platform"
)

var (
	ErrSharingViolation = errors.New("sharing violation")
	ErrLockViolation    = errors.New("lock violation")
	ErrTransientWrite   = errors.New("transient rooted write failure")
)

type RetryPolicy struct {
	Initial        time.Duration
	Max            time.Duration
	InlineAttempts int
	QueueAttempts  int
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		Initial:        100 * time.Millisecond,
		Max:            2 * time.Second,
		InlineAttempts: 5,
		QueueAttempts:  8,
	}
}

type RootedWriter struct {
	Project *pathguard.Directory
	Vault   *pathguard.Directory
	Retry   RetryPolicy
	Sleep   func(context.Context, time.Duration) error

	// write replaces only the final system write in package tests. Root, side,
	// relative-path, parent-identity, policy, context, and mode validation always
	// run before this hook and cannot be bypassed through it.
	write func(Side, string, []byte, fs.FileMode) error
}

func (writer RootedWriter) Write(ctx context.Context, side Side, relative string, content []byte, mode fs.FileMode) error {
	if ctx == nil {
		return errors.New("rooted write context is required")
	}
	if err := validateRetryPolicy(writer.Retry); err != nil {
		return err
	}
	directory, err := writer.directoryFor(side)
	if err != nil {
		return err
	}
	if err := validateWriterRelative(relative); err != nil {
		return err
	}
	if err := validateWriterMode(mode); err != nil {
		return err
	}
	sleep := writer.Sleep
	if sleep == nil {
		sleep = sleepWithContext
	}
	delay := writer.Retry.Initial
	for attempt := 0; attempt < writer.Retry.InlineAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateWriterParent(directory, relative); err != nil {
			return writerFailure{cause: err}
		}
		var writeErr error
		if writer.write != nil {
			writeErr = writer.write(side, relative, content, mode)
		} else {
			writeErr = atomicfile.WriteRoot(directory.Root, filepath.FromSlash(relative), content, mode)
		}
		if writeErr == nil {
			return nil
		}
		if !isTransientWrite(writeErr) {
			return writerFailure{cause: writeErr}
		}
		if attempt == writer.Retry.InlineAttempts-1 {
			return ErrTransientWrite
		}
		if err := sleep(ctx, delay); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if errors.Is(err, context.Canceled) {
				return context.Canceled
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return context.DeadlineExceeded
			}
			return writerFailure{cause: err}
		}
		delay = nextRetryDelay(delay, writer.Retry.Max)
	}
	return ErrTransientWrite
}

func (writer RootedWriter) directoryFor(side Side) (*pathguard.Directory, error) {
	var directory *pathguard.Directory
	switch side {
	case SideProject:
		directory = writer.Project
	case SideVault:
		directory = writer.Vault
	default:
		return nil, errors.New("invalid rooted write side")
	}
	if directory == nil || directory.Root == nil || directory.Info() == nil {
		return nil, errors.New("selected rooted write directory is required")
	}
	return directory, nil
}

func validateRetryPolicy(policy RetryPolicy) error {
	if policy.Initial <= 0 || policy.Max < policy.Initial || policy.InlineAttempts <= 0 || policy.QueueAttempts <= 0 {
		return errors.New("invalid rooted write retry policy")
	}
	return nil
}

func validateWriterRelative(relative string) error {
	if relative == "" || relative == "." || strings.Contains(relative, `\`) || path.Clean(relative) != relative {
		return errors.New("invalid rooted write path")
	}
	if _, err := platform.PathKey("windows", platform.CaseSensitive, relative); err != nil {
		return errors.New("invalid rooted write path")
	}
	return nil
}

func validateWriterMode(mode fs.FileMode) error {
	if mode != mode.Perm() || mode&0o700 != 0o600 || mode&0o133 != 0 {
		return errors.New("unsafe rooted write mode")
	}
	return nil
}

func validateWriterParent(directory *pathguard.Directory, relative string) error {
	current, err := directory.Root.Stat(".")
	if err != nil || !os.SameFile(directory.Info(), current) || !current.IsDir() {
		return errors.New("rooted write directory identity changed")
	}
	parent := path.Dir(relative)
	if parent == "." {
		opened, err := directory.Root.OpenRoot(".")
		if err != nil {
			return errors.New("cannot pin rooted write parent")
		}
		defer opened.Close()
		info, err := opened.Stat(".")
		if err != nil || !os.SameFile(current, info) {
			return errors.New("rooted write parent changed while opening")
		}
		return nil
	}
	opened, before, err := directory.OpenDirectory(parent)
	if err != nil {
		return errors.New("rooted write parent is redirected or invalid")
	}
	defer opened.Close()
	after, err := opened.Stat(".")
	if err != nil || !os.SameFile(before, after) || !after.IsDir() {
		return errors.New("rooted write parent changed while opening")
	}
	return nil
}

func isTransientWrite(err error) bool {
	if errors.Is(err, ErrSharingViolation) || errors.Is(err, ErrLockViolation) {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	return errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))
}

func nextRetryDelay(current, maximum time.Duration) time.Duration {
	if current >= maximum || current > maximum/2 {
		return maximum
	}
	return current * 2
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type writerFailure struct{ cause error }

func (writerFailure) Error() string { return "rooted write failed" }
func (failure writerFailure) Unwrap() error {
	return failure.cause
}
