//go:build !darwin && !windows

package sessionlaunch

func isInteractiveTerminal() bool { return false }
