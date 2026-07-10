package detect

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chhoumann/uca/internal/version"
)

// Latest-version lookups hit the package registry directly over HTTP instead of
// spawning `npm view` / `bun info` (a Node/manager startup plus its own HTTP
// round-trip, ~600-900ms each). The GET {registry}/{pkg}/latest endpoint returns
// the dist-tags.latest manifest, which is exactly what those CLI queries read.
// Any failure (custom registry needing auth, offline, non-JSON response) falls
// back to the manager CLI, so this is purely a fast path.

const defaultNpmRegistry = "https://registry.npmjs.org"

var registryHTTPClient = &http.Client{Timeout: 5 * time.Second}

var npmRegistryConfig struct {
	once   sync.Once
	def    string            // default registry (empty = npmjs)
	scoped map[string]string // "@scope" -> registry URL
}

// loadNpmRegistryConfig resolves the registry URL(s) the way npm does for the
// subset that matters here: the npm_config_registry environment variable and the
// user-level ~/.npmrc (registry= and @scope:registry= keys). Anything more
// exotic (per-project .npmrc, auth) is served by the CLI fallback instead.
func loadNpmRegistryConfig() {
	cfg := &npmRegistryConfig
	cfg.scoped = map[string]string{}
	if home, err := os.UserHomeDir(); err == nil {
		parseNpmrcRegistries(filepath.Join(home, ".npmrc"), cfg.scoped, &cfg.def)
	}
	if v := strings.TrimSpace(os.Getenv("npm_config_registry")); v != "" {
		cfg.def = v
	}
}

func parseNpmrcRegistries(path string, scoped map[string]string, def *string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if key == "registry" {
			*def = value
			continue
		}
		if scope, rest, ok := strings.Cut(key, ":"); ok && rest == "registry" && strings.HasPrefix(scope, "@") {
			scoped[scope] = value
		}
	}
}

func registryForPackage(pkg string) (string, bool) {
	// Kill switch (also keeps tests hermetic): fall back to the manager CLI.
	// Checked per call, not once, so tests can toggle it.
	if os.Getenv("UCA_NO_REGISTRY_HTTP") != "" {
		return "", false
	}
	npmRegistryConfig.once.Do(loadNpmRegistryConfig)
	cfg := &npmRegistryConfig
	if strings.HasPrefix(pkg, "@") {
		if scope, _, ok := strings.Cut(pkg, "/"); ok {
			if reg, found := cfg.scoped[scope]; found {
				return reg, true
			}
		}
	}
	if cfg.def != "" {
		return cfg.def, true
	}
	return defaultNpmRegistry, true
}

// registryLatestVersion returns dist-tags.latest for pkg straight from the
// registry, or "" when the fast path does not apply (caller falls back to the
// manager CLI).
func registryLatestVersion(ctx context.Context, pkg string) string {
	registry, ok := registryForPackage(pkg)
	if !ok {
		return ""
	}
	return fetchRegistryLatest(ctx, registry, pkg)
}

// fetchRegistryLatest GETs {registry}/{pkg}/latest and extracts the manifest
// version. Empty on any failure.
func fetchRegistryLatest(ctx context.Context, registry, pkg string) string {
	endpoint := strings.TrimRight(registry, "/") + "/" + url.PathEscape(pkg) + "/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/json")
	resp, err := registryHTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return ""
	}
	v := strings.TrimSpace(manifest.Version)
	if v == "" {
		return ""
	}
	// Validate the shape so an unexpected payload never surfaces as a "version".
	if _, ok := version.ExtractToken(v); !ok {
		return ""
	}
	return v
}
