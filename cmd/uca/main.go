package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chhoumann/uca/internal/agents"
	"github.com/chhoumann/uca/internal/detect"
	"github.com/chhoumann/uca/internal/vercache"
)

type options struct {
	Parallel bool
	Serial   bool
	Safe     bool
	Timeout  time.Duration
	// Concurrency limits how many update commands are allowed to run at once.
	// 0 means "no limit" (default).
	Concurrency int
	Verbose     bool
	Quiet       bool
	DryRun      bool
	// Force runs every update command even for agents that are provably already
	// at the latest version (normally those are skipped as unchanged).
	Force   bool
	Check   bool
	Explain bool
	JSON    bool
	Only    string
	Skip    string
	Help    bool
	Version bool
}

type result struct {
	Agent     agents.Agent
	Status    string
	Reason    string
	Before    string
	After     string
	Duration  time.Duration
	Log       string
	UpdateCmd string
	Method    string
	Explain   string
}

var buildVersion = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		// parseFlags already printed the error and the custom usage to stderr.
		os.Exit(2)
	}
	if opts.Help {
		usage()
		return
	}
	if opts.Version {
		fmt.Fprintln(os.Stdout, buildVersion)
		return
	}
	if err := validateOptions(opts); err != nil {
		fmt.Fprintf(os.Stderr, "uca: %v\n", err)
		usageTo(os.Stderr)
		os.Exit(2)
	}

	all, err := loadAgents()
	if err != nil {
		fmt.Fprintf(os.Stderr, "uca: %v\n", err)
		os.Exit(2)
	}
	selected, unknown := filterAgents(all, opts.Only, opts.Skip)
	if len(selected) == 0 {
		if opts.Check {
			// Keep --check's own output schema, and signal a typo'd selection
			// (unknown names) with a non-zero exit so it isn't mistaken for "all
			// healthy".
			if opts.JSON {
				printCheckJSON(nil, unknown)
			} else {
				fmt.Fprintln(os.Stdout, noSelectionMessage(unknown, all))
			}
			if len(unknown) > 0 {
				os.Exit(2)
			}
			return
		}
		if opts.JSON {
			printJSON(nil, unknown, opts)
		} else {
			fmt.Fprintln(os.Stdout, noSelectionMessage(unknown, all))
			printSummary(nil, unknown)
		}
		return
	}

	env := detect.New(ctx)
	// Kick off the detection loaders the selected agents can need (they run
	// concurrently and dedupe with on-demand callers). Without this, the resolver
	// goroutines all walk the managers in the same order and the once-guarded
	// lazy loaders end up executing one after another - detection takes the
	// serial sum of all manager probes instead of the slowest single one.
	env.Prewarm(prewarmNeeds(selected))
	// Start the "latest version" lookups now so the network round-trip overlaps
	// detection instead of following it. Every mode consumes them: dry-run/check
	// and the live UI display them, and the update path uses them to skip
	// commands for agents provably already at latest.
	env.PrefetchLatest(ctx, nodePackages(selected))
	env.PrefetchMarketplaceLatest(ctx, extensionIDs(selected))

	verCache = vercache.Open()

	if opts.Check {
		checkResults := runCheck(ctx, selected, env)
		verCache.Save() // best-effort; a failed save just re-runs version reads next time
		if opts.JSON {
			printCheckJSON(checkResults, unknown)
		} else {
			printCheck(checkResults, unknown, opts)
		}
		if ctx.Err() == context.Canceled {
			os.Exit(130)
		}
		if hasOutdated(checkResults) {
			os.Exit(10)
		}
		return
	}

	uiEnabled := shouldShowUI(opts)
	results := runAll(ctx, selected, env, opts, uiEnabled)
	verCache.Save() // best-effort; a failed save just re-runs version reads next time

	if opts.JSON {
		printJSON(results, unknown, opts)
	} else {
		if !uiEnabled {
			printResults(results, opts)
		} else {
			fmt.Fprintln(os.Stdout)
			if opts.Explain && !opts.Quiet {
				printExplainDetails(results)
			}
		}
		printLogs(results, opts)
		printSummary(results, unknown)
	}

	// A user interrupt (Ctrl-C / SIGTERM) exits with the conventional 130 so
	// scripts can distinguish cancellation from an agent-update failure (1).
	if ctx.Err() == context.Canceled {
		os.Exit(130)
	}
	if hasFailures(results) {
		os.Exit(1)
	}
}

