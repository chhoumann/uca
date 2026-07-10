package detect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

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
			got := fetchRegistryLatest(context.Background(), srv.URL, tt.pkg)
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
	if _, ok := registryForPackage("anything"); ok {
		t.Fatal("kill switch must disable the HTTP fast path")
	}
}

func TestCellarFormulae(t *testing.T) {
	cellar := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cellar, "omp", "16.4.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cellar, "empty-formula"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cellar, "stray-file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	formulae := cellarFormulae(cellar)
	if formulae == nil {
		t.Fatal("cellarFormulae = nil, want map")
	}
	if !formulae["omp"] {
		t.Fatal("omp (with a version dir) must be detected")
	}
	if formulae["empty-formula"] {
		t.Fatal("a formula dir without version dirs must not be detected")
	}
	if formulae["stray-file"] {
		t.Fatal("a plain file must not be detected")
	}

	if got := cellarFormulae(""); got != nil {
		t.Fatalf("cellarFormulae(\"\") = %v, want nil", got)
	}
	if got := cellarFormulae(filepath.Join(cellar, "does-not-exist")); got != nil {
		t.Fatalf("cellarFormulae(missing) = %v, want nil (fall back to brew list)", got)
	}
}
