// Package detect probes the environment to decide how each agent is installed:
// which package managers are present, what their global bin dirs and package
// lists contain, installed VS Code extensions, and the latest available versions.
// Results are cached behind per-Env lazy loaders and are safe for concurrent use.
package detect

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chhoumann/uca/internal/agents"
	"github.com/chhoumann/uca/internal/runner"
	"github.com/chhoumann/uca/internal/version"
)

const (
	detectCmdTimeout        = 10 * time.Second
	latestVersionCmdTimeout = 12 * time.Second
	nativeHelpCheckTimeout  = 2 * time.Second
)

// lazy memoizes one value: the first get runs the loader bound at
// construction, every later get returns that same value.
type lazy[T any] struct {
	once sync.Once
	load func() T
	v    T
}

func (l *lazy[T]) get() T {
	l.once.Do(func() {
		l.v = l.load()
		l.load = nil
	})
	return l.v
}

// set forces the value without running the loader. Test hook; call before get.
func (l *lazy[T]) set(v T) {
	l.once.Do(func() {
		l.v = v
		l.load = nil
	})
}

// nodeManager is one global node package manager's per-Env state.
type nodeManager struct {
	installed bool
	binDir    lazy[string]
	pkgs      lazy[map[string]bool]
}

// nodeManagerDefs drives every per-kind dispatch for the global node package
// managers - install probe, global bin dir, global package list, and the
// latest-version CLI query - in resolution order. Adding a manager is one row
// here instead of a switch per operation. The probed binary is the kind itself.
var nodeManagerDefs = []struct {
	kind        string
	binDir      func(*Env) string
	packages    func(*Env) map[string]bool
	latestArgs  func(pkg string) []string
	parseLatest func(out string) string
}{
	{
		kind:        agents.KindNpm,
		binDir:      (*Env).loadNpmBin,
		packages:    (*Env).loadNpmPkgs,
		latestArgs:  func(pkg string) []string { return []string{"npm", "view", pkg, "dist-tags.latest"} },
		parseLatest: version.ParseLatest,
	},
	{
		kind:        agents.KindPnpm,
		binDir:      cmdBinDir("pnpm", "bin", "-g"),
		packages:    (*Env).loadPnpmPkgs,
		latestArgs:  func(pkg string) []string { return []string{"pnpm", "view", pkg, "dist-tags.latest", "--silent"} },
		parseLatest: version.ParseLatest,
	},
	{
		kind:        agents.KindYarn,
		binDir:      cmdBinDir("yarn", "global", "bin"),
		packages:    cmdPackageList("yarn", "global", "list", "--depth=0"),
		latestArgs:  func(pkg string) []string { return []string{"yarn", "info", pkg, "dist-tags.latest", "--silent"} },
		parseLatest: version.ParseLatest,
	},
	{
		kind:     agents.KindBun,
		binDir:   cmdBinDir("bun", "pm", "bin", "-g"),
		packages: cmdPackageList("bun", "pm", "ls", "-g"),
		// `bun info` needs `-g` to work outside of a JS project.
		latestArgs: func(pkg string) []string { return []string{"bun", "info", "-g", pkg, "version", "--json"} },
		// bun emits JSON: a scalar ("6.0.3") or the full manifest object. Parse
		// the top-level version explicitly so a dependency's version isn't
		// mistaken for it.
		parseLatest: func(out string) string {
			if v := version.ParseBunJSON(out); v != "" {
				return v
			}
			return version.ParseLatest(out)
		},
	},
}

// cmdBinDir builds a bin-dir loader that runs one command and trims its output.
func cmdBinDir(args ...string) func(*Env) string {
	return func(e *Env) string {
		out, exitCode, _, _ := runner.RunStdout(e.baseCtx(), args, detectCmdTimeout)
		if exitCode != 0 {
			return ""
		}
		return strings.TrimSpace(out)
	}
}

// cmdPackageList builds a package-list loader that runs one command and parses
// "<name>@<version>" tokens from its output.
func cmdPackageList(args ...string) func(*Env) map[string]bool {
	return func(e *Env) map[string]bool {
		out, exitCode, _, _ := runner.RunStdout(e.baseCtx(), args, detectCmdTimeout)
		if exitCode != 0 {
			return nil
		}
		return parsePackageListOutput(out)
	}
}

