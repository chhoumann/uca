package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chhoumann/uca/internal/agents"
	"github.com/chhoumann/uca/internal/agentspec"
	"github.com/chhoumann/uca/internal/detect"
	runner "github.com/chhoumann/uca/internal/exec"
	"github.com/chhoumann/uca/internal/ui"
	"github.com/chhoumann/uca/internal/version"
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
	Check       bool
	Explain     bool
	JSON        bool
	Only        string
	Skip        string
	Help        bool
	Version     bool
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

// Local aliases of the shared vocabulary in internal/agents (single source of
// truth; these keep cmd/uca call sites terse without a parallel set of literals).
const (
	statusUpdated   = agents.StatusUpdated
	statusUnchanged = agents.StatusUnchanged
	statusSkipped   = agents.StatusSkipped
	statusFailed    = agents.StatusFailed
)

var buildVersion = "dev"

const (
	reasonMissing       = agents.ReasonMissing
	reasonMissingCode   = agents.ReasonMissingCode
	reasonManualInstall = agents.ReasonManualInstall
	reasonQuota         = agents.ReasonQuota
	reasonNpmNotEmpty   = agents.ReasonNpmNotEmpty
)

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

	if opts.Check {
		checkResults := runCheck(ctx, selected, env)
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

func noSelectionMessage(unknown []string, all []agents.Agent) string {
	if len(unknown) > 0 {
		return fmt.Sprintf("no agents selected (unknown: %s; valid: %s)", strings.Join(unknown, " "), knownAgentNames(all))
	}
	return "no agents selected"
}

func knownAgentNames(all []agents.Agent) string {
	names := make([]string, 0, len(all))
	for _, a := range all {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// loadAgents returns the built-in agents merged with any user-defined agents from
// the optional config file. A malformed config is a hard error (fail fast rather
// than silently ignoring something the user wrote).
func loadAgents() ([]agents.Agent, error) {
	base := agents.Default()
	user, err := loadConfigAgents()
	if err != nil {
		return nil, err
	}
	if len(user) == 0 {
		return base, nil
	}
	return mergeAgents(base, user), nil
}

// configPath resolves the optional config file: $UCA_CONFIG if set, else
// <user-config-dir>/uca/config.json. Empty when no location can be determined.
func configPath() string {
	if p := strings.TrimSpace(os.Getenv("UCA_CONFIG")); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "uca", "config.json")
}

func loadConfigAgents() ([]agents.Agent, error) {
	path := configPath()
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // no config is the normal case
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return parseConfigAgents(data, path)
}

func parseConfigAgents(data []byte, path string) ([]agents.Agent, error) {
	var cfg struct {
		Agents []agents.Agent `json:"agents"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	// Surface keys that match no field. (encoding/json still matches known fields
	// case-insensitively, so a case-variant of a real key is accepted, not flagged.)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	for i, a := range cfg.Agents {
		if strings.TrimSpace(a.Name) == "" {
			return nil, fmt.Errorf("parse config %s: agent #%d is missing a name", path, i+1)
		}
		for j, s := range a.Strategies {
			if !agents.ValidKind(s.Kind) {
				return nil, fmt.Errorf("parse config %s: agent %q strategy #%d has unknown kind %q", path, a.Name, j+1, s.Kind)
			}
		}
	}
	return cfg.Agents, nil
}

// mergeAgents appends user-defined agents to the built-ins; a user agent whose
// name matches a built-in (case-insensitively) overrides it.
func mergeAgents(base, user []agents.Agent) []agents.Agent {
	out := append([]agents.Agent(nil), base...)
	idx := make(map[string]int, len(out))
	for i, a := range out {
		idx[strings.ToLower(a.Name)] = i
	}
	for _, ua := range user {
		key := strings.ToLower(ua.Name)
		if i, ok := idx[key]; ok {
			out[i] = ua
		} else {
			idx[key] = len(out)
			out = append(out, ua)
		}
	}
	return out
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

func filterAgents(all []agents.Agent, onlyRaw, skipRaw string) ([]agents.Agent, []string) {
	known := make(map[string]string, len(all))
	for _, agent := range all {
		name := strings.ToLower(agent.Name)
		known[name] = name
		for _, alias := range agent.Aliases {
			alias = strings.ToLower(strings.TrimSpace(alias))
			if alias == "" {
				continue
			}
			known[alias] = name
		}
	}

	only, onlyUnknown := normalizeAgentList(parseList(onlyRaw), known)
	skip, skipUnknown := normalizeAgentList(parseList(skipRaw), known)

	// Distinguish "no --only given" (select all) from "--only given but every
	// entry was unknown" (select none). Without this, a typo like `--only bogus`
	// would fall through to selecting every agent.
	onlyProvided := strings.TrimSpace(onlyRaw) != ""

	unknownSet := map[string]bool{}
	for _, name := range onlyUnknown {
		unknownSet[name] = true
	}
	for _, name := range skipUnknown {
		unknownSet[name] = true
	}

	selected := make([]agents.Agent, 0, len(all))
	for _, agent := range all {
		// only/skip are keyed by lowercased canonical names; match the same way
		// so a user-defined agent with an uppercase name is still targetable.
		name := strings.ToLower(agent.Name)
		if onlyProvided && !only[name] {
			continue
		}
		if skip[name] {
			continue
		}
		selected = append(selected, agent)
	}

	unknown := make([]string, 0, len(unknownSet))
	for name := range unknownSet {
		unknown = append(unknown, name)
	}
	sort.Strings(unknown)
	return selected, unknown
}

func normalizeAgentList(items map[string]bool, known map[string]string) (map[string]bool, []string) {
	normalized := map[string]bool{}
	unknown := []string{}
	for name := range items {
		canonical, ok := known[name]
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		normalized[canonical] = true
	}
	return normalized, unknown
}

func parseList(raw string) map[string]bool {
	items := map[string]bool{}
	if strings.TrimSpace(raw) == "" {
		return items
	}
	parts := strings.Split(raw, ",")
	for _, part := range parts {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		items[name] = true
	}
	return items
}

func shouldShowUI(opts options) bool {
	if opts.Quiet || opts.JSON {
		return false
	}
	if !isTTY(os.Stdout) {
		return false
	}
	return true
}

func isTTY(file *os.File) bool {
	stat, err := file.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

func runAll(ctx context.Context, selected []agents.Agent, env *detect.Env, opts options, uiEnabled bool) []result {
	if uiEnabled {
		return runAllWithUI(ctx, selected, env, opts)
	}
	return runAllWithEvents(ctx, selected, env, opts, nil)
}

type agentWork struct {
	agent           agents.Agent
	index           int
	show            bool
	method          string
	explain         string
	reason          string
	versionCmd      []string
	nodePackageName string
	// nodePackageVersion is the pinned version (empty = @latest). A pinned node
	// agent is excluded from batching, since the batch assumes a uniform @latest.
	nodePackageVersion string
	// updateCmd is the final command to run (may be a batch command).
	updateCmd []string
	// updateCmdSingle is the per-agent command (used for fallback when batch updates fail).
	updateCmdSingle []string
}

type updateTask struct {
	kind   string
	cmd    []string
	agents []agentWork
}

type managerLocker struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newManagerLocker() *managerLocker {
	return &managerLocker{locks: map[string]*sync.Mutex{}}
}

func (l *managerLocker) lock(kind string) func() {
	if kind == "" {
		return func() {}
	}
	l.mu.Lock()
	m, ok := l.locks[kind]
	if !ok {
		m = &sync.Mutex{}
		l.locks[kind] = m
	}
	l.mu.Unlock()
	m.Lock()
	return func() { m.Unlock() }
}

func effectiveConcurrency(opts options, numTasks int) int {
	if opts.Serial {
		return 1
	}
	if opts.Safe && opts.Concurrency <= 0 {
		return 1
	}
	if opts.Concurrency > 0 {
		return opts.Concurrency
	}
	if numTasks <= 0 {
		return 1
	}
	return numTasks
}

func runAllWithEvents(ctx context.Context, selected []agents.Agent, env *detect.Env, opts options, events chan<- updateEvent) []result {
	results := make([]result, len(selected))
	works := make([]agentWork, len(selected))

	// Resolve agents concurrently: each resolveUpdate may shell out (native help
	// probe, brew/pip/uv detection) and trigger the once-loaders, so running them
	// in parallel makes detection ~max(slowest) instead of the serial sum. The
	// loaders are sync.Once/mutex-guarded, so this is safe.
	// Seed identity fields up front so an entry left unscheduled by a mid-loop
	// cancellation still maps to its own index (rather than collapsing onto the
	// zero-value index 0) in the downstream emission loops.
	for i, agent := range selected {
		works[i] = agentWork{agent: agent, index: i}
	}
	var resolveWG sync.WaitGroup
	resolveSem := make(chan struct{}, resolveConcurrency)
	for i, agent := range selected {
		if ctx.Err() != nil {
			break // user interrupted during detection; stop scheduling more work
		}
		resolveWG.Add(1)
		go func(i int, agent agents.Agent) {
			defer resolveWG.Done()
			resolveSem <- struct{}{}
			defer func() { <-resolveSem }()
			resolved := agentspec.Resolve(agent, env)
			work := agentWork{
				agent:           agent,
				index:           i,
				show:            resolved.Cmd != nil || resolved.Reason == reasonManualInstall,
				method:          resolved.Method,
				explain:         resolved.Detail,
				reason:          resolved.Reason,
				versionCmd:      resolved.VersionCmd,
				updateCmdSingle: resolved.Cmd,
			}
			if agentspec.IsNodeKind(resolved.Method) {
				work.nodePackageName = agentspec.NodePackageName(agent.Strategies)
				work.nodePackageVersion = resolved.Version
			}
			works[i] = work
		}(i, agent)
	}
	resolveWG.Wait()

	// Build tasks (batch node updates by manager kind).
	tasks := []updateTask{}
	nodeGroups := map[string][]int{}
	for i := range works {
		work := &works[i]
		if work.updateCmdSingle == nil {
			continue
		}
		if agentspec.IsNodeKind(work.method) {
			nodeGroups[work.method] = append(nodeGroups[work.method], i)
			continue
		}
		work.updateCmd = work.updateCmdSingle
		tasks = append(tasks, updateTask{kind: work.method, cmd: work.updateCmd, agents: []agentWork{*work}})
	}
	for kind, indexes := range nodeGroups {
		pkgSet := map[string]bool{}
		pkgs := make([]string, 0, len(indexes))
		batchIndexes := make([]int, 0, len(indexes))
		for _, idx := range indexes {
			pkg := strings.TrimSpace(works[idx].nodePackageName)
			// A version-pinned agent can't join the @latest batch — run it on its
			// own with its pinned single command.
			if pkg == "" || works[idx].nodePackageVersion != "" {
				works[idx].updateCmd = works[idx].updateCmdSingle
				tasks = append(tasks, updateTask{kind: kind, cmd: works[idx].updateCmd, agents: []agentWork{works[idx]}})
				continue
			}
			if !pkgSet[pkg] {
				pkgSet[pkg] = true
				pkgs = append(pkgs, pkg)
			}
			batchIndexes = append(batchIndexes, idx)
		}
		if len(batchIndexes) == 0 {
			continue
		}
		sort.Strings(pkgs)
		cmd := agentspec.NodeBatchUpdateCommand(kind, pkgs)
		group := make([]agentWork, 0, len(indexes))
		for _, idx := range batchIndexes {
			works[idx].updateCmd = cmd
			group = append(group, works[idx])
		}
		tasks = append(tasks, updateTask{kind: kind, cmd: cmd, agents: group})
	}

	// Emit detect events and handle skipped/dry-run results.
	now := time.Now()

	if opts.DryRun {
		return dryRunResults(ctx, works, env, results, events, now)
	}

	for _, work := range works {
		res := result{
			Agent:     work.agent,
			Method:    work.method,
			Explain:   work.explain,
			UpdateCmd: cmdString(work.updateCmd),
		}

		if work.updateCmdSingle == nil {
			res.Status = statusSkipped
			if work.reason == "" {
				res.Reason = reasonMissing
			} else {
				res.Reason = work.reason
			}
			results[work.index] = res
			if events != nil {
				events <- updateEvent{Index: work.index, Phase: phaseDetect, Result: res, Time: now, Show: work.show}
				events <- updateEvent{Index: work.index, Phase: phaseFinish, Result: res, Time: now, Show: work.show}
			}
			continue
		}

		if events != nil {
			events <- updateEvent{Index: work.index, Phase: phaseDetect, Result: res, Time: now, Show: work.show}
		}
	}

	locker := newManagerLocker()
	taskCh := make(chan updateTask)
	var wg sync.WaitGroup
	workerCount := effectiveConcurrency(opts, len(tasks))
	if workerCount > len(tasks) {
		workerCount = len(tasks)
	}
	if workerCount < 1 {
		workerCount = 1
	}
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer wg.Done()
			for task := range taskCh {
				runTask(ctx, task, env, opts, locker, events, results)
			}
		}()
	}
	for _, task := range tasks {
		taskCh <- task
	}
	close(taskCh)
	wg.Wait()

	return results
}

const (
	dryRunPreviewBudget      = 12 * time.Second
	dryRunPreviewConcurrency = 8
	resolveConcurrency       = 16
)

// dryRunResults computes the dry-run preview. It emits all detect events first
// (cheap), then fetches current+latest versions concurrently — each is a
// subprocess / network round-trip — then emits finish events. This keeps
// `uca -n` fast: wall-clock is ~max(single lookup) instead of the sum across
// agents (previously the lookups ran fully serially with no budget cap).
func dryRunResults(ctx context.Context, works []agentWork, env *detect.Env, results []result, events chan<- updateEvent, now time.Time) []result {
	updatable := make([]*agentWork, 0, len(works))
	for i := range works {
		work := &works[i]
		res := result{
			Agent:     work.agent,
			Method:    work.method,
			Explain:   work.explain,
			UpdateCmd: cmdString(work.updateCmd),
		}
		if work.updateCmdSingle == nil {
			res.Status = statusSkipped
			if work.reason == "" {
				res.Reason = reasonMissing
			} else {
				res.Reason = work.reason
			}
			results[work.index] = res
			if events != nil {
				events <- updateEvent{Index: work.index, Phase: phaseDetect, Result: res, Time: now, Show: work.show}
				events <- updateEvent{Index: work.index, Phase: phaseFinish, Result: res, Time: now, Show: work.show}
			}
			continue
		}
		if events != nil {
			events <- updateEvent{Index: work.index, Phase: phaseDetect, Result: res, Time: now, Show: work.show}
		}
		updatable = append(updatable, work)
	}

	previewCtx, cancel := context.WithTimeout(ctx, dryRunPreviewBudget)
	var wg sync.WaitGroup
	sem := make(chan struct{}, dryRunPreviewConcurrency)
	for _, work := range updatable {
		wg.Add(1)
		go func(work *agentWork) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res := result{
				Agent:     work.agent,
				Method:    work.method,
				Explain:   work.explain,
				UpdateCmd: cmdString(work.updateCmd),
				Status:    statusUpdated,
				Reason:    "dry-run",
			}
			res.Before = getVersion(previewCtx, work.agent, env, work.method, work.versionCmd)
			res.After = res.Before
			if agentspec.IsNodeKind(work.method) {
				if latest := env.NodeLatestVersion(previewCtx, work.method, work.nodePackageName); latest != "" {
					if formatted := version.FormatWithToken(res.Before, latest); formatted != "" {
						res.After = formatted
					} else {
						res.After = latest
					}
				}
			}
			results[work.index] = res
		}(work)
	}
	wg.Wait()
	cancel()

	if events != nil {
		for _, work := range updatable {
			events <- updateEvent{Index: work.index, Phase: phaseFinish, Result: results[work.index], Time: time.Now(), Show: work.show}
		}
	}
	return results
}

func runTask(ctx context.Context, task updateTask, env *detect.Env, opts options, locker *managerLocker, events chan<- updateEvent, results []result) {
	if len(task.agents) == 0 {
		return
	}

	kind := task.kind
	unlock := func() {}
	if agentspec.ShouldLockKind(kind) {
		unlock = locker.lock(kind)
	}
	defer unlock()

	// Prepare results and emit start events.
	prepared := make([]result, len(task.agents))
	for i, work := range task.agents {
		res := result{
			Agent:     work.agent,
			Method:    work.method,
			Explain:   work.explain,
			UpdateCmd: cmdString(work.updateCmd),
		}
		res.Before = getVersion(ctx, work.agent, env, work.method, work.versionCmd)
		prepared[i] = res
	}
	if events != nil && agentspec.IsNodeKind(kind) {
		// Best-effort latest version preview. Keep it short so we don't delay updates on bad networks.
		previewCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		var wg sync.WaitGroup
		for i, work := range task.agents {
			pkg := strings.TrimSpace(work.nodePackageName)
			if pkg == "" {
				continue
			}
			before := prepared[i].Before
			wg.Add(1)
			go func(i int, before, pkg string) {
				defer wg.Done()
				latest := env.NodeLatestVersion(previewCtx, kind, pkg)
				if latest == "" {
					return
				}
				after := version.FormatWithToken(before, latest)
				if after == "" {
					after = latest
				}
				prepared[i].After = after
			}(i, before, pkg)
		}
		wg.Wait()
		cancel()
	}
	startTime := time.Now()
	if events != nil {
		for i, work := range task.agents {
			events <- updateEvent{Index: work.index, Phase: phaseStart, Result: prepared[i], Time: startTime, Show: work.show}
		}
	}

	out, classifyOut, exitCode, duration, _ := runUpdateCmd(ctx, task.cmd, opts.Timeout)

	// If a batched node update fails, fall back to per-package updates so we can still make progress and
	// attribute failures precisely.
	if exitCode != 0 && len(task.agents) > 1 && agentspec.IsNodeKind(kind) {
		for i, work := range task.agents {
			res := prepared[i]
			res.Explain = appendHint(res.Explain, "batch update failed; retrying individually")

			indOut, indClassifyOut, indExitCode, indDuration, _ := runUpdateCmd(ctx, work.updateCmdSingle, opts.Timeout)
			res.Duration = indDuration
			res.Log = strings.TrimRight(out, "\n")
			if strings.TrimSpace(res.Log) != "" && strings.TrimSpace(indOut) != "" {
				res.Log += "\n\n(uca) retrying individually after batch failure\n"
			} else if strings.TrimSpace(res.Log) != "" {
				res.Log += "\n"
			}
			res.Log += strings.TrimSpace(indOut)
			res.After = getVersion(ctx, work.agent, env, work.method, work.versionCmd)

			if indExitCode != 0 {
				setFailureResult(&res, indExitCode, work.updateCmdSingle, indClassifyOut, opts.Timeout)
			} else if res.Before != "" && res.After != "" && res.Before == res.After && res.Before != "unknown" {
				res.Status = statusUnchanged
			} else {
				res.Status = statusUpdated
			}
			results[work.index] = res
			if events != nil {
				events <- updateEvent{Index: work.index, Phase: phaseFinish, Result: res, Time: time.Now(), Show: work.show}
			}
		}
		return
	}

	// Batch success or non-batch failure path.
	for i, work := range task.agents {
		res := prepared[i]
		res.Duration = duration
		res.Log = out
		res.After = getVersion(ctx, work.agent, env, work.method, work.versionCmd)

		if exitCode != 0 {
			setFailureResult(&res, exitCode, task.cmd, classifyOut, opts.Timeout)
		} else if res.Before != "" && res.After != "" && res.Before == res.After && res.Before != "unknown" {
			res.Status = statusUnchanged
		} else {
			res.Status = statusUpdated
		}
		results[work.index] = res
		if events != nil {
			events <- updateEvent{Index: work.index, Phase: phaseFinish, Result: res, Time: time.Now(), Show: work.show}
		}
	}
}

type updateEvent struct {
	Index  int
	Phase  string
	Result result
	Time   time.Time
	Show   bool
}

const (
	phaseDetect = agents.PhaseDetect
	phaseStart  = agents.PhaseStart
	phaseFinish = agents.PhaseFinish
)

func runAllWithUI(ctx context.Context, selected []agents.Agent, env *detect.Env, opts options) []result {
	events := make(chan updateEvent, len(selected)*4)
	done := make(chan struct{})

	rows := make([]ui.Row, len(selected))
	nameWidth := 0
	for i, agent := range selected {
		rows[i] = ui.Row{Name: agent.Name, Status: agents.StatusPending, Visible: false}
		if len(agent.Name) > nameWidth {
			nameWidth = len(agent.Name)
		}
	}

	renderer := ui.NewRenderer(os.Stdout)
	start := time.Now()
	ui.HideCursor(renderer.Out)
	totalAgents := len(selected)
	detectedCount := 0
	renderer.Draw(renderer.RenderFrame(rows, nameWidth, start, opts.Explain, detectedCount, totalAgents))

	ticker := time.NewTicker(120 * time.Millisecond)
	go func() {
		defer close(done)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					ticker.Stop()
					renderer.Draw(renderer.RenderFrame(rows, nameWidth, start, opts.Explain, detectedCount, totalAgents))
					return
				}
				if ev.Phase == phaseDetect && !rows[ev.Index].Detected {
					rows[ev.Index].Detected = true
					detectedCount++
				}
				ui.ApplyEvent(&rows[ev.Index], toUIEvent(ev))
				renderer.Draw(renderer.RenderFrame(rows, nameWidth, start, opts.Explain, detectedCount, totalAgents))
			case <-ticker.C:
				renderer.Draw(renderer.RenderFrame(rows, nameWidth, start, opts.Explain, detectedCount, totalAgents))
			}
		}
	}()

	env.Prewarm()

	results := runAllWithEvents(ctx, selected, env, opts, events)
	close(events)
	<-done
	ui.ShowCursor(renderer.Out)
	return results
}

// toUIEvent adapts the orchestrator's updateEvent into the dashboard's flat
// view-model so internal/ui need not depend on the result/options types.
func toUIEvent(ev updateEvent) ui.Event {
	return ui.Event{
		Index:    ev.Index,
		Phase:    ev.Phase,
		Status:   ev.Result.Status,
		Reason:   ev.Result.Reason,
		Method:   ev.Result.Method,
		Before:   ev.Result.Before,
		After:    ev.Result.After,
		Duration: ev.Result.Duration,
		Time:     ev.Time,
		Show:     ev.Show,
	}
}

// versionSpec returns the version selector for a package spec: a pinned version
// when set, otherwise "latest" (forced to avoid getting stuck on old
// minor/prerelease versions, common for 0.x CLIs).
const versionCmdTimeout = 10 * time.Second

func getVersion(ctx context.Context, agent agents.Agent, env *detect.Env, method string, versionCmd []string) string {
	if method == agents.KindVSCode && agent.ExtensionID != "" {
		if version := env.VscodeVersion(agent.ExtensionID); version != "" {
			return version
		}
	}
	if len(versionCmd) > 0 {
		return runVersionCmd(ctx, versionCmd)
	}
	if len(agent.VersionCmd) > 0 {
		if agent.Binary == "" || env.HasBinary(agent.Binary) {
			return runVersionCmd(ctx, agent.VersionCmd)
		}
	}
	if agent.ExtensionID != "" {
		if version := env.VscodeVersion(agent.ExtensionID); version != "" {
			return version
		}
	}
	return "unknown"
}

// nodeLatestVersion returns the registry "latest" for a node package, memoized
// per (kind,pkg) so the dry-run preview and the live preview don't re-query the
// same package. Only successful (non-empty) results are cached, so a transient
// failure does not poison the preview for the rest of the run.
// queryNodeLatestVersion runs the manager's registry query and extracts a single
// semver token. Managers (and wrappers like safe-chain) can emit advisory banner
// lines on stdout, so the parse prefers a clean standalone version line and fails
// closed (empty -> no preview) rather than surfacing banner text as a "version".
// latestVersion returns the latest available version for an update method, or ""
// when it is not cheaply/reliably knowable (native updaters, VS Code extensions,
// and pip/uv tools, which lack a stable, banner-free CLI query). Used by --check.
type checkState string

const (
	checkUpToDate checkState = "up-to-date"
	checkOutdated checkState = "outdated"
	checkUnknown  checkState = "unknown"
	checkMissing  checkState = "missing"
)

type checkResult struct {
	Agent   agents.Agent
	Method  string
	State   checkState
	Current string
	Latest  string
	Reason  string
}

// compareVersions decides an agent's check state. It treats "current >= latest"
// as up-to-date (so a build that is newer than the published latest is not
// falsely flagged) and only "current strictly older than latest" as outdated.
func compareVersions(current, latest string) checkState {
	if strings.TrimSpace(latest) == "" {
		return checkUnknown
	}
	curToken, ok := version.ExtractToken(current)
	if !ok {
		return checkUnknown
	}
	latToken, ok := version.ExtractToken(latest)
	if !ok {
		latToken = latest
	}
	if version.Compare(curToken, latToken) < 0 {
		return checkOutdated
	}
	return checkUpToDate
}

const (
	checkConcurrency = 8
	checkBudget      = 15 * time.Second
)

// runCheck resolves every selected agent and compares its installed version to
// the latest available one, without changing anything. Lookups run concurrently
// under a shared budget.
func runCheck(ctx context.Context, selected []agents.Agent, env *detect.Env) []checkResult {
	results := make([]checkResult, len(selected))
	checkCtx, cancel := context.WithTimeout(ctx, checkBudget)
	defer cancel()
	var wg sync.WaitGroup
	sem := make(chan struct{}, checkConcurrency)
	for i, agent := range selected {
		wg.Add(1)
		go func(i int, agent agents.Agent) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resolved := agentspec.Resolve(agent, env)
			res := checkResult{Agent: agent, Method: resolved.Method}
			if resolved.Cmd == nil {
				res.State = checkMissing
				res.Reason = resolved.Reason
				if res.Reason == "" {
					res.Reason = reasonMissing
				}
				results[i] = res
				return
			}
			res.Current = getVersion(checkCtx, agent, env, resolved.Method, resolved.VersionCmd)
			res.Latest = env.LatestVersion(checkCtx, resolved.Method, resolved.Pkg)
			res.State = compareVersions(res.Current, res.Latest)
			results[i] = res
		}(i, agent)
	}
	wg.Wait()
	return results
}

func hasOutdated(results []checkResult) bool {
	for _, res := range results {
		if res.State == checkOutdated {
			return true
		}
	}
	return false
}

func runVersionCmd(ctx context.Context, args []string) string {
	if len(args) == 0 {
		return "unknown"
	}
	out, code, _, _ := runner.Run(ctx, args, versionCmdTimeout)
	if code != 0 {
		return "unknown"
	}
	return version.ParseOutput(out)
}

func runUpdateCmd(ctx context.Context, args []string, timeout time.Duration) (string, string, int, time.Duration, error) {
	out, exitCode, duration, err := runner.Run(ctx, args, timeout)
	classifyOut := out
	if exitCode == 0 {
		return out, classifyOut, exitCode, duration, err
	}
	if shouldRetryNpm(args, out) {
		cleanupMsg := cleanupNpmENotEmpty(out)
		retryOut, retryCode, retryDuration, retryErr := runner.Run(ctx, args, timeout)
		combined := formatRetryOutput(out, cleanupMsg, retryOut)
		classifyOut = retryOut
		if strings.TrimSpace(classifyOut) == "" {
			classifyOut = out
		}
		return combined, classifyOut, retryCode, duration + retryDuration, retryErr
	}
	return out, classifyOut, exitCode, duration, err
}

func setFailureResult(res *result, exitCode int, updateCmd []string, output string, timeout time.Duration) {
	res.Status = statusFailed
	switch exitCode {
	case runner.ExitCodeTimeout:
		res.Reason = "timeout"
		if timeout > 0 {
			res.Explain = appendHint(res.Explain, fmt.Sprintf("command timed out after %s; rerun with --timeout 0 or increase it", timeout.Round(time.Second)))
		} else {
			res.Explain = appendHint(res.Explain, "command timed out; rerun with a larger --timeout")
		}
		return
	case runner.ExitCodeCanceled:
		res.Reason = "canceled"
		res.Explain = appendHint(res.Explain, "interrupted; retry the update")
		return
	}
	reason, hint := classifyUpdateFailure(updateCmd, output)
	if reason == "" {
		res.Reason = fmt.Sprintf("exit %d", exitCode)
	} else {
		res.Reason = reason
	}
	if hint != "" {
		res.Explain = appendHint(res.Explain, hint)
	}
}

func classifyUpdateFailure(updateCmd []string, output string) (string, string) {
	lower := strings.ToLower(output)
	if strings.Contains(output, "TerminalQuotaError") ||
		strings.Contains(lower, "exhausted your capacity") ||
		strings.Contains(lower, "quota will reset") {
		return reasonQuota, "quota exceeded; retry later or update via npm (@google/gemini-cli)"
	}
	if isNpmGlobalMutate(updateCmd) && (strings.Contains(output, "ENOTEMPTY") ||
		strings.Contains(output, "errno -66") ||
		strings.Contains(lower, "directory not empty")) {
		return reasonNpmNotEmpty, "npm rename failed; retry or remove leftover temp directory under the global npm prefix"
	}
	if strings.Contains(lower, "eacces") || strings.Contains(lower, "eperm") || strings.Contains(lower, "permission denied") {
		return "permission", "permission error; check your global install prefix and file permissions"
	}
	if strings.Contains(lower, "etimedout") ||
		strings.Contains(lower, "timed out") ||
		strings.Contains(lower, "econnreset") ||
		strings.Contains(lower, "enotfound") ||
		strings.Contains(lower, "eai_again") ||
		strings.Contains(lower, "econnrefused") ||
		strings.Contains(lower, "socket hang up") {
		return "network", "network error; check connectivity/proxy/VPN and retry"
	}
	if strings.Contains(lower, "self signed certificate") ||
		strings.Contains(lower, "unable to get local issuer certificate") ||
		strings.Contains(lower, "cert has expired") ||
		strings.Contains(lower, "ssl routines") ||
		strings.Contains(lower, "tls") && strings.Contains(lower, "certificate") {
		return "tls", "TLS/CA error; check corporate proxy settings or system certificates"
	}
	if len(updateCmd) > 0 && updateCmd[0] == "brew" &&
		(strings.Contains(lower, "another active homebrew update process") ||
			strings.Contains(lower, "homebrew is already updating") ||
			strings.Contains(lower, "cannot install in homebrew prefix")) {
		return "brew busy", "homebrew is locked/busy; wait for other brew process and retry"
	}
	return "", ""
}

func appendHint(detail, hint string) string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return detail
	}
	if strings.TrimSpace(detail) == "" {
		return "hint: " + hint
	}
	return detail + "; hint: " + hint
}

func shouldRetryNpm(args []string, output string) bool {
	if !isNpmGlobalMutate(args) {
		return false
	}
	if strings.Contains(output, "ENOTEMPTY") {
		return true
	}
	if strings.Contains(output, "errno -66") {
		return true
	}
	if strings.Contains(output, "directory not empty") {
		return true
	}
	return false
}

func formatRetryOutput(first, cleanupMsg, second string) string {
	first = strings.TrimRight(first, "\n")
	cleanupMsg = strings.TrimSpace(cleanupMsg)
	second = strings.TrimSpace(second)
	if first == "" {
		return second
	}
	if second == "" {
		return first
	}
	if cleanupMsg != "" {
		return fmt.Sprintf("%s\n\n(uca) %s\n(uca) retrying npm after ENOTEMPTY\n%s", first, cleanupMsg, second)
	}
	return fmt.Sprintf("%s\n\n(uca) retrying npm after ENOTEMPTY\n%s", first, second)
}

func isNpmGlobalMutate(args []string) bool {
	if len(args) < 2 || args[0] != "npm" {
		return false
	}
	switch args[1] {
	case "install", "update":
		return true
	default:
		return false
	}
}

func cleanupNpmENotEmpty(output string) string {
	path, dest := extractNpmRenamePaths(output)
	if !isSafeNpmRenameTarget(path, dest) {
		return ""
	}
	if _, err := os.Stat(dest); err != nil {
		return ""
	}
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Sprintf("failed to remove stale npm temp dir %s: %v", dest, err)
	}
	return fmt.Sprintf("removed stale npm temp dir %s", dest)
}

func extractNpmRenamePaths(output string) (string, string) {
	var path string
	var dest string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "npm error path ") {
			path = strings.TrimSpace(strings.TrimPrefix(line, "npm error path "))
			continue
		}
		if strings.HasPrefix(line, "npm error dest ") {
			dest = strings.TrimSpace(strings.TrimPrefix(line, "npm error dest "))
		}
	}
	if path != "" && dest != "" {
		return path, dest
	}
	scanner = bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.Contains(line, "rename '") || !strings.Contains(line, "' -> '") {
			continue
		}
		start := strings.Index(line, "rename '")
		if start == -1 {
			continue
		}
		start += len("rename '")
		mid := strings.Index(line[start:], "' -> '")
		if mid == -1 {
			continue
		}
		path = line[start : start+mid]
		rest := line[start+mid+len("' -> '"):]
		end := strings.Index(rest, "'")
		if end == -1 {
			continue
		}
		dest = rest[:end]
		break
	}
	return path, dest
}

func isSafeNpmRenameTarget(path, dest string) bool {
	if path == "" || dest == "" {
		return false
	}
	if !filepath.IsAbs(dest) || !filepath.IsAbs(path) {
		return false
	}
	if filepath.Dir(path) != filepath.Dir(dest) {
		return false
	}
	base := filepath.Base(path)
	destBase := filepath.Base(dest)
	if destBase == "." || destBase == ".." || base == "." || base == ".." {
		return false
	}
	prefix := "." + base
	if !strings.HasPrefix(destBase, prefix) {
		return false
	}
	return true
}

// detectCmdTimeout bounds each detection subprocess. These are local registry /
// manager metadata reads, so a tight bound keeps startup responsive on a degraded
// environment (e.g. a manager hung behind a dead proxy) without affecting healthy
// runs, where they return in well under a second.
func cmdString(args []string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, quoteArg(arg))
	}
	return strings.Join(parts, " ")
}

func quoteArg(arg string) string {
	if strings.IndexFunc(arg, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '"' || r == '\'' }) == -1 {
		return arg
	}
	return fmt.Sprintf("%q", arg)
}

func printResults(results []result, opts options) {
	if opts.Quiet {
		return
	}
	// In dry-run, node agents under one manager share a single batch command.
	// Group them so the command prints once under all involved agents instead of
	// repeating the full batch line (with every other agent's package) per agent.
	// --explain stays per-agent because its detail differs per agent.
	if opts.DryRun && !opts.Explain {
		printDryRunPlan(results)
		return
	}
	for _, res := range results {
		fmt.Fprintln(os.Stdout, formatResult(res, opts))
		if opts.Explain {
			if line := formatExplain(res); line != "" {
				fmt.Fprintln(os.Stdout, line)
			}
		}
	}
}

func printDryRunPlan(results []result) {
	for _, line := range dryRunPlanLines(results) {
		fmt.Fprintln(os.Stdout, line)
	}
}

// dryRunPlanLines renders the dry-run plan, collapsing agents that share a batch
// command onto a single line (e.g. "codex, opencode, pi: bun add -g ...") so the
// batch command is shown once rather than repeated per agent.
func dryRunPlanLines(results []result) []string {
	lines := make([]string, 0, len(results))
	printedCmd := map[string]bool{}
	for i, res := range results {
		if res.Status != statusUpdated {
			// skipped / other: print individually, in place.
			lines = append(lines, formatResult(res, options{DryRun: true}))
			continue
		}
		if printedCmd[res.UpdateCmd] {
			continue
		}
		printedCmd[res.UpdateCmd] = true
		names := []string{res.Agent.Name}
		for _, other := range results[i+1:] {
			if other.Status == statusUpdated && other.UpdateCmd == res.UpdateCmd {
				names = append(names, other.Agent.Name)
			}
		}
		lines = append(lines, fmt.Sprintf("%s: %s", strings.Join(names, ", "), res.UpdateCmd))
	}
	return lines
}

func printExplainDetails(results []result) {
	for _, res := range results {
		if strings.TrimSpace(res.Explain) == "" {
			continue
		}
		fmt.Fprintf(os.Stdout, "%s: %s\n", res.Agent.Name, res.Explain)
	}
}

func formatResult(res result, opts options) string {
	name := res.Agent.Name
	switch res.Status {
	case statusSkipped:
		return fmt.Sprintf("%s: skipped (%s)", name, res.Reason)
	case statusFailed:
		reason := strings.TrimSpace(res.Reason)
		if reason != "" {
			return fmt.Sprintf("%s: failed (%s; %s -> %s (%s))", name, reason, safeVersion(res.Before), safeVersion(res.After), fmtDuration(res.Duration))
		}
		return fmt.Sprintf("%s: failed (%s -> %s (%s))", name, safeVersion(res.Before), safeVersion(res.After), fmtDuration(res.Duration))
	case statusUpdated:
		if opts.DryRun {
			return fmt.Sprintf("%s: %s", name, res.UpdateCmd)
		}
		return fmt.Sprintf("%s: %s -> %s (%s)", name, safeVersion(res.Before), safeVersion(res.After), fmtDuration(res.Duration))
	case statusUnchanged:
		return fmt.Sprintf("%s: unchanged %s -> %s (%s)", name, safeVersion(res.Before), safeVersion(res.After), fmtDuration(res.Duration))
	default:
		return fmt.Sprintf("%s: unknown", name)
	}
}

func formatExplain(res result) string {
	if strings.TrimSpace(res.Explain) == "" {
		return ""
	}
	return fmt.Sprintf("  info: %s", res.Explain)
}

type jsonAgentResult struct {
	Name            string `json:"name"`
	Method          string `json:"method,omitempty"`
	Status          string `json:"status"`
	Reason          string `json:"reason,omitempty"`
	Before          string `json:"before,omitempty"`
	After           string `json:"after,omitempty"`
	DurationSeconds int    `json:"durationSeconds"`
	UpdateCmd       string `json:"updateCmd,omitempty"`
	Explain         string `json:"explain,omitempty"`
}

type jsonReport struct {
	DryRun       bool              `json:"dryRun"`
	Agents       []jsonAgentResult `json:"agents"`
	UnknownNames []string          `json:"unknownNames,omitempty"`
	Summary      map[string]int    `json:"summary"`
}

// jsonStatus normalizes the internal status into a stable, self-describing token
// for machine consumers (dry-run surfaces as its own status rather than
// "updated" with reason "dry-run").
func jsonStatus(res result) string {
	if res.Status == statusUpdated && res.Reason == "dry-run" {
		return "dry-run"
	}
	if res.Status == "" {
		return "unknown"
	}
	return res.Status
}

func buildReport(results []result, unknown []string, opts options) jsonReport {
	report := jsonReport{
		DryRun:       opts.DryRun,
		Agents:       make([]jsonAgentResult, 0, len(results)),
		UnknownNames: unknown,
		Summary:      map[string]int{},
	}
	for _, res := range results {
		status := jsonStatus(res)
		reason := res.Reason
		if status == "dry-run" {
			reason = "" // redundant with the status
		}
		report.Agents = append(report.Agents, jsonAgentResult{
			Name:            res.Agent.Name,
			Method:          res.Method,
			Status:          status,
			Reason:          reason,
			Before:          res.Before,
			After:           res.After,
			DurationSeconds: int(res.Duration.Round(time.Second).Seconds()),
			UpdateCmd:       res.UpdateCmd,
			Explain:         res.Explain,
		})
		report.Summary[status]++
	}
	return report
}

func printJSON(results []result, unknown []string, opts options) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(buildReport(results, unknown, opts)); err != nil {
		fmt.Fprintf(os.Stderr, "uca: failed to encode JSON: %v\n", err)
	}
}

type jsonCheckAgent struct {
	Name    string `json:"name"`
	Method  string `json:"method,omitempty"`
	State   string `json:"state"`
	Current string `json:"current,omitempty"`
	Latest  string `json:"latest,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type jsonCheckReport struct {
	Agents       []jsonCheckAgent `json:"agents"`
	UnknownNames []string         `json:"unknownNames,omitempty"`
	Summary      map[string]int   `json:"summary"`
}

func buildCheckReport(results []checkResult, unknown []string) jsonCheckReport {
	report := jsonCheckReport{
		Agents:       make([]jsonCheckAgent, 0, len(results)),
		UnknownNames: unknown,
		Summary:      map[string]int{},
	}
	for _, res := range results {
		report.Agents = append(report.Agents, jsonCheckAgent{
			Name:    res.Agent.Name,
			Method:  res.Method,
			State:   string(res.State),
			Current: res.Current,
			Latest:  res.Latest,
			Reason:  res.Reason,
		})
		report.Summary[string(res.State)]++
	}
	return report
}

func printCheckJSON(results []checkResult, unknown []string) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(buildCheckReport(results, unknown)); err != nil {
		fmt.Fprintf(os.Stderr, "uca: failed to encode JSON: %v\n", err)
	}
}

func printCheck(results []checkResult, unknown []string, opts options) {
	nameWidth := 0
	for _, res := range results {
		if len(res.Agent.Name) > nameWidth {
			nameWidth = len(res.Agent.Name)
		}
	}
	upToDate, outdated, unknownCnt, missing := []string{}, []string{}, []string{}, []string{}
	for _, res := range results {
		var detail string
		switch res.State {
		case checkOutdated:
			detail = fmt.Sprintf("outdated (%s -> %s)", safeVersion(res.Current), safeVersion(res.Latest))
			outdated = append(outdated, res.Agent.Name)
		case checkUpToDate:
			detail = fmt.Sprintf("up-to-date (%s)", safeVersion(res.Current))
			upToDate = append(upToDate, res.Agent.Name)
		case checkMissing:
			detail = res.Reason
			if detail == "" {
				detail = reasonMissing
			}
			missing = append(missing, res.Agent.Name)
		default: // unknown
			detail = fmt.Sprintf("%s (latest unknown)", safeVersion(res.Current))
			unknownCnt = append(unknownCnt, res.Agent.Name)
		}
		if opts.Explain && res.Method != "" {
			detail = fmt.Sprintf("%s [%s]", detail, ui.MethodLabel(res.Method))
		}
		fmt.Fprintf(os.Stdout, "%-*s  %s\n", nameWidth, res.Agent.Name, detail)
	}
	printSummaryLine("outdated", outdated)
	printSummaryLine("up-to-date", upToDate)
	printSummaryLine("unknown", unknownCnt)
	printSummaryLine("missing", missing)
	if len(unknown) > 0 {
		printSummaryLine("skipped (unknown)", unknown)
	}
}

func safeVersion(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return v
}

func fmtDuration(d time.Duration) string {
	seconds := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%ds", seconds)
}

func printLogs(results []result, opts options) {
	if opts.DryRun {
		return
	}
	type logGroup struct {
		names []string
		log   string
	}
	groups := map[string]*logGroup{}
	order := []string{}

	for _, res := range results {
		if res.Status != statusFailed && !(opts.Verbose && res.Status == statusUpdated) {
			continue
		}
		key := res.UpdateCmd + "\n" + res.Status + "\n" + res.Log
		group := groups[key]
		if group == nil {
			group = &logGroup{log: res.Log}
			groups[key] = group
			order = append(order, key)
		}
		group.names = append(group.names, res.Agent.Name)
	}

	for _, key := range order {
		group := groups[key]
		printLog(strings.Join(group.names, ", "), group.log)
	}
}

func printLog(agentName, log string) {
	fmt.Fprintf(os.Stdout, "==> %s\n", agentName)
	trimmed := strings.TrimSpace(log)
	if trimmed == "" {
		fmt.Fprintln(os.Stdout, "(no output)")
		return
	}
	fmt.Fprintln(os.Stdout, trimmed)
}

func printSummary(results []result, unknown []string) {
	updated := []string{}
	unchanged := []string{}
	skippedMissing := []string{}
	skippedCode := []string{}
	skippedManual := []string{}
	failed := []string{}

	for _, res := range results {
		switch res.Status {
		case statusUpdated:
			updated = append(updated, res.Agent.Name)
		case statusUnchanged:
			unchanged = append(unchanged, res.Agent.Name)
		case statusSkipped:
			switch res.Reason {
			case reasonMissingCode:
				skippedCode = append(skippedCode, res.Agent.Name)
			case reasonManualInstall:
				skippedManual = append(skippedManual, res.Agent.Name)
			default:
				skippedMissing = append(skippedMissing, res.Agent.Name)
			}
		case statusFailed:
			failed = append(failed, res.Agent.Name)
		}
	}

	printSummaryLine("updated", updated)
	printSummaryLine("unchanged", unchanged)
	printSummaryLine("skipped (missing)", skippedMissing)
	printSummaryLine("skipped (missing vscode)", skippedCode)
	printSummaryLine("skipped (manual install)", skippedManual)
	if len(unknown) > 0 {
		printSummaryLine("skipped (unknown)", unknown)
	}
	if len(failed) > 0 {
		printSummaryLine("failed", failed)
	}
}

func printSummaryLine(label string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(os.Stdout, "%s: %s\n", label, strings.Join(items, " "))
}

func hasFailures(results []result) bool {
	for _, res := range results {
		if res.Status == statusFailed {
			return true
		}
	}
	return false
}
