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
	"io"
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
	return run(ctx, args, timeout, true)
}

// RunStdout executes a command capturing stdout only (stderr discarded).
func RunStdout(ctx context.Context, args []string, timeout time.Duration) (string, int, time.Duration, error) {
	return run(ctx, args, timeout, false)
}

// run executes a command with the shared timeout, cancellation, process-group,
// trace, and exit-code classification handling. combined selects whether stderr
// is captured alongside stdout or discarded.
func run(ctx context.Context, args []string, timeout time.Duration, combined bool) (string, int, time.Duration, error) {
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
	var out bytes.Buffer
	cmd.Stdout = &out
	if combined {
		cmd.Stderr = &out
	} else {
		cmd.Stderr = io.Discard
	}
	cmd.WaitDelay = waitDelay
	configureProcessGroup(cmd)
	err := cmd.Run()
	duration := time.Since(start)
	code := 0
	if err != nil {
		code = classify(cmdCtx, ctx, cmd, err)
	}
	trace(args, start, duration, code)
	if code == 0 {
		return out.String(), 0, duration, nil
	}
	return out.String(), code, duration, err
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
