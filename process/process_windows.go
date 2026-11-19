//go:build windows

package process

import "os/exec"

// platformSupported is false on Windows: Run returns errUnsupported without
// starting a process. The package still compiles so cross-platform callers
// build, but there is no process-tree guarantee.
const platformSupported = false

func configureCommand(cmd *exec.Cmd) {}

func killGroup(cmd *exec.Cmd) error { return errUnsupported }

func cleanupGroup(cmd *exec.Cmd) {}
