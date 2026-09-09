//go:build windows

package sessionlaunch

import (
	"context"
	"errors"
)

func probeVerifiedExecutable(context.Context, string, string) (string, Configuration, error) {
	return "", Configuration{}, errors.New("native Session launch is unsupported on Windows")
}
