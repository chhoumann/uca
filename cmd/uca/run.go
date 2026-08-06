// Update orchestration: resolve agents, batch node updates, run commands,
// and stream lifecycle events to the optional live UI.
package main

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chhoumann/uca/internal/agents"
	"github.com/chhoumann/uca/internal/agentspec"
	"github.com/chhoumann/uca/internal/detect"
	"github.com/chhoumann/uca/internal/runner"
	"github.com/chhoumann/uca/internal/ui"
	"github.com/chhoumann/uca/internal/vercache"
	"github.com/chhoumann/uca/internal/version"
)

const (
	dryRunPreviewBudget = 12 * time.Second
	resolveConcurrency  = 16
)

const versionCmdTimeout = 10 * time.Second

// verCache memoizes version-command output across runs, keyed by the binary's
// identity so any update invalidates it. Nil (e.g. in tests, or when disabled)
// is a no-op.
var verCache *vercache.Cache

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
	agent      agents.Agent
	index      int
	show       bool
	method     string
	explain    string
	reason     string
	versionCmd []string
	// pkg is the resolved package/formula/extension identifier the update
	// targets (used for latest-version lookups and skip-if-current).
	pkg             string
	nodePackageName string
	// nodePackageVersion is the pinned version (empty = @latest). A pinned node
	// agent is excluded from batching, since the batch assumes a uniform @latest.
	nodePackageVersion string
	// updateCmd is the final command to run (may be a batch command).
	updateCmd []string
	// updateCmdSingle is the per-agent command (used for fallback when batch updates fail).
	updateCmdSingle []string
	// beforeVersion delivers the pre-update version lookup, launched already
	// during resolution so a slow CLI startup overlaps detection (and other
	// agents' resolution) instead of serializing after it. The lookup is always
	// spawned before the agent's own update command runs, so it reads the
	// pre-mutation binary. previewLatest likewise delivers the dry-run registry
	// lookup. Both channels are buffered and written exactly once.
	beforeVersion <-chan string
	previewLatest <-chan string
	// dispatched marks an agent whose task was sent to the workers during
	// resolution (before the resolve barrier); the post-barrier loops must not
	// emit its events or build a task for it again.
	dispatched bool
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
	var previewCtx context.Context
	if opts.DryRun {
		var previewCancel context.CancelFunc
		previewCtx, previewCancel = context.WithTimeout(ctx, dryRunPreviewBudget)
		defer previewCancel()
	}

	// Workers start before resolution so a non-node agent's update can begin the
	// moment its own resolve completes, instead of waiting out the global
	// barrier (the slowest manager probe). Node agents still wait for the
	// barrier: batching needs every node resolution. Serial mode keeps the old
	// everything-after-the-barrier flow so its execution order stays
	// deterministic. Dry-run runs no updates at all.
	earlyDispatch := !opts.DryRun && !opts.Serial
	locker := newManagerLocker()
	taskCh := make(chan updateTask, len(selected))
	var workerWG sync.WaitGroup
	if !opts.DryRun {
		workerCount := effectiveConcurrency(opts, len(selected))
		if workerCount > len(selected) && len(selected) > 0 {
			workerCount = len(selected)
		}
		if workerCount < 1 {
			workerCount = 1
		}
		workerWG.Add(workerCount)
		for i := 0; i < workerCount; i++ {
			go func() {
				defer workerWG.Done()
				for task := range taskCh {
					runTask(ctx, task, env, opts, locker, events, results)
				}
			}()
		}
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
				show:            resolved.Cmd != nil || resolved.Reason == agents.ReasonManualInstall,
				method:          resolved.Method,
				explain:         resolved.Detail,
				reason:          resolved.Reason,
				versionCmd:      resolved.VersionCmd,
				pkg:             resolved.Pkg,
				updateCmdSingle: resolved.Cmd,
			}
			if agents.IsNodeKind(resolved.Method) {
				work.nodePackageName = agentspec.NodePackageName(agent.Strategies)
				work.nodePackageVersion = resolved.Version
			}
			if work.updateCmdSingle != nil {
				// Start the pre-update version lookup now (one channel write);
				// runTask / dryRunResults collect it after the resolve barrier.
				versionCtx := ctx
				if opts.DryRun {
					versionCtx = previewCtx
				}
				before := make(chan string, 1)
				work.beforeVersion = before
				go func(work agentWork) {
					before <- getVersion(versionCtx, work.agent, env, work.method, work.versionCmd)
				}(work)
				if opts.DryRun {
					latest := make(chan string, 1)
					work.previewLatest = latest
					go func(work agentWork) {
						if agents.IsNodeKind(work.method) {
							latest <- env.NodeLatestVersion(previewCtx, work.method, work.nodePackageName)
							return
						}
						latest <- ""
					}(work)
				}
				if earlyDispatch && !agents.IsNodeKind(work.method) {
					work.updateCmd = work.updateCmdSingle
					work.dispatched = true
					if events != nil {
						events <- updateEvent{Index: work.index, Phase: agents.PhaseDetect, Result: work.baseResult(), Time: time.Now(), Show: work.show}
					}
					works[i] = work
					taskCh <- updateTask{kind: work.method, cmd: work.updateCmd, agents: []agentWork{work}}
					return
				}
			}
			works[i] = work
		}(i, agent)
	}
	resolveWG.Wait()

	// Build the remaining tasks (batch node updates by manager kind); agents
	// already dispatched during resolution are excluded.
	tasks := []updateTask{}
	nodeGroups := map[string][]int{}
	for i := range works {
		work := &works[i]
		if work.updateCmdSingle == nil || work.dispatched {
			continue
		}
		if agents.IsNodeKind(work.method) {
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
			// A version-pinned agent can't join the @latest batch - run it on its
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
		return dryRunResults(works, results, events, now)
	}

	for i := range works {
		if works[i].dispatched {
			continue // detect event already emitted during resolution
		}
		emitResolved(&works[i], results, events, now)
	}

	for _, task := range tasks {
		taskCh <- task
	}
	close(taskCh)
	workerWG.Wait()

	return results
}