// Env holds detected environment state. Construct it with New; its methods are
// safe for concurrent use.
type Env struct {
	ctx context.Context

	hasBrew   bool
	hasUv     bool
	hasPython bool
	codeCmd   string

	node map[string]*nodeManager // keyed by kind, see nodeManagerDefs

	npmPrefixVal lazy[string]
	uvTools      lazy[map[string]bool]
	brewFormulae lazy[map[string]bool]
	pipPkgs      lazy[map[string]bool]
	codeExts     lazy[map[string]string]

	npmRegistry npmRegistryConfig

	mu           sync.Mutex
	binPathCache map[string]string
	helpChecks   map[string]bool
	latestCache  map[string]string
	latestFlight map[string]*latestFlight
}

// New probes which managers/tools are installed and returns a ready Env.
func New(ctx context.Context) *Env {
	e := &Env{
		ctx:          ctx,
		hasBrew:      hasBinary("brew"),
		hasUv:        hasBinary("uv"),
		hasPython:    hasBinary("python3"),
		codeCmd:      detectCodeCmd(),
		node:         make(map[string]*nodeManager, len(nodeManagerDefs)),
		binPathCache: map[string]string{},
		helpChecks:   map[string]bool{},
		latestCache:  map[string]string{},
		latestFlight: map[string]*latestFlight{},
	}
	e.npmPrefixVal.load = e.loadNpmPrefix
	e.uvTools.load = e.loadUvTools
	e.brewFormulae.load = e.loadBrewFormulae
	e.pipPkgs.load = e.loadPipPkgs
	e.codeExts.load = e.loadCodeExtensions
	for _, def := range nodeManagerDefs {
		m := &nodeManager{installed: hasBinary(def.kind)}
		m.binDir.load = func() string {
			if !m.installed {
				return ""
			}
			return def.binDir(e)
		}
		m.pkgs.load = func() map[string]bool {
			if !m.installed {
				return nil
			}
			return def.packages(e)
		}
		e.node[def.kind] = m
	}
	return e
}

// PrewarmNeeds selects which detection loaders Prewarm should kick off, so
// callers only pay for manager probes their agents can actually consult.
type PrewarmNeeds struct {
	Node   bool // npm/pnpm/yarn/bun bin dirs and global package lists
	Brew   bool
	Pip    bool
	Uv     bool
	VSCode bool
}

// Prewarm kicks off the requested detection loaders concurrently so later
// lookups are already populated (the lazy loaders dedupe with on-demand
// callers).
func (e *Env) Prewarm(needs PrewarmNeeds) {
	if needs.Node {
		for _, m := range e.node {
			go m.binDir.get()
			go m.pkgs.get()
		}
	}
	if needs.Uv {
		go e.uvTools.get()
	}
	if needs.Brew {
		go e.brewFormulae.get()
	}
	if needs.Pip {
		go e.pipPkgs.get()
	}
	if needs.VSCode {
		go e.codeExts.get()
	}
}

// Capability accessors (the four flags the resolver reads directly).
func (e *Env) HasBrew() bool   { return e.hasBrew }
func (e *Env) HasPython() bool { return e.hasPython }
func (e *Env) HasUv() bool     { return e.hasUv }
func (e *Env) CodeCmd() string { return e.codeCmd }

func detectCodeCmd() string {
	for _, candidate := range []string{"code", "codium", "code-insiders"} {
		if hasBinary(candidate) {
			return candidate
		}
	}
	return ""
}

func (e *Env) baseCtx() context.Context {
	if e.ctx == nil {
		return context.Background()
	}
	return e.ctx
}

// HasBinary reports whether name resolves on PATH (cached).
func (e *Env) HasBinary(name string) bool {
	return e.binaryPath(name) != ""
}

func hasBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (e *Env) binaryPath(name string) string {
	if name == "" {
		return ""
	}
	e.mu.Lock()
	if path, ok := e.binPathCache[name]; ok {
		e.mu.Unlock()
		return path
	}
	e.mu.Unlock()
	path, err := exec.LookPath(name)
	if err != nil {
		path = ""
	} else {
		path = filepath.Clean(path)
	}
	e.mu.Lock()
	e.binPathCache[name] = path
	e.mu.Unlock()
	return path
}

