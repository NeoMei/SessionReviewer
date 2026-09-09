//go:build windows

package decisions

import "os"

func terminateExtractionProcess(process *os.Process) error { return process.Kill() }