// dryRunResults assembles the dry-run preview. The version lookups were already
// launched during resolution (see the previewBefore/previewLatest channels), so
// this emits the detect events, then collects each agent's lookups and emits its
// finish event. Wall-clock is ~max(resolve barrier, slowest single lookup)
// instead of the sum across agents.
func dryRunResults(works []agentWork, results []result, events chan<- updateEvent, now time.Time) []result {
	updatable := make([]*agentWork, 0, len(works))
	for i := range works {
		if emitResolved(&works[i], results, events, now) {
			updatable = append(updatable, &works[i])
		}
	}

	for _, work := range updatable {
		res := work.baseResult()
		res.Status = agents.StatusUpdated
		res.Reason = agents.ReasonDryRun
		res.Before = "unknown"
		if work.beforeVersion != nil {
			res.Before = <-work.beforeVersion
		}
		res.After = res.Before
		if work.previewLatest != nil {
			if latest := <-work.previewLatest; latest != "" {
				if formatted := version.FormatWithToken(res.Before, latest); formatted != "" {
					res.After = formatted
				} else {
					res.After = latest
				}
			}
		}
		results[work.index] = res
		if events != nil {
			events <- updateEvent{Index: work.index, Phase: agents.PhaseFinish, Result: res, Time: time.Now(), Show: work.show}
		}
	}
	return results
}

// baseResult seeds a result with the fields known at resolve time.
func (w *agentWork) baseResult() result {
	return result{
		Agent:     w.agent,
		Method:    w.method,
		Explain:   w.explain,
		UpdateCmd: cmdString(w.updateCmd),
	}
}

