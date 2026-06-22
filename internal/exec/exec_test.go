//go:build !windows

package exec

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesCombinedOutput(t *testing.T) {
	out, code, _, err := Run(context.Background(), []string{"sh", "-c", "echo hello; echo oops 1>&2"}, time.Second)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "oops") {
		t.Fatalf("combined output missing streams: %q", out)
	}
}

func TestRunStdoutDiscardsStderr(t *testing.T) {
	out, code, _, _ := RunStdout(context.Background(), []string{"sh", "-c", "echo out; echo err 1>&2"}, time.Second)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(out, "err") || !strings.Contains(out, "out") {
		t.Fatalf("RunStdout should capture stdout only; got %q", out)
	}
}

func TestRunExitCode(t *testing.T) {
	_, code, _, _ := Run(context.Background(), []string{"sh", "-c", "exit 7"}, time.Second)
	if code != 7 {
		t.Fatalf("code=%d, want 7", code)
	}
}

func TestRunTimeoutIsResponsive(t *testing.T) {
	start := time.Now()
	_, code, _, _ := Run(context.Background(), []string{"sh", "-c", "sleep 30"}, 500*time.Millisecond)
	if code != ExitCodeTimeout {
		t.Fatalf("code=%d, want ExitCodeTimeout (%d)", code, ExitCodeTimeout)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout not responsive: took %s", elapsed)
	}
}

func TestRunCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, code, _, _ := Run(ctx, []string{"sh", "-c", "sleep 30"}, 0)
	if code != ExitCodeCanceled {
		t.Fatalf("code=%d, want ExitCodeCanceled (%d)", code, ExitCodeCanceled)
	}
}