// HasNodeManager reports whether the given node manager kind is installed.
func (e *Env) HasNodeManager(kind string) bool {
	m := e.node[kind]
	return m != nil && m.installed
}

func (e *Env) nodeBinDir(kind string) string {
	if m := e.node[kind]; m != nil {
		return m.binDir.get()
	}
	return ""
}

func (e *Env) nodeManagerHasPackage(kind, pkg string) bool {
	if m := e.node[kind]; m != nil {
		return m.pkgs.get()[pkg]
	}
	return false
}

// NodeManagerForBinary returns the node manager whose global bin dir contains
// name. When several managers' bin dirs match (nested or symlinked layouts),
// the longest bin dir path wins as the most specific; an exact tie or no match
// returns "".
func (e *Env) NodeManagerForBinary(name string) string {
	binPath := e.binaryPath(name)
	if binPath == "" {
		return ""
	}
	// Canonicalize the binary's location once: its dir as PATH names it, plus
	// the dir of the fully resolved binary (a manager shim is usually a symlink
	// into the manager's store). Each manager dir is then resolved at most once
	// instead of per comparison.
	binDir := filepath.Dir(binPath)
	resolvedBinDir := ""
	if resolvedPath := resolveSymlinkPath(binPath); resolvedPath != "" {
		resolvedBinDir = filepath.Dir(resolvedPath)
	} else {
		resolvedBinDir = resolveSymlinkPath(binDir)
	}
	matches := []string{}
	for _, def := range nodeManagerDefs {
		if !e.HasNodeManager(def.kind) {
			continue
		}
		dir := e.nodeBinDir(def.kind)
		if dir == "" {
			continue
		}
		if binDirMatches(dir, binDir, resolvedBinDir) {
			matches = append(matches, def.kind)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	if len(matches) > 1 {
		bestKind := ""
		bestLen := -1
		tie := false
		for _, kind := range matches {
			dir := e.nodeBinDir(kind)
			if len(dir) > bestLen {
				bestLen = len(dir)
				bestKind = kind
				tie = false
				continue
			}
			if len(dir) == bestLen {
				tie = true
			}
		}
		if !tie {
			return bestKind
		}
	}
	return ""
}

// binDirMatches reports whether a manager bin dir names the same directory as
// the binary's dir (binDir, as PATH looked it up) or its symlink-resolved form.
func binDirMatches(dir, binDir, resolvedBinDir string) bool {
	dir = filepath.Clean(dir)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(dir, binDir) || (resolvedBinDir != "" && strings.EqualFold(dir, resolvedBinDir))
	}
	if dir == binDir || dir == resolvedBinDir {
		return true
	}
	resolvedDir := resolveSymlinkPath(dir)
	return resolvedDir != "" && (resolvedDir == binDir || resolvedDir == resolvedBinDir)
}

// NodeBinHasBinary reports whether the manager's global bin dir contains name.
func (e *Env) NodeBinHasBinary(kind, name string) bool {
	return binDirHasBinary(e.nodeBinDir(kind), name)
}

// NodeManagerForPackage returns the unique node manager whose global package list
// contains pkg, or "" when absent or ambiguous.
func (e *Env) NodeManagerForPackage(pkg string) string {
	if pkg == "" {
		return ""
	}
	matches := []string{}
	for _, def := range nodeManagerDefs {
		if !e.HasNodeManager(def.kind) {
			continue
		}
		if e.nodeManagerHasPackage(def.kind, pkg) {
			matches = append(matches, def.kind)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

// npmPrefix resolves npm's global prefix: from the environment / ~/.npmrc /
// the node binary's location when confidently derivable (see npmfs.go), else
// from `npm prefix -g`.
func (e *Env) npmPrefix() string {
	return e.npmPrefixVal.get()
}

func (e *Env) loadNpmPrefix() string {
	if prefix := npmPrefixFast(); prefix != "" {
		return prefix
	}
	out, exitCode, _, _ := runner.RunStdout(e.baseCtx(), []string{"npm", "prefix", "-g"}, detectCmdTimeout)
	if exitCode != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

func (e *Env) loadNpmBin() string {
	prefix := e.npmPrefix()
	if prefix == "" {
		return ""
	}
	// On Unix-like systems, global binaries are installed under <prefix>/bin.
	// On Windows, global binaries are typically installed directly under <prefix>.
	if runtime.GOOS == "windows" {
		bin := filepath.Join(prefix, "bin")
		if info, err := os.Stat(bin); err == nil && info.IsDir() {
			return bin
		}
		return prefix
	}
	return filepath.Join(prefix, "bin")
}

func (e *Env) loadNpmPkgs() map[string]bool {
	// Fast path: a directory listing of the global node_modules answers "is this
	// package installed" without a ~200ms `npm list` startup.
	if prefix := e.npmPrefix(); prefix != "" {
		if pkgs := listGlobalNodePackages(globalNodeModulesDir(prefix)); pkgs != nil {
			return pkgs
		}
	}
	// `npm list` exits nonzero for dependency problems (extraneous, invalid,
	// missing peers) while still printing the full JSON tree, so the exit code
	// is deliberately ignored; JSON parsing decides.
	out, _, _, _ := runner.RunStdout(e.baseCtx(), []string{"npm", "list", "-g", "--depth=0", "--json"}, detectCmdTimeout)
	var payload struct {
		Dependencies map[string]any `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return nil
	}
	pkgs := make(map[string]bool, len(payload.Dependencies))
	for name := range payload.Dependencies {
		pkgs[name] = true
	}
	return pkgs
}

func (e *Env) loadPnpmPkgs() map[string]bool {
	// Like npm, `pnpm list` can exit nonzero yet still print usable JSON, so
	// the exit code is deliberately ignored; JSON parsing decides.
	out, _, _, _ := runner.RunStdout(e.baseCtx(), []string{"pnpm", "list", "-g", "--depth=0", "--json"}, detectCmdTimeout)
	type pnpmPayload struct {
		Dependencies map[string]any `json:"dependencies"`
	}
	pkgs := map[string]bool{}
	var list []pnpmPayload
	if err := json.Unmarshal([]byte(out), &list); err == nil {
		for _, entry := range list {
			for name := range entry.Dependencies {
				pkgs[name] = true
			}
		}
		return pkgs
	}
	var single pnpmPayload
	if err := json.Unmarshal([]byte(out), &single); err != nil {
		return nil
	}
	for name := range single.Dependencies {
		pkgs[name] = true
	}
	return pkgs
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func binDirHasBinary(binDir, name string) bool {
	if binDir == "" || name == "" {
		return false
	}
	candidates := []string{filepath.Join(binDir, name)}
	if runtime.GOOS == "windows" {
		candidates = append(candidates,
			filepath.Join(binDir, name+".exe"),
			filepath.Join(binDir, name+".cmd"),
			filepath.Join(binDir, name+".bat"),
		)
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return true
		}
	}
	return false
}

func resolveSymlinkPath(path string) string {
	if path == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	return filepath.Clean(resolved)
}

func parsePackageListOutput(out string) map[string]bool {
	pkgs := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		for _, token := range strings.Fields(line) {
			if name := parsePackageFromToken(token); name != "" {
				pkgs[name] = true
			}
		}
	}
	return pkgs
}

func parsePackageFromToken(token string) string {
	if token == "" {
		return ""
	}
	token = strings.Trim(token, "\"'`,")
	token = strings.TrimRight(token, "):,")
	token = strings.TrimLeft(token, "(")
	if !strings.Contains(token, "@") {
		return ""
	}
	idx := strings.LastIndex(token, "@")
	if idx <= 0 || idx == len(token)-1 {
		return ""
	}
	return token[:idx]
}

// UvHas reports whether a uv tool is installed.
func (e *Env) UvHas(pkg string) bool {
	return e.uvTools.get()[pkg]
}

func (e *Env) loadUvTools() map[string]bool {
	if !e.hasUv {
		return nil
	}
	out, _, _, _ := runner.RunStdout(e.baseCtx(), []string{"uv", "tool", "list"}, detectCmdTimeout)
	tools := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		tools[fields[0]] = true
	}
	return tools
}

// BrewHas reports whether a brew formula is installed.
func (e *Env) BrewHas(formula string) bool {
	return e.hasBrew && e.brewFormulae.get()[formula]
}

func (e *Env) loadBrewFormulae() map[string]bool {
	if !e.hasBrew {
		return nil
	}
	// Fast path: an installed formula is a non-empty directory under the Cellar
	// (each subdirectory is an installed version), so one directory listing
	// replaces a `brew list` subprocess (~0.5s of Ruby startup). See brewfs.go.
	if formulae := cellarFormulae(brewCellarDir()); formulae != nil {
		return formulae
	}
	// One `brew list` instead of a `brew list <formula>` per agent (brew is slow
	// to cold-start); each line is "<formula> <version> [more versions]".
	out, exitCode, _, _ := runner.RunStdout(e.baseCtx(), []string{"brew", "list", "--formula", "--versions"}, detectCmdTimeout)
	if exitCode != 0 {
		return nil
	}
	formulae := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) > 0 {
			formulae[fields[0]] = true
		}
	}
	return formulae
}

func normalizePipName(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-")
}

// PipHas reports whether a pip package is installed (name-normalized).
func (e *Env) PipHas(pkg string) bool {
	return e.hasPython && e.pipPkgs.get()[normalizePipName(pkg)]
}

func (e *Env) loadPipPkgs() map[string]bool {
	if !e.hasPython {
		return nil
	}
	// One `pip list` instead of a `pip show <pkg>` per agent. Names are normalized
	// (lowercase, "_"->"-") to match pip's own canonicalization. Unlike npm/pnpm,
	// a nonzero exit means pip itself failed, so it is treated as "no answer".
	out, exitCode, _, _ := runner.RunStdout(e.baseCtx(), []string{"python3", "-m", "pip", "list", "--format", "freeze"}, detectCmdTimeout)
	if exitCode != 0 {
		return nil
	}
	pkgs := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if i := strings.Index(line, "=="); i > 0 {
			pkgs[normalizePipName(line[:i])] = true
		}
	}
	return pkgs
}

// VscodeHas reports whether a VS Code extension is installed.
func (e *Env) VscodeHas(extID string) bool {
	_, ok := e.codeExts.get()[extID]
	return ok
}

// VscodeVersion returns the installed version of a VS Code extension, or "".
func (e *Env) VscodeVersion(extID string) string {
	return e.codeExts.get()[extID]
}

func (e *Env) loadCodeExtensions() map[string]string {
	if e.codeCmd == "" {
		return nil
	}
	// Fast path: read the extensions manifest VS Code itself maintains instead
	// of spawning its CLI (see vscodeext.go).
	if exts := readCodeExtensions(codeExtensionsDir(e.codeCmd)); exts != nil {
		return exts
	}
	out, _, _, _ := runner.RunStdout(e.baseCtx(), []string{e.codeCmd, "--list-extensions", "--show-versions"}, detectCmdTimeout)
	exts := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		idx := strings.LastIndex(line, "@")
		if idx <= 0 {
			continue
		}
		exts[line[:idx]] = line[idx+1:]
	}
	return exts
}

// HelpMatches reports whether `<binary> --help` succeeds and contains the given
// identifying substring (cached). An empty contains always matches.
func (e *Env) HelpMatches(binary, contains string) bool {
	if strings.TrimSpace(contains) == "" {
		return true
	}
	if binary == "" {
		return false
	}
	path := e.binaryPath(binary)
	cacheKey := binary + "\x00" + path + "\x00" + contains
	e.mu.Lock()
	if ok, found := e.helpChecks[cacheKey]; found {
		e.mu.Unlock()
		return ok
	}
	e.mu.Unlock()

	out, exitCode, _, _ := runner.Run(e.baseCtx(), []string{binary, "--help"}, nativeHelpCheckTimeout)
	ok := exitCode == 0 && strings.Contains(out, contains)

	e.mu.Lock()
	e.helpChecks[cacheKey] = ok
	e.mu.Unlock()
	return ok
}

// PrefetchLatest starts registry latest-version lookups for the given packages
// in the background, so the answers are (mostly) ready by the time resolution
// finishes and asks for them. Results land in the same memoized store
// NodeLatestVersion and PeekLatest read.
func (e *Env) PrefetchLatest(ctx context.Context, pkgs []string) {
	for _, pkg := range pkgs {
		if pkg = strings.TrimSpace(pkg); pkg != "" {
			go e.registryLatestOnce(ctx, pkg)
		}
	}
}

type latestFlight struct {
	once sync.Once
	v    string
}

// lookupFlight deduplicates concurrent lookups per key: the first caller runs
// query while concurrent callers wait for that same result. A successful
// (non-empty) flight stays memoized; a failed one is removed - identity-checked
// under the mutex so a newer retry flight is left alone - and a later call runs
// query again (a canceled prefetch must not poison the on-demand path).
func (e *Env) lookupFlight(key string, query func() string) string {
	e.mu.Lock()
	f, ok := e.latestFlight[key]
	if !ok {
		f = &latestFlight{}
		e.latestFlight[key] = f
	}
	e.mu.Unlock()
	f.once.Do(func() {
		f.v = query()
		if f.v == "" {
			e.mu.Lock()
			if e.latestFlight[key] == f {
				delete(e.latestFlight, key)
			}
			e.mu.Unlock()
		}
	})
	return f.v
}

// registryLatestOnce queries the registry over HTTP for a package's latest
// version, deduplicated per package (a prefetch and an on-demand caller share
// one request; the loser of the race just waits for the same result). A
// successful answer lands in latestCache so PeekLatest sees completed
// prefetches.
func (e *Env) registryLatestOnce(ctx context.Context, pkg string) string {
	v := e.lookupFlight(pkg, func() string { return e.registryLatestVersion(ctx, pkg) })
	if v != "" {
		e.mu.Lock()
		e.latestCache[pkg] = v
		e.mu.Unlock()
	}
	return v
}

// PeekLatest returns a node package's latest version only if a lookup has
// already completed, never blocking or spawning. Used where waiting would cost
// more than the update command it might skip (bun's no-op install is faster
// than a registry round-trip).
func (e *Env) PeekLatest(pkg string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.latestCache[strings.TrimSpace(pkg)]
}

// NodeLatestVersion returns the registry "latest" for a node package, memoized
// per package (the answer is manager-independent). Only successful (non-empty)
// results are cached.
func (e *Env) NodeLatestVersion(ctx context.Context, kind, pkg string) string {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		return ""
	}
	e.mu.Lock()
	v := e.latestCache[pkg]
	e.mu.Unlock()
	if v != "" {
		return v
	}
	// Fast path: ask the registry directly (no manager-CLI startup); usually
	// already in flight via PrefetchLatest. The CLI query remains as fallback
	// for registries the HTTP path can't serve.
	if v := e.registryLatestOnce(ctx, pkg); v != "" {
		return v // already memoized by registryLatestOnce
	}
	v = queryNodeLatestVersion(ctx, kind, pkg)
	if v != "" {
		e.mu.Lock()
		e.latestCache[pkg] = v
		e.mu.Unlock()
	}
	return v
}

func queryNodeLatestVersion(ctx context.Context, kind, pkg string) string {
	for _, def := range nodeManagerDefs {
		if def.kind != kind {
			continue
		}
		out, exitCode, _, _ := runner.RunStdout(ctx, def.latestArgs(pkg), latestVersionCmdTimeout)
		if exitCode != 0 {
			return ""
		}
		return def.parseLatest(out)
	}
	return ""
}

// LatestVersion returns the latest available version for an update method, or ""
// when it is not cheaply/reliably knowable (native/pip/uv). Used by --check.
// For vscode, pkg is the extension ID.
func (e *Env) LatestVersion(ctx context.Context, method, pkg string) string {
	switch method {
	case agents.KindNpm, agents.KindPnpm, agents.KindYarn, agents.KindBun:
		return e.NodeLatestVersion(ctx, method, pkg)
	case agents.KindBrew:
		return e.brewLatest(ctx, pkg)
	case agents.KindVSCode:
		return e.VSCodeMarketplaceLatest(ctx, pkg)
	default:
		return ""
	}
}
