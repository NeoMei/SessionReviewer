//go:build windows

package sessionlaunch

import "errors"

func Execute(ExecPlan) error { return errors.New("native Session launch is unsupported on Windows") }
