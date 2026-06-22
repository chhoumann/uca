//go:build windows

package exec

import "os/exec"

// configureProcessGroup is a no-op on Windows, where process-group signalling
// works differently and the release targets are darwin/linux. exec's default
// context cancellation (kill the child) is used as-is.
func configureProcessGroup(cmd *exec.Cmd) {}
