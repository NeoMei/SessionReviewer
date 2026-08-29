package sync

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
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
	ErrConcurrentWrite  = errors.New("rooted write target changed concurrently")
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

	// beforeWrite injects test failures or namespace races after the immediate
	// parent is pinned. Returning nil cannot replace or bypass the real atomic
	// write through that pinned handle.
	beforeWrite func(Side, *os.Root, string) error

	// beforeMutation reauthenticates the higher-level namespace capability.
	// It runs after the adversarial hook, before atomic recovery/temporary
	// creation, and at every checked publication boundary.
	beforeMutation func(Side) error
}

func (writer RootedWriter) Write(ctx context.Context, side Side, relative string, content []byte, mode fs.FileMode) error {
	return writer.write(ctx, side, relative, content, mode, nil)
}

// WriteIfUnchanged publishes only while the destination still has the exact
// preimage observed by the caller. The check runs through the pinned parent
// after the test/race hook and immediately before atomic publication.
func (writer RootedWriter) WriteIfUnchanged(ctx context.Context, side Side, relative string, content []byte, mode fs.FileMode, expected []byte, expectedExists bool) error {
	expectation := &writerExpectation{content: bytes.Clone(expected), exists: expectedExists}
	return writer.write(ctx, side, relative, content, mode, expectation)
}

type writerExpectation struct {
	content []byte
	exists  bool
}

func (writer RootedWriter) write(ctx context.Context, side Side, relative string, content []byte, mode fs.FileMode, expectation *writerExpectation) error {
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
		writeErr := writer.writeAttempt(side, directory, relative, content, mode, expectation)
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

func (writer RootedWriter) writeAttempt(side Side, directory *pathguard.Directory, relative string, content []byte, mode fs.FileMode, expectation *writerExpectation) error {
	parent, parentInfo, parentRelative, leaf, err := openWriterImmediateParent(directory, relative)
	if err != nil {
		return err
	}
	defer parent.Close()
	if writer.beforeWrite != nil {
		if err := writer.beforeWrite(side, parent, leaf); err != nil {
			return err
		}
	}
	checkpoint := func() error {
		if writer.beforeMutation != nil {
			if err := writer.beforeMutation(side); err != nil {
				return err
			}
		}
		return verifyWriterParentNamespace(directory, parent, parentInfo, parentRelative)
	}
	if expectation != nil {
		if err := verifyWriterPreimage(parent, leaf, *expectation); err != nil {
			return err
		}
	}
	return atomicfile.WriteRootFileChecked(parent, leaf, content, mode, checkpoint)
}

func verifyWriterPreimage(parent *os.Root, leaf string, expected writerExpectation) error {
	info, err := parent.Lstat(leaf)
	if errors.Is(err, os.ErrNotExist) {
		if expected.exists {
			return ErrConcurrentWrite
		}
		return nil
	}
	if err != nil || !expected.exists || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return ErrConcurrentWrite
	}
	file, err := parent.Open(leaf)
	if err != nil {
		return ErrConcurrentWrite
	}
	body, readErr := io.ReadAll(io.LimitReader(file, int64(len(expected.content))+1))
	afterHandle, statErr := file.Stat()
	closeErr := file.Close()
	afterPath, pathErr := parent.Lstat(leaf)
	if readErr != nil || statErr != nil || closeErr != nil || pathErr != nil ||
		!os.SameFile(info, afterHandle) || !os.SameFile(info, afterPath) ||
		afterHandle.Size() != info.Size() || !afterHandle.ModTime().Equal(info.ModTime()) ||
		!bytes.Equal(body, expected.content) {
		return ErrConcurrentWrite
	}
	return nil
}

func openWriterImmediateParent(directory *pathguard.Directory, relative string) (*os.Root, os.FileInfo, string, string, error) {
	current, err := directory.Root.Stat(".")
	if err != nil || !os.SameFile(directory.Info(), current) || !current.IsDir() {
		return nil, nil, "", "", errors.New("rooted write directory identity changed")
	}
	parentRelative := path.Dir(relative)
	leaf := path.Base(relative)
	if parentRelative == "." {
		opened, err := directory.Root.OpenRoot(".")
		if err != nil {
			return nil, nil, "", "", errors.New("cannot pin rooted write parent")
		}
		info, err := opened.Stat(".")
		if err != nil || !os.SameFile(current, info) {
			_ = opened.Close()
			return nil, nil, "", "", errors.New("rooted write parent changed while opening")
		}
		return opened, info, parentRelative, leaf, nil
	}
	opened, before, err := directory.OpenDirectory(parentRelative)
	if err != nil {
		return nil, nil, "", "", errors.New("rooted write parent is redirected or invalid")
	}
	after, err := opened.Stat(".")
	if err != nil || !os.SameFile(before, after) || !after.IsDir() {
		_ = opened.Close()
		return nil, nil, "", "", errors.New("rooted write parent changed while opening")
	}
	return opened, before, parentRelative, leaf, nil
}

func verifyWriterParentNamespace(directory *pathguard.Directory, parent *os.Root, parentInfo os.FileInfo, parentRelative string) error {
	pinned, err := parent.Stat(".")
	if err != nil || !os.SameFile(parentInfo, pinned) || !pinned.IsDir() {
		return errors.New("rooted write parent changed during publication")
	}
	current, err := directory.Root.Stat(".")
	if err != nil || !os.SameFile(directory.Info(), current) || !current.IsDir() {
		return errors.New("rooted write directory identity changed")
	}
	if parentRelative == "." {
		if !os.SameFile(parentInfo, current) {
			return errors.New("rooted write parent namespace changed")
		}
		return nil
	}
	reopened, namespaceInfo, err := directory.OpenDirectory(parentRelative)
	if err != nil {
		return errors.New("rooted write parent namespace changed")
	}
	defer reopened.Close()
	if !os.SameFile(parentInfo, namespaceInfo) {
		return errors.New("rooted write parent namespace changed")
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
