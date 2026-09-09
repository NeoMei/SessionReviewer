//go:build darwin

package sessionlaunch

import "os/exec"

func launchTerminal(script string) error {
	return exec.Command("/usr/bin/open", "-a", "Terminal", script).Run()
}
