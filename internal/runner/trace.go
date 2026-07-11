package runner

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Tracing is enabled with UCA_TRACE=1: every subprocess is logged to stderr with
// its offset from process start, duration, and exit code. This is a debugging /
// performance-measurement aid, not a stable interface.
var (
	traceEnabled = os.Getenv("UCA_TRACE") != ""
	traceStart   = time.Now()
	traceMu      sync.Mutex
)

func trace(args []string, start time.Time, duration time.Duration, exitCode int) {
	if !traceEnabled {
		return
	}
	traceMu.Lock()
	defer traceMu.Unlock()
	fmt.Fprintf(os.Stderr, "[trace] +%7.0fms %7.0fms exit=%-3d %s\n",
		float64(start.Sub(traceStart).Microseconds())/1000,
		float64(duration.Microseconds())/1000,
		exitCode,
		strings.Join(args, " "))
}
