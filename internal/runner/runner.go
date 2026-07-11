// Package runner runs external commands with consistent timeout, cancellation, and
// process-group handling. Run captures combined stdout+stderr; RunStdout captures
// stdout only (detection parsers depend on a banner-free stdout). On
// timeout/cancellation the whole process group is killed (see proc_unix.go) and
// the result is classified as ExitCodeTimeout/ExitCodeCanceled.
package runner

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

const (
	// ExitCodeTimeout / ExitCodeCanceled are synthetic exit codes the runner
	// returns when a command is killed by its deadline or by user cancellation.
	ExitCodeTimeout  = 124
	ExitCodeCanceled = 130
)

// waitDelay bounds how long Wait blocks after a command's context is canceled
// (timeout/SIGINT) or after the process exits while a child still holds an output
// pipe open. Without it, a single orphaned grandchild (e.g. a `sleep` spawned by
// an update script) keeps the pipe open and makes --timeout and Ctrl-C
// effectively non-responsive.
const waitDelay = 5 * time.Second

// Run executes a command capturing combined stdout+stderr.
func Run(ctx context.Context, args []string, timeout time.Duration) (string, int, time.Duration, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	cmdCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, args[0], args[1:]...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.Stdin = nil
	cmd.WaitDelay = waitDelay
	configureProcessGroup(cmd)
	err := cmd.Run()
	duration := time.Since(start)
	if err == nil {
		trace(args, start, duration, 0)
		return buf.String(), 0, duration, nil
	}
	code := classify(cmdCtx, ctx, cmd, err)
	trace(args, start, duration, code)
	if code == 0 {
		return buf.String(), 0, duration, nil
	}
	return buf.String(), code, duration, err
}

// RunStdout executes a command capturing stdout only (stderr discarded).
func RunStdout(ctx context.Context, args []string, timeout time.Duration) (string, int, time.Duration, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	cmdCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, args[0], args[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = waitDelay
	configureProcessGroup(cmd)
	out, err := cmd.Output()
	duration := time.Since(start)
	if err == nil {
		trace(args, start, duration, 0)
		return string(out), 0, duration, nil
	}
	code := classify(cmdCtx, ctx, cmd, err)
	trace(args, start, duration, code)
	if code == 0 {
		return string(out), 0, duration, nil
	}
	return string(out), code, duration, err
}

// classify maps a finished command's error to an exit code, preferring the
// context state (a SIGKILL'd child surfaces as an ExitError, not a context
// error). Returns 0 when the command effectively succeeded (e.g. a WaitDelay
// expiry after a clean exit).
func classify(cmdCtx, ctx context.Context, cmd *exec.Cmd, err error) int {
	if cmdCtx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
		return ExitCodeTimeout
	}
	if ctx.Err() == context.Canceled || errors.Is(err, context.Canceled) {
		return ExitCodeCanceled
	}
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0 {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}
