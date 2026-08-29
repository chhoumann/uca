package detect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/chhoumann/uca/internal/agents"
)

func fakeEnv(t *testing.T, scripts map[string]string) *Env {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script PATH fixtures are POSIX-only")
	}
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Setenv("PATH", dir)
	// Keep latest-version lookups on the fake manager CLIs instead of the live
	// registry HTTP fast path, and isolate the filesystem fast paths (npmrc
	// prefix, VS Code extensions manifest, brew Cellar/taps) from host state.
	t.Setenv("UCA_NO_REGISTRY_HTTP", "1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("npm_config_prefix", "")
	t.Setenv("NPM_CONFIG_PREFIX", "")
	t.Setenv("HOMEBREW_CELLAR", "")
	t.Setenv("HOMEBREW_REPOSITORY", "")
	return New(context.Background())
}

func TestNodeLatestVersionCaches(t *testing.T) {
	env := New(context.Background())
	env.latestCache[packageQueryKey("pkg", "")] = "9.9.9"
	if got := env.NodeLatestVersion(context.Background(), agents.KindNpm, "pkg", ""); got != "9.9.9" {
		t.Fatalf("NodeLatestVersion cached = %q, want 9.9.9", got)
	}
	if got := env.NodeLatestVersion(context.Background(), agents.KindBun, "pkg", ""); got != "9.9.9" {
		t.Fatalf("NodeLatestVersion cached (other kind) = %q, want 9.9.9", got)
	}
}

func TestNodeManagerLatestArgsUsesVersionSpec(t *testing.T) {
	betaWant := map[string][]string{
		agents.KindNpm:  {"npm", "view", "pkg@beta", "version"},
		agents.KindPnpm: {"pnpm", "view", "pkg@beta", "version", "--silent"},
		agents.KindYarn: {"yarn", "info", "pkg@beta", "version", "--silent"},
		agents.KindBun:  {"bun", "info", "-g", "pkg@beta", "version", "--json"},
	}
	latestWant := map[string][]string{
		agents.KindNpm:  {"npm", "view", "pkg@latest", "version"},
		agents.KindPnpm: {"pnpm", "view", "pkg@latest", "version", "--silent"},
		agents.KindYarn: {"yarn", "info", "pkg@latest", "version", "--silent"},
		agents.KindBun:  {"bun", "info", "-g", "pkg@latest", "version", "--json"},
	}
	for _, def := range nodeManagerDefs {
		if got := def.latestArgs("pkg", "beta"); !reflect.DeepEqual(got, betaWant[def.kind]) {
			t.Errorf("%s beta args = %#v, want %#v", def.kind, got, betaWant[def.kind])
		}
		if got := def.latestArgs("pkg", ""); !reflect.DeepEqual(got, latestWant[def.kind]) {
			t.Errorf("%s latest args = %#v, want %#v", def.kind, got, latestWant[def.kind])
		}
	}
}

func TestLatestVersionDispatch(t *testing.T) {
	t.Setenv("UCA_NO_REGISTRY_HTTP", "")
	env := New(context.Background())

	for _, m := range []string{agents.KindNative, agents.KindPip, agents.KindUv, "unknown"} {
		if got := env.LatestVersion(context.Background(), m, "pkg", "beta"); got != "" {
			t.Fatalf("LatestVersion(%q) = %q, want empty", m, got)
		}
	}

	env.latestCache[packageQueryKey("pkg", "beta")] = "9.9.9"
	if got := env.LatestVersion(context.Background(), agents.KindNpm, "pkg", "beta"); got != "9.9.9" {
		t.Fatalf("LatestVersion(npm) = %q, want 9.9.9", got)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"extensions":[{"versions":[{"version":"4.0.7","properties":[]}]}]}]}`))
	}))
	defer srv.Close()
	old := marketplaceURL
	marketplaceURL = srv.URL
	defer func() { marketplaceURL = old }()
	if got := env.LatestVersion(context.Background(), agents.KindVSCode, "pub.ext", "ignored"); got != "4.0.7" {
		t.Fatalf("LatestVersion(vscode) = %q, want 4.0.7", got)
	}
}

func TestPipHasNormalizesNames(t *testing.T) {
	// pip canonicalizes names (case-insensitive, "_"->"-"); detection must match.
	env := fakeEnv(t, map[string]string{
		"python3": "#!/bin/sh\nif [ \"$3\" = \"list\" ]; then echo \"Aider_Chat==1.0.0\"; exit 0; fi\nexit 1\n",
	})
	if !env.PipHas("aider-chat") {
		t.Fatal("PipHas(aider-chat) = false, want true (name normalization)")
	}
}

func TestNodeManagerForBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping PATH-based binary detection test on windows")
	}
	dir := t.TempDir()
	binName := "fakecli"
	if err := os.WriteFile(filepath.Join(dir, binName), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	t.Setenv("PATH", dir)

	env := New(context.Background())
	env.node[agents.KindNpm].installed = true
	env.node[agents.KindNpm].binDir.set(dir)

	if got := env.NodeManagerForBinary(binName); got != agents.KindNpm {
		t.Fatalf("NodeManagerForBinary() = %q, want %q", got, agents.KindNpm)
	}
}

func TestNodeManagerForBinarySymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping symlink detection test on windows")
	}
	binDir := t.TempDir()
	targetDir := t.TempDir()
	binName := "fakecli"
	targetPath := filepath.Join(targetDir, binName)
	if err := os.WriteFile(targetPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write target binary: %v", err)
	}
	if err := os.Symlink(targetPath, filepath.Join(binDir, binName)); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	t.Setenv("PATH", binDir)

	env := New(context.Background())
	env.node[agents.KindNpm].installed = true
	env.node[agents.KindNpm].binDir.set(targetDir)

	if got := env.NodeManagerForBinary(binName); got != agents.KindNpm {
		t.Fatalf("NodeManagerForBinary() = %q, want %q", got, agents.KindNpm)
	}
}

func TestParsePackageFromToken(t *testing.T) {
	tests := []struct {
		token string
		want  string
	}{
		{token: "\"@google/gemini-cli@1.2.3\"", want: "@google/gemini-cli"},
		{token: "opencode-ai@0.1.0", want: "opencode-ai"},
		{token: "nope", want: ""},
		{token: "@scope/nover@", want: ""},
	}
	for _, tt := range tests {
		if got := parsePackageFromToken(tt.token); got != tt.want {
			t.Fatalf("parsePackageFromToken(%q) = %q, want %q", tt.token, got, tt.want)
		}
	}
}