func parseFlags(args []string) (options, error) {
	var opts options
	fs := flag.NewFlagSet("uca", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	// On a parse error, render the polished custom usage (to stderr) instead of
	// Go's raw PrintDefaults dump, keeping error output consistent with --help.
	fs.Usage = func() { usageTo(os.Stderr) }
	fs.BoolVar(&opts.Parallel, "p", false, "run updates in parallel")
	fs.BoolVar(&opts.Parallel, "parallel", false, "run updates in parallel")
	fs.BoolVar(&opts.Serial, "serial", false, "run updates sequentially")
	fs.BoolVar(&opts.Safe, "safe", false, "use safer execution (limits concurrency)")
	fs.DurationVar(&opts.Timeout, "timeout", 15*time.Minute, "timeout per update command (0 disables)")
	fs.IntVar(&opts.Concurrency, "concurrency", 0, "max concurrent update commands (0 disables)")
	fs.BoolVar(&opts.Verbose, "v", false, "show update command output")
	fs.BoolVar(&opts.Verbose, "verbose", false, "show update command output")
	fs.BoolVar(&opts.Quiet, "q", false, "summary only")
	fs.BoolVar(&opts.Quiet, "quiet", false, "summary only")
	fs.BoolVar(&opts.DryRun, "n", false, "print commands without executing")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "print commands without executing")
	fs.BoolVar(&opts.Force, "f", false, "run updates even when already at the latest version")
	fs.BoolVar(&opts.Force, "force", false, "run updates even when already at the latest version")
	fs.BoolVar(&opts.Check, "check", false, "report which agents are outdated, do not update")
	fs.BoolVar(&opts.Explain, "explain", false, "explain detection and update method")
	fs.BoolVar(&opts.JSON, "json", false, "emit machine-readable JSON (implies no live UI)")
	fs.StringVar(&opts.Only, "only", "", "comma-separated agent list")
	fs.StringVar(&opts.Skip, "skip", "", "comma-separated agent list to exclude")
	fs.BoolVar(&opts.Help, "h", false, "show help")
	fs.BoolVar(&opts.Help, "help", false, "show help")
	fs.BoolVar(&opts.Version, "version", false, "show version")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		// flag.Parse stops at the first positional, so anything after it
		// (including flags) would be silently dropped and the run would proceed
		// with defaults. Fail loudly instead.
		err := fmt.Errorf("unexpected argument %q (select agents with --only/--skip)", fs.Arg(0))
		fmt.Fprintf(os.Stderr, "uca: %v\n", err)
		usageTo(os.Stderr)
		return opts, err
	}
	applyEnvDefaults(&opts, fs)
	return opts, nil
}

// applyEnvDefaults seeds options from UCA_* environment variables for any flag
// the user did not pass explicitly, so a persistent default (e.g. always
// --serial, or a standing --skip list) can be set without a shell alias. Flags
// always win over the environment; invalid values are ignored (the built-in
// default stands). Resulting values still go through validateOptions.
func applyEnvDefaults(opts *options, fs *flag.FlagSet) {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if !set["timeout"] {
		if v := os.Getenv("UCA_TIMEOUT"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				opts.Timeout = d
			}
		}
	}
	if !set["concurrency"] {
		if v := os.Getenv("UCA_CONCURRENCY"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				opts.Concurrency = n
			}
		}
	}
	if !set["only"] {
		if v := os.Getenv("UCA_ONLY"); v != "" {
			opts.Only = v
		}
	}
	if !set["skip"] {
		if v := os.Getenv("UCA_SKIP"); v != "" {
			opts.Skip = v
		}
	}
	if !set["serial"] && envIsTrue(os.Getenv("UCA_SERIAL")) {
		opts.Serial = true
	}
	if !set["safe"] && envIsTrue(os.Getenv("UCA_SAFE")) {
		opts.Safe = true
	}
}

func envIsTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// validateOptions rejects flag combinations that are contradictory or
// nonsensical, so the tool fails fast with a clear message instead of silently
// picking a surprising behavior.
func validateOptions(opts options) error {
	if opts.Serial && opts.Parallel {
		return errors.New("--serial and --parallel are mutually exclusive")
	}
	if opts.Concurrency < 0 {
		return errors.New("--concurrency must be >= 0")
	}
	return nil
}

func usage() {
	usageTo(os.Stdout)
}

func usageTo(w io.Writer) {
	fmt.Fprintf(w, `uca - update multiple coding-agent CLIs

Usage:
  uca [options]

Options:
  -p, --parallel    run updates in parallel (default)
      --serial      run updates sequentially
      --safe        safer execution (limits concurrency)
      --timeout D   timeout per update command (0 disables, default 15m)
      --concurrency N max concurrent update commands (0 disables)
  -v, --verbose     show update command output for each agent
  -q, --quiet       suppress per-agent version lines (summary only)
  -n, --dry-run     print commands that would run, do not execute
  -f, --force       run updates even when already at the latest version
      --check       report which agents are outdated, do not update (exit 10 if any are)
      --explain     show detection details and chosen update method
      --json        emit machine-readable JSON (implies no live UI)
      --only LIST   comma-separated agent list to include
      --skip LIST   comma-separated agent list to exclude
      --version     show version
  -h, --help        show usage

Examples:
  uca                      update every detected agent
  uca -n --only claude     preview the claude update only
  uca --check              report which agents are outdated (no changes)
  uca --json | jq .        machine-readable results

Environment (used as defaults; flags override):
  UCA_TIMEOUT, UCA_CONCURRENCY, UCA_ONLY, UCA_SKIP, UCA_SERIAL, UCA_SAFE

Config: define extra agents in <user-config-dir>/uca/config.json (or $UCA_CONFIG),
  e.g. {"agents":[{"name":"x","binary":"x","versionCmd":["x","--version"],
  "strategies":[{"kind":"npm","package":"x"}]}]}. Same-name entries override
  built-ins; a node strategy is per-manager, so list each manager you use.

Note: 'agent' is accepted as an alias for cursor in --only/--skip.
`)
}
