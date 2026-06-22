// Package detect probes the environment to decide how each agent is installed:
// which package managers are present, what their global bin dirs and package
// lists contain, installed VS Code extensions, and the latest available versions.
// Results are cached behind sync.Once loaders and are safe for concurrent use.
package detect

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	exec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chhoumann/uca/internal/agents"
	runner "github.com/chhoumann/uca/internal/exec"
	"github.com/chhoumann/uca/internal/version"
)

const (
	detectCmdTimeout        = 10 * time.Second
	latestVersionCmdTimeout = 12 * time.Second
	nativeHelpCheckTimeout  = 2 * time.Second
)

// Env holds detected environment state. Construct it with New; its methods are
// safe for concurrent use.
type Env struct {
	ctx context.Context

	hasBun    bool
	hasBrew   bool
	hasNpm    bool
	hasPnpm   bool
	hasYarn   bool
	hasUv     bool
	hasPython bool
	codeCmd   string

	mu           sync.Mutex
	binPathCache map[string]string
	npmBinOnce   sync.Once
	npmBin       string
	npmPkgOnce   sync.Once
	npmPkgs      map[string]bool
	pnpmBinOnce  sync.Once
	pnpmBin      string
	pnpmPkgOnce  sync.Once
	pnpmPkgs     map[string]bool
	yarnBinOnce  sync.Once
	yarnBin      string
	yarnPkgOnce  sync.Once
	yarnPkgs     map[string]bool
	bunBinOnce   sync.Once
	bunGlobalBin string
	bunPkgOnce   sync.Once
	bunPkgs      map[string]bool
	uvOnce       sync.Once
	uvTools      map[string]bool
	codeOnce     sync.Once
	codeExts     map[string]string
	helpChecks   map[string]bool
	latestCache  map[string]string
	brewOnce     sync.Once
	brewFormulae map[string]bool
	pipOnce      sync.Once
	pipPkgs      map[string]bool
}

// New probes which managers/tools are installed and returns a ready Env.
func New(ctx context.Context) *Env {
	return &Env{
		ctx:          ctx,
		hasBun:       hasBinary("bun"),
		hasBrew:      hasBinary("brew"),
		hasNpm:       hasBinary("npm"),
		hasPnpm:      hasBinary("pnpm"),
		hasYarn:      hasBinary("yarn"),
		hasUv:        hasBinary("uv"),
		hasPython:    hasBinary("python3"),
		codeCmd:      detectCodeCmd(),
		binPathCache: map[string]string{},
		helpChecks:   map[string]bool{},
	}
}

