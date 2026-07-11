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

// npmRegistryConfig resolves the registry URL(s) the way npm does for the
// subset that matters here: the npm_config_registry environment variable and
// the user-level ~/.npmrc (registry= and @scope:registry= keys). Anything more
// exotic (per-project .npmrc, auth) is served by the CLI fallback instead.
// One instance lives on Env so tests get a fresh, presettable config per Env.
type npmRegistryConfig struct {
	once   sync.Once
	def    string            // default registry (empty = npmjs)
	scoped map[string]string // "@scope" -> registry URL
}

func (c *npmRegistryConfig) load() {
	c.scoped = map[string]string{}
	if home, err := os.UserHomeDir(); err == nil {
		parseNpmrcRegistries(filepath.Join(home, ".npmrc"), c.scoped, &c.def)
	}
	if v := strings.TrimSpace(os.Getenv("npm_config_registry")); v != "" {
		c.def = v
	}
}

// parseNpmrcRegistries collects registry= (into def, last one wins, as npm
// does) and @scope:registry= keys from an npmrc file. Registry URLs are used
// verbatim, without expansion.
func parseNpmrcRegistries(path string, scoped map[string]string, def *string) {
	eachNpmrcEntry(path, func(key, value string) {
		if value == "" {
			return
		}
		if key == "registry" {
			*def = value
			return
		}
		if scope, rest, ok := strings.Cut(key, ":"); ok && rest == "registry" && strings.HasPrefix(scope, "@") {
			scoped[scope] = value
		}
	})
}

// registryForPackage returns the registry base URL to query for pkg. The bool
// means "the HTTP fast path is enabled": it is false only under the
// UCA_NO_REGISTRY_HTTP kill switch (checked per call, not once, so tests can
// toggle it); otherwise a registry is always returned, defaulting to npmjs.
func (e *Env) registryForPackage(pkg string) (string, bool) {
	if os.Getenv("UCA_NO_REGISTRY_HTTP") != "" {
		return "", false
	}
	cfg := &e.npmRegistry
	cfg.once.Do(cfg.load)
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
func (e *Env) registryLatestVersion(ctx context.Context, pkg string) string {
	registry, ok := e.registryForPackage(pkg)
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