// emitResolved records a resolved agent's detect event, or its full skipped
// result (detect+finish) when it has no update command. Reports whether the
// agent has an update to run.
func emitResolved(work *agentWork, results []result, events chan<- updateEvent, now time.Time) bool {
	res := work.baseResult()
	if work.updateCmdSingle == nil {
		res.Status = agents.StatusSkipped
		res.Reason = work.reason
		if res.Reason == "" {
			res.Reason = agents.ReasonMissing
		}
		results[work.index] = res
		if events != nil {
			events <- updateEvent{Index: work.index, Phase: agents.PhaseDetect, Result: res, Time: now, Show: work.show}
			events <- updateEvent{Index: work.index, Phase: agents.PhaseFinish, Result: res, Time: now, Show: work.show}
		}
		return false
	}
	if events != nil {
		events <- updateEvent{Index: work.index, Phase: agents.PhaseDetect, Result: res, Time: now, Show: work.show}
	}
	return true
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

	// Prepare results and emit start events. The pre-update version lookups were
	// launched during resolution (see beforeVersion); collect them here.
	prepared := make([]result, len(task.agents))
	for i, work := range task.agents {
		prepared[i] = work.baseResult()
		if work.beforeVersion != nil {
			prepared[i].Before = <-work.beforeVersion
		} else {
			prepared[i].Before = getVersion(ctx, work.agent, env, work.method, work.versionCmd)
		}
	}
	if events != nil && agents.IsNodeKind(kind) {
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
	// Skip the update command entirely when every agent in the task is provably
	// already at the latest version (exact metadata only; see taskUpToDate). A
	// no-op update still costs 0.5-2s of manager startup, so this is the big
	// win on the common nothing-to-do run. --force restores the old behavior.
	if !opts.Force && ctx.Err() == nil && taskUpToDate(ctx, task, env) {
		now := time.Now()
		for i, work := range task.agents {
			res := prepared[i]
			res.After = res.Before
			res.Status = agents.StatusUnchanged
			res.Explain = appendHint(res.Explain, "already at latest; use --force to run the update anyway")
			results[work.index] = res
			if events != nil {
				events <- updateEvent{Index: work.index, Phase: agents.PhaseFinish, Result: res, Time: now, Show: work.show}
			}
		}
		return
	}

	startTime := time.Now()
	if events != nil {
		for i, work := range task.agents {
			events <- updateEvent{Index: work.index, Phase: agents.PhaseStart, Result: prepared[i], Time: startTime, Show: work.show}
		}
	}

	out, classifyOut, exitCode, duration, _ := runUpdateCmd(ctx, task.cmd, opts.Timeout)

	// If a batched node update fails, fall back to per-package updates so we can still make progress and
	// attribute failures precisely.
	if exitCode != 0 && len(task.agents) > 1 && agents.IsNodeKind(kind) {
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
			} else if res.Before != "" && res.After != "" && version.Same(res.Before, res.After) && res.Before != "unknown" {
				res.Status = agents.StatusUnchanged
			} else {
				res.Status = agents.StatusUpdated
			}
			results[work.index] = res
			if events != nil {
				events <- updateEvent{Index: work.index, Phase: agents.PhaseFinish, Result: res, Time: time.Now(), Show: work.show}
			}
		}
		return
	}

	// Batch success or non-batch failure path.
	afters := make([]string, len(task.agents))
	var afterWG sync.WaitGroup
	for i, work := range task.agents {
		afterWG.Add(1)
		go func(i int, work agentWork) {
			defer afterWG.Done()
			afters[i] = getVersion(ctx, work.agent, env, work.method, work.versionCmd)
		}(i, work)
	}
	afterWG.Wait()
	for i, work := range task.agents {
		res := prepared[i]
		res.Duration = duration
		res.Log = out
		res.After = afters[i]

		if exitCode != 0 {
			setFailureResult(&res, exitCode, task.cmd, classifyOut, opts.Timeout)
		} else if res.Before != "" && res.After != "" && version.Same(res.Before, res.After) && res.Before != "unknown" {
			res.Status = agents.StatusUnchanged
		} else {
			res.Status = agents.StatusUpdated
		}
		results[work.index] = res
		if events != nil {
			events <- updateEvent{Index: work.index, Phase: agents.PhaseFinish, Result: res, Time: time.Now(), Show: work.show}
		}
	}
}

