package detect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// registryStubEnv returns an Env whose npm registry config points at a stub
// server answering every package lookup with the given manifest version.
func registryStubEnv(t *testing.T, version string) *Env {
	t.Helper()
	t.Setenv("UCA_NO_REGISTRY_HTTP", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"version":"` + version + `"}`))
	}))
	t.Cleanup(srv.Close)
	env := New(context.Background())
	env.npmRegistry.once.Do(func() {
		env.npmRegistry.def = srv.URL
		env.npmRegistry.scoped = map[string]string{}
	})
	return env
}

func TestLatestCacheSeparatesPackageSpecs(t *testing.T) {
	t.Setenv("UCA_NO_REGISTRY_HTTP", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pkg/latest":
			w.Write([]byte(`{"version":"1.2.3"}`))
		case "/pkg/beta":
			w.Write([]byte(`{"version":"2.0.0-beta.1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	env := New(context.Background())
	env.npmRegistry.once.Do(func() {
		env.npmRegistry.def = srv.URL
		env.npmRegistry.scoped = map[string]string{}
	})
	env.PrefetchLatest(context.Background(), []PackageQuery{
		{Package: "pkg"},
		{Package: "pkg", Spec: "beta"},
	})
	deadline := time.Now().Add(2 * time.Second)
	for env.PeekLatest("pkg", "") != "1.2.3" || env.PeekLatest("pkg", "beta") != "2.0.0-beta.1" {
		if time.Now().After(deadline) {
			t.Fatalf(
				"PeekLatest latest/beta = %q/%q, want 1.2.3/2.0.0-beta.1",
				env.PeekLatest("pkg", ""),
				env.PeekLatest("pkg", "beta"),
			)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := env.PeekLatest("pkg", "latest"); got != "1.2.3" {
		t.Fatalf("explicit latest = %q, want 1.2.3", got)
	}
	if got := env.NodeLatestVersion(context.Background(), "missing-manager", "pkg", "beta"); got != "2.0.0-beta.1" {
		t.Fatalf("NodeLatestVersion beta = %q, want cached 2.0.0-beta.1", got)
	}
}

func TestFailedRegistryLookupRetries(t *testing.T) {
	env := registryStubEnv(t, "2.0.0")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := env.registryLatestOnce(canceled, "pkg", ""); got != "" {
		t.Fatalf("canceled lookup = %q, want empty", got)
	}
	if got := env.registryLatestOnce(context.Background(), "pkg", ""); got != "2.0.0" {
		t.Fatalf("lookup after failed flight = %q, want 2.0.0 (a failed flight must not be cached)", got)
	}
}

func TestParseNpmrcRegistries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".npmrc")
	content := `# comment
; also a comment
registry=https://mirror.example.com/npm/
@corp:registry=https://npm.corp.example.com
@empty:registry=
not-a-registry=value
min-release-age=7
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write npmrc: %v", err)
	}
	scoped := map[string]string{}
	def := ""
	parseNpmrcRegistries(path, scoped, &def)
	if def != "https://mirror.example.com/npm/" {
		t.Fatalf("default registry = %q", def)
	}
	if scoped["@corp"] != "https://npm.corp.example.com" {
		t.Fatalf("scoped registry = %q", scoped["@corp"])
	}
	if _, ok := scoped["@empty"]; ok {
		t.Fatal("empty scoped registry value must be ignored")
	}
}

func TestParseNpmrcRegistriesMissingFile(t *testing.T) {
	scoped := map[string]string{}
	def := ""
	parseNpmrcRegistries(filepath.Join(t.TempDir(), "nope"), scoped, &def)
	if def != "" || len(scoped) != 0 {
		t.Fatalf("missing file must leave config untouched, got def=%q scoped=%v", def, scoped)
	}
}

func TestFetchRegistryLatest(t *testing.T) {
	tests := []struct {
		name    string
		pkg     string
		spec    string
		status  int
		body    string
		want    string
		wantURL string
	}{
		{
			name: "plain package", pkg: "opencode-ai",
			status: http.StatusOK, body: `{"name":"opencode-ai","version":"1.2.3"}`,
			want: "1.2.3", wantURL: "/opencode-ai/latest",
		},
		{
			name: "scoped package is path-escaped", pkg: "@openai/codex",
			status: http.StatusOK, body: `{"version":"0.44.1"}`,
			want: "0.44.1", wantURL: "/@openai%2Fcodex/latest",
		},
		{
			name: "tag", pkg: "pkg", spec: "beta",
			status: http.StatusOK, body: `{"version":"2.0.0-beta.1"}`,
			want: "2.0.0-beta.1", wantURL: "/pkg/beta",
		},
		{
			name: "not found", pkg: "missing",
			status: http.StatusNotFound, body: `{"error":"Not found"}`,
			want: "",
		},
		{
			name: "non-JSON body", pkg: "weird",
			status: http.StatusOK, body: `<html>proxy portal</html>`,
			want: "",
		},
		{
			name: "non-version payload fails closed", pkg: "banner",
			status: http.StatusOK, body: `{"version":"see https://example.com"}`,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotURL string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotURL = r.URL.String()
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			got := fetchRegistryLatest(context.Background(), srv.URL, tt.pkg, tt.spec)
			if got != tt.want {
				t.Fatalf("fetchRegistryLatest = %q, want %q", got, tt.want)
			}
			if tt.wantURL != "" && gotURL != tt.wantURL {
				t.Fatalf("request URL = %q, want %q", gotURL, tt.wantURL)
			}
		})
	}
}

func TestRegistryForPackageKillSwitch(t *testing.T) {
	t.Setenv("UCA_NO_REGISTRY_HTTP", "1")
	if _, ok := New(context.Background()).registryForPackage("anything"); ok {
		t.Fatal("kill switch must disable the HTTP fast path")
	}
}

func TestQueryMarketplaceLatest(t *testing.T) {
	body := `{"results":[{"extensions":[{"versions":[
	 {"version":"5.0.0","properties":[{"key":"Microsoft.VisualStudio.Code.PreRelease","value":"true"}]},
	 {"version":"4.0.7","properties":[]},
	 {"version":"4.0.6","properties":[]}
	]}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()
	old := marketplaceURL
	marketplaceURL = srv.URL
	defer func() { marketplaceURL = old }()

	if got := queryMarketplaceLatest(context.Background(), "pub.ext"); got != "4.0.7" {
		t.Fatalf("queryMarketplaceLatest = %q, want 4.0.7 (first stable, skipping pre-release)", got)
	}
}

func TestQueryMarketplaceLatestSkipsMalformedVersion(t *testing.T) {
	body := `{"results":[{"extensions":[{"versions":[
	 {"version":"see release notes","properties":[]},
	 {"version":"3.1.0","properties":[]}
	]}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()
	old := marketplaceURL
	marketplaceURL = srv.URL
	defer func() { marketplaceURL = old }()

	if got := queryMarketplaceLatest(context.Background(), "pub.ext"); got != "3.1.0" {
		t.Fatalf("queryMarketplaceLatest = %q, want 3.1.0 (a malformed entry must not abort the lookup)", got)
	}
}

func TestQueryMarketplaceLatestEmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"extensions":[]}]}`))
	}))
	defer srv.Close()
	old := marketplaceURL
	marketplaceURL = srv.URL
	defer func() { marketplaceURL = old }()

	if got := queryMarketplaceLatest(context.Background(), "pub.missing"); got != "" {
		t.Fatalf("queryMarketplaceLatest(missing) = %q, want empty", got)
	}
}

func TestVSCodeMarketplaceLatestKillSwitch(t *testing.T) {
	t.Setenv("UCA_NO_REGISTRY_HTTP", "1")
	env := New(context.Background())
	if got := env.VSCodeMarketplaceLatest(context.Background(), "pub.ext"); got != "" {
		t.Fatalf("kill switch must disable marketplace lookups, got %q", got)
	}
}
