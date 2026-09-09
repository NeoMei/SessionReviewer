//go:build !windows

package sessionlaunch

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
)

func probeVerifiedExecutable(ctx context.Context, provider, executable string) (string, Configuration, error) {
	physical, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", Configuration{}, err
	}
	measured, err := MeasureConfiguration(provider, "pending", physical)
	if err != nil {
		return "", Configuration{}, err
	}
	var output boundedVersionBuffer
	command := exec.CommandContext(ctx, physical, "--version")
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		return "", Configuration{}, errors.New("launcher version verification failed")
	}
	version := strings.TrimSpace(output.String())
	if version == "" || output.overflow {
		return "", Configuration{}, errors.New("launcher version response is invalid")
	}
	after, err := MeasureConfiguration(provider, "pending", physical)
	if err != nil || after != measured {
		return "", Configuration{}, errors.New("launcher executable changed during verification")
	}
	return version, measured, nil
}

type boundedVersionBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedVersionBuffer) Write(value []byte) (int, error) {
	total, remain := len(value), 4096-b.Len()
	if remain < 0 {
		remain = 0
	}
	if len(value) > remain {
		b.overflow = true
		value = value[:remain]
	}
	_, err := b.Buffer.Write(value)
	return total, err
}