// taskUpToDate reports whether every agent in the task is provably already at
// its target version, using only exact metadata: the manager's own installed
// records (global package.json, extensions manifest) against an authoritative
// latest (npm registry, marketplace). Any gap in either side fails open (run
// the update command). pnpm/yarn global package dirs are not confidently
// derivable; native/pip/uv have no cheap authoritative latest; and brew's
// locally-cloned tap formula has no freshness guarantee (only `brew update`,
// which `brew upgrade` runs implicitly, refreshes it - so skipping the upgrade
// on tap data would keep the tap stale forever). Those always run.
func taskUpToDate(ctx context.Context, task updateTask, env *detect.Env) bool {
	for _, work := range task.agents {
		switch {
		case agents.IsNodeKind(work.method):
			installed := env.NodeInstalledVersion(work.method, work.nodePackageName)
			if installed == "" {
				return false
			}
			if pin := strings.TrimSpace(work.nodePackageVersion); pin != "" {
				// A pin can be a downgrade target, so only exact equality skips.
				if version.Compare(installed, pin) != 0 {
					return false
				}
				continue
			}
			var latest string
			if work.method == agents.KindBun {
				// bun's no-op install is faster than a registry round-trip, so
				// only use an answer that has already arrived.
				latest = env.PeekLatest(work.nodePackageName)
			} else {
				latest = env.NodeLatestVersion(ctx, work.method, work.nodePackageName)
			}
			if compareVersions(installed, latest) != checkUpToDate {
				return false
			}
		case work.method == agents.KindVSCode:
			installed := env.VscodeVersion(work.pkg)
			latest := env.VSCodeMarketplaceLatest(ctx, work.pkg)
			if installed == "" || compareVersions(installed, latest) != checkUpToDate {
				return false
			}
		default:
			return false
		}
	}
	return len(task.agents) > 0
}

type updateEvent struct {
	Index  int
	Phase  string
	Result result
	Time   time.Time
	Show   bool
}

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
	renderer.HideCursor()
	totalAgents := len(selected)
	detected := make([]bool, len(selected))
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
				if ev.Phase == agents.PhaseDetect && !detected[ev.Index] {
					detected[ev.Index] = true
					detectedCount++
				}
				ui.ApplyEvent(&rows[ev.Index], toUIEvent(ev))
				renderer.Draw(renderer.RenderFrame(rows, nameWidth, start, opts.Explain, detectedCount, totalAgents))
			case <-ticker.C:
				renderer.Draw(renderer.RenderFrame(rows, nameWidth, start, opts.Explain, detectedCount, totalAgents))
			}
		}
	}()

	results := runAllWithEvents(ctx, selected, env, opts, events)
	close(events)
	<-done
	renderer.ShowCursor()
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

func getVersion(ctx context.Context, agent agents.Agent, env *detect.Env, method string, versionCmd []string) string {
	if method == agents.KindVSCode && agent.ExtensionID != "" {
		if version := env.VscodeVersion(agent.ExtensionID); version != "" {
			return version
		}
	}
	if len(versionCmd) > 0 {
		return runVersionCmd(ctx, versionCmd, !agent.DisableVersionCache)
	}
	if len(agent.VersionCmd) > 0 {
		if agent.Binary == "" || env.HasBinary(agent.Binary) {
			return runVersionCmd(ctx, agent.VersionCmd, !agent.DisableVersionCache)
		}
	}
	if agent.ExtensionID != "" {
		if version := env.VscodeVersion(agent.ExtensionID); version != "" {
			return version
		}
	}
	return "unknown"
}

func runVersionCmd(ctx context.Context, args []string, useCache bool) string {
	if len(args) == 0 {
		return "unknown"
	}
	if useCache {
		if v, ok := verCache.Get(args); ok {
			return v
		}
	}
	out, code, _, _ := runner.Run(ctx, args, versionCmdTimeout)
	if code != 0 {
		return "unknown"
	}
	v := version.ParseOutput(out)
	if useCache {
		verCache.Put(args, v)
	}
	return v
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
