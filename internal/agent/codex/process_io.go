package codex

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

var errProcessPipeDrainTimeout = errors.New("process output pipes did not close after tree termination")

// commandIO gives exec.Cmd real files rather than arbitrary Writers. Cmd.Wait
// therefore observes only the parent process and never waits for os/exec copy
// goroutines whose pipe descriptors may have been inherited by descendants.
// We own and bound those drains after the native process tree is torn down.
type commandIO struct {
	stdoutReader *os.File
	stdoutWriter *os.File
	stderrReader *os.File
	stderrWriter *os.File
	stdinReader  *os.File
	stdinWriter  *os.File
	stdin        []byte

	stdoutDone chan error
	stderrDone chan error
	stdinDone  chan struct{}
	startOnce  sync.Once
	closeOnce  sync.Once
}

func attachCommandIO(command *exec.Cmd, stdin []byte, stdout, stderr io.Writer) (*commandIO, error) {
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return nil, err
	}
	pipes := &commandIO{
		stdoutReader: stdoutReader,
		stdoutWriter: stdoutWriter,
		stderrReader: stderrReader,
		stderrWriter: stderrWriter,
		stdin:        append([]byte(nil), stdin...),
		stdoutDone:   make(chan error, 1),
		stderrDone:   make(chan error, 1),
		stdinDone:    make(chan struct{}),
	}
	if stdin != nil {
		pipes.stdinReader, pipes.stdinWriter, err = os.Pipe()
		if err != nil {
			pipes.abort()
			return nil, err
		}
		command.Stdin = pipes.stdinReader
	} else {
		close(pipes.stdinDone)
	}
	command.Stdout = stdoutWriter
	command.Stderr = stderrWriter
	go func() {
		_, copyErr := io.Copy(stdout, stdoutReader)
		closeErr := stdoutReader.Close()
		pipes.stdoutDone <- errors.Join(copyErr, closeErr)
	}()
	go func() {
		_, copyErr := io.Copy(stderr, stderrReader)
		closeErr := stderrReader.Close()
		pipes.stderrDone <- errors.Join(copyErr, closeErr)
	}()
	return pipes, nil
}

// started closes the parent's copies of child pipe ends and begins the bounded
// prompt write. It must be called exactly once after a successful process start.
func (pipes *commandIO) started() {
	if pipes == nil {
		return
	}
	pipes.startOnce.Do(func() {
		_ = pipes.stdoutWriter.Close()
		_ = pipes.stderrWriter.Close()
		if pipes.stdinReader == nil {
			return
		}
		_ = pipes.stdinReader.Close()
		go func() {
			_, _ = pipes.stdinWriter.Write(pipes.stdin)
			_ = pipes.stdinWriter.Close()
			close(pipes.stdinDone)
		}()
	})
}

func (pipes *commandIO) abort() {
	if pipes == nil {
		return
	}
	pipes.closeOnce.Do(func() {
		_ = pipes.stdoutWriter.Close()
		_ = pipes.stderrWriter.Close()
		_ = pipes.stdoutReader.Close()
		_ = pipes.stderrReader.Close()
		if pipes.stdinReader != nil {
			_ = pipes.stdinReader.Close()
		}
		if pipes.stdinWriter != nil {
			_ = pipes.stdinWriter.Close()
		}
	})
}

// finish waits for EOF only after the native tree boundary has been closed.
// If a platform violates that contract, closing our read ends keeps return
// time bounded and turns the lifecycle into a fail-closed adapter error.
func (pipes *commandIO) finish(timeout time.Duration) error {
	if pipes == nil {
		return nil
	}
	if pipes.stdinWriter != nil {
		_ = pipes.stdinWriter.Close()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var result error
	stdoutDone := pipes.stdoutDone
	stderrDone := pipes.stderrDone
	stdinDone := pipes.stdinDone
	for stdoutDone != nil || stderrDone != nil || stdinDone != nil {
		select {
		case err := <-stdoutDone:
			stdoutDone = nil
			if err != nil && !errors.Is(err, os.ErrClosed) {
				result = errors.Join(result, err)
			}
		case err := <-stderrDone:
			stderrDone = nil
			if err != nil && !errors.Is(err, os.ErrClosed) {
				result = errors.Join(result, err)
			}
		case <-stdinDone:
			stdinDone = nil
		case <-timer.C:
			pipes.abort()
			return errors.Join(result, errProcessPipeDrainTimeout)
		}
	}
	return result
}