// Prewarm kicks off every detection loader concurrently so later lookups are
// already populated (the sync.Once loaders dedupe with on-demand callers).
func (e *Env) Prewarm() {
	go func() { e.npmBinOnce.Do(e.loadNpmBin) }()
	go func() { e.npmPkgOnce.Do(e.loadNpmPkgs) }()
	go func() { e.pnpmBinOnce.Do(e.loadPnpmBin) }()
	go func() { e.pnpmPkgOnce.Do(e.loadPnpmPkgs) }()
	go func() { e.yarnBinOnce.Do(e.loadYarnBin) }()
	go func() { e.yarnPkgOnce.Do(e.loadYarnPkgs) }()
	go func() { e.bunBinOnce.Do(e.loadBunGlobalBin) }()
	go func() { e.bunPkgOnce.Do(e.loadBunPkgs) }()
	go func() { e.uvOnce.Do(e.loadUvTools) }()
	go func() { e.brewOnce.Do(e.loadBrewFormulae) }()
	go func() { e.pipOnce.Do(e.loadPipPkgs) }()
	go func() { e.codeOnce.Do(e.loadCodeExtensions) }()
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
	if e == nil || e.ctx == nil {
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
	switch kind {
	case agents.KindNpm:
		return e.hasNpm
	case agents.KindPnpm:
		return e.hasPnpm
	case agents.KindYarn:
		return e.hasYarn
	case agents.KindBun:
		return e.hasBun
	default:
		return false
	}
}

// NodeManagerForBinary returns the node manager whose global bin dir contains
// name, or "" when it is absent or ambiguous.
func (e *Env) NodeManagerForBinary(name string) string {
	binPath := e.binaryPath(name)
	if binPath == "" {
		return ""
	}
	binDir := filepath.Dir(binPath)
	resolvedBinDir := ""
	if resolvedPath := resolveSymlinkPath(binPath); resolvedPath != "" {
		resolvedBinDir = filepath.Dir(resolvedPath)
	}
	matches := []string{}
	for _, kind := range []string{agents.KindNpm, agents.KindPnpm, agents.KindYarn, agents.KindBun} {
		if !e.HasNodeManager(kind) {
			continue
		}
		dir := e.nodeBinDir(kind)
		if dir == "" {
			continue
		}
		if samePath(dir, binDir) || (resolvedBinDir != "" && samePath(dir, resolvedBinDir)) {
			matches = append(matches, kind)
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

// NodeBinHasBinary reports whether the manager's global bin dir contains name.
func (e *Env) NodeBinHasBinary(kind, name string) bool {
	return binDirHasBinary(e.nodeBinDir(kind), name)
}

func (e *Env) nodeBinDir(kind string) string {
	switch kind {
	case agents.KindNpm:
		return e.npmBinDir()
	case agents.KindPnpm:
		return e.pnpmBinDir()
	case agents.KindYarn:
		return e.yarnBinDir()
	case agents.KindBun:
		return e.bunGlobalBinDir()
	default:
		return ""
	}
}

// NodeManagerForPackage returns the unique node manager whose global package list
// contains pkg, or "" when absent or ambiguous.
func (e *Env) NodeManagerForPackage(pkg string) string {
	if pkg == "" {
		return ""
	}
	matches := []string{}
	for _, kind := range []string{agents.KindNpm, agents.KindPnpm, agents.KindYarn, agents.KindBun} {
		if !e.HasNodeManager(kind) {
			continue
		}
		if e.nodeManagerHasPackage(kind, pkg) {
			matches = append(matches, kind)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

func (e *Env) nodeManagerHasPackage(kind, pkg string) bool {
	switch kind {
	case agents.KindNpm:
		return e.npmHas(pkg)
	case agents.KindPnpm:
		return e.pnpmHas(pkg)
	case agents.KindYarn:
		return e.yarnHas(pkg)
	case agents.KindBun:
		return e.bunHas(pkg)
	default:
		return false
	}
}

func (e *Env) npmBinDir() string {
	e.npmBinOnce.Do(e.loadNpmBin)
	return e.npmBin
}

func (e *Env) loadNpmBin() {
	e.npmBin = ""
	if !e.hasNpm {
		return
	}
	// Both probes share one budget so a hung npm can't burn two full timeouts.
	probeCtx, cancel := context.WithTimeout(e.baseCtx(), detectCmdTimeout)
	defer cancel()
	out, exitCode, _, _ := runner.RunStdout(probeCtx, []string{"npm", "bin", "-g"}, 0)
	if exitCode == 0 {
		if dir := strings.TrimSpace(out); dir != "" {
			e.npmBin = dir
			return
		}
	}

	// npm v11 removed `npm bin`, but `npm prefix -g` still works.
	prefixOut, exitCode, _, _ := runner.RunStdout(probeCtx, []string{"npm", "prefix", "-g"}, 0)
	if exitCode != 0 {
		return
	}
	prefix := strings.TrimSpace(prefixOut)
	if prefix == "" {
		return
	}
	// On Unix-like systems, global binaries are installed under <prefix>/bin.
	// On Windows, global binaries are typically installed directly under <prefix>.
	if runtime.GOOS == "windows" {
		bin := filepath.Join(prefix, "bin")
		if info, err := os.Stat(bin); err == nil && info.IsDir() {
			e.npmBin = bin
			return
		}
		e.npmBin = prefix
		return
	}
	e.npmBin = filepath.Join(prefix, "bin")
}

func (e *Env) npmHas(pkg string) bool {
	e.npmPkgOnce.Do(e.loadNpmPkgs)
	return e.npmPkgs[pkg]
}

func (e *Env) loadNpmPkgs() {
	e.npmPkgs = map[string]bool{}
	if !e.hasNpm {
		return
	}
	out, _, _, _ := runner.RunStdout(e.baseCtx(), []string{"npm", "list", "-g", "--depth=0", "--json"}, detectCmdTimeout)
	var payload struct {
		Dependencies map[string]any `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return
	}
	for name := range payload.Dependencies {
		e.npmPkgs[name] = true
	}
}

func (e *Env) pnpmBinDir() string {
	e.pnpmBinOnce.Do(e.loadPnpmBin)
	return e.pnpmBin
}

func (e *Env) loadPnpmBin() {
	e.pnpmBin = ""
	if !e.hasPnpm {
		return
	}
	out, exitCode, _, _ := runner.RunStdout(e.baseCtx(), []string{"pnpm", "bin", "-g"}, detectCmdTimeout)
	if exitCode != 0 {
		return
	}
	e.pnpmBin = strings.TrimSpace(out)
}

func (e *Env) pnpmHas(pkg string) bool {
	e.pnpmPkgOnce.Do(e.loadPnpmPkgs)
	return e.pnpmPkgs[pkg]
}

func (e *Env) loadPnpmPkgs() {
	e.pnpmPkgs = map[string]bool{}
	if !e.hasPnpm {
		return
	}
	out, _, _, _ := runner.RunStdout(e.baseCtx(), []string{"pnpm", "list", "-g", "--depth=0", "--json"}, detectCmdTimeout)
	type pnpmPayload struct {
		Dependencies map[string]any `json:"dependencies"`
	}
	var list []pnpmPayload
	if err := json.Unmarshal([]byte(out), &list); err == nil {
		for _, entry := range list {
			for name := range entry.Dependencies {
				e.pnpmPkgs[name] = true
			}
		}
		return
	}
	var single pnpmPayload
	if err := json.Unmarshal([]byte(out), &single); err != nil {
		return
	}
	for name := range single.Dependencies {
		e.pnpmPkgs[name] = true
	}
}

func (e *Env) yarnBinDir() string {
	e.yarnBinOnce.Do(e.loadYarnBin)
	return e.yarnBin
}

func (e *Env) loadYarnBin() {
	e.yarnBin = ""
	if !e.hasYarn {
		return
	}
	out, exitCode, _, _ := runner.RunStdout(e.baseCtx(), []string{"yarn", "global", "bin"}, detectCmdTimeout)
	if exitCode != 0 {
		return
	}
	e.yarnBin = strings.TrimSpace(out)
}

func (e *Env) yarnHas(pkg string) bool {
	e.yarnPkgOnce.Do(e.loadYarnPkgs)
	return e.yarnPkgs[pkg]
}

func (e *Env) loadYarnPkgs() {
	e.yarnPkgs = map[string]bool{}
	if !e.hasYarn {
		return
	}
	out, exitCode, _, _ := runner.RunStdout(e.baseCtx(), []string{"yarn", "global", "list", "--depth=0"}, detectCmdTimeout)
	if exitCode != 0 {
		return
	}
	for name := range parsePackageListOutput(out) {
		e.yarnPkgs[name] = true
	}
}

func (e *Env) bunGlobalBinDir() string {
	e.bunBinOnce.Do(e.loadBunGlobalBin)
	return e.bunGlobalBin
}

func (e *Env) loadBunGlobalBin() {
	e.bunGlobalBin = ""
	if !e.hasBun {
		return
	}
	out, exitCode, _, _ := runner.RunStdout(e.baseCtx(), []string{"bun", "pm", "bin", "-g"}, detectCmdTimeout)
	if exitCode != 0 {
		return
	}
	e.bunGlobalBin = strings.TrimSpace(out)
}

func (e *Env) bunHas(pkg string) bool {
	e.bunPkgOnce.Do(e.loadBunPkgs)
	return e.bunPkgs[pkg]
}

func (e *Env) loadBunPkgs() {
	e.bunPkgs = map[string]bool{}
	if !e.hasBun {
		return
	}
	out, exitCode, _, _ := runner.RunStdout(e.baseCtx(), []string{"bun", "pm", "ls", "-g"}, detectCmdTimeout)
	if exitCode != 0 {
		return
	}
	for name := range parsePackageListOutput(out) {
		e.bunPkgs[name] = true
	}
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

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	if a == b {
		return true
	}
	ra := resolveSymlinkPath(a)
	rb := resolveSymlinkPath(b)
	if ra != "" && rb != "" {
		return ra == rb
	}
	if ra != "" && ra == b {
		return true
	}
	if rb != "" && rb == a {
		return true
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
	e.uvOnce.Do(e.loadUvTools)
	return e.uvTools[pkg]
}

func (e *Env) loadUvTools() {
	e.uvTools = map[string]bool{}
	if !e.hasUv {
		return
	}
	out, _, _, _ := runner.RunStdout(e.baseCtx(), []string{"uv", "tool", "list"}, detectCmdTimeout)
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
		e.uvTools[fields[0]] = true
	}
}

// BrewHas reports whether a brew formula is installed.
func (e *Env) BrewHas(formula string) bool {
	if !e.hasBrew {
		return false
	}
	e.brewOnce.Do(e.loadBrewFormulae)
	return e.brewFormulae[formula]
}

func (e *Env) loadBrewFormulae() {
	e.brewFormulae = map[string]bool{}
	if !e.hasBrew {
		return
	}
	// One `brew list` instead of a `brew list <formula>` per agent (brew is slow
	// to cold-start); each line is "<formula> <version> [more versions]".
	out, exitCode, _, _ := runner.RunStdout(e.baseCtx(), []string{"brew", "list", "--formula", "--versions"}, detectCmdTimeout)
	if exitCode != 0 {
		return
	}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) > 0 {
			e.brewFormulae[fields[0]] = true
		}
	}
}

func normalizePipName(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-")
}

// PipHas reports whether a pip package is installed (name-normalized).
func (e *Env) PipHas(pkg string) bool {
	if !e.hasPython {
		return false
	}
	e.pipOnce.Do(e.loadPipPkgs)
	return e.pipPkgs[normalizePipName(pkg)]
}

func (e *Env) loadPipPkgs() {
	e.pipPkgs = map[string]bool{}
	if !e.hasPython {
		return
	}
	// One `pip list` instead of a `pip show <pkg>` per agent. Names are normalized
	// (lowercase, "_"->"-") to match pip's own canonicalization.
	out, exitCode, _, _ := runner.RunStdout(e.baseCtx(), []string{"python3", "-m", "pip", "list", "--format", "freeze"}, detectCmdTimeout)
	if exitCode != 0 {
		return
	}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if i := strings.Index(line, "=="); i > 0 {
			e.pipPkgs[normalizePipName(line[:i])] = true
		}
	}
}

// VscodeHas reports whether a VS Code extension is installed.
func (e *Env) VscodeHas(extID string) bool {
	e.codeOnce.Do(e.loadCodeExtensions)
	_, ok := e.codeExts[extID]
	return ok
}

// VscodeVersion returns the installed version of a VS Code extension, or "".
func (e *Env) VscodeVersion(extID string) string {
	e.codeOnce.Do(e.loadCodeExtensions)
	return e.codeExts[extID]
}

func (e *Env) loadCodeExtensions() {
	e.codeExts = map[string]string{}
	if e.codeCmd == "" {
		return
	}
	out, _, _, _ := runner.RunStdout(e.baseCtx(), []string{e.codeCmd, "--list-extensions", "--show-versions"}, detectCmdTimeout)
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
		id := line[:idx]
		ver := line[idx+1:]
		e.codeExts[id] = ver
	}
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
	if e.helpChecks != nil {
		if ok, found := e.helpChecks[cacheKey]; found {
			e.mu.Unlock()
			return ok
		}
	} else {
		e.helpChecks = map[string]bool{}
	}
	e.mu.Unlock()

	out, exitCode, _, _ := runner.Run(e.baseCtx(), []string{binary, "--help"}, nativeHelpCheckTimeout)
	ok := exitCode == 0 && strings.Contains(out, contains)

	e.mu.Lock()
	e.helpChecks[cacheKey] = ok
	e.mu.Unlock()
	return ok
}

// NodeLatestVersion returns the registry "latest" for a node package, memoized
// per (kind,pkg). Only successful (non-empty) results are cached.
func (e *Env) NodeLatestVersion(ctx context.Context, kind, pkg string) string {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		return ""
	}
	key := kind + "\x00" + pkg
	e.mu.Lock()
	if e.latestCache != nil {
		if v, ok := e.latestCache[key]; ok {
			e.mu.Unlock()
			return v
		}
	}
	e.mu.Unlock()

	v := queryNodeLatestVersion(ctx, kind, pkg)
	if v != "" {
		e.mu.Lock()
		if e.latestCache == nil {
			e.latestCache = map[string]string{}
		}
		e.latestCache[key] = v
		e.mu.Unlock()
	}
	return v
}

func queryNodeLatestVersion(ctx context.Context, kind, pkg string) string {
	var args []string
	switch kind {
	case agents.KindNpm:
		args = []string{"npm", "view", pkg, "dist-tags.latest"}
	case agents.KindPnpm:
		args = []string{"pnpm", "view", pkg, "dist-tags.latest", "--silent"}
	case agents.KindYarn:
		args = []string{"yarn", "info", pkg, "dist-tags.latest", "--silent"}
	case agents.KindBun:
		// `bun info` needs `-g` to work outside of a JS project.
		args = []string{"bun", "info", "-g", pkg, "version", "--json"}
	default:
		return ""
	}

	out, exitCode, _, _ := runner.RunStdout(ctx, args, latestVersionCmdTimeout)
	if exitCode != 0 {
		return ""
	}
	// bun emits JSON: a scalar ("6.0.3") or the full manifest object. Parse the
	// top-level version explicitly so a dependency's version isn't mistaken for it.
	if kind == agents.KindBun {
		if v := version.ParseBunJSON(out); v != "" {
			return v
		}
	}
	return version.ParseLatest(out)
}

// LatestVersion returns the latest available version for an update method, or ""
// when it is not cheaply/reliably knowable (native/VS Code/pip/uv). Used by --check.
func (e *Env) LatestVersion(ctx context.Context, method, pkg string) string {
	switch method {
	case agents.KindNpm, agents.KindPnpm, agents.KindYarn, agents.KindBun:
		return e.NodeLatestVersion(ctx, method, pkg)
	case agents.KindBrew:
		return e.brewLatest(ctx, pkg)
	default:
		return ""
	}
}

func (e *Env) brewLatest(ctx context.Context, formula string) string {
	if formula == "" {
		return ""
	}
	out, exitCode, _, _ := runner.RunStdout(ctx, []string{"brew", "info", "--json=v2", formula}, latestVersionCmdTimeout)
	if exitCode != 0 {
		return ""
	}
	return version.ParseBrewLatest(out)
}
