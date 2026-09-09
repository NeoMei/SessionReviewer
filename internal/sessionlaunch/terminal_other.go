//go:build !darwin

package sessionlaunch

import "errors"

func launchTerminal(string) error {
	return errors.New("native Session launch is unsupported on this operating system")
}
