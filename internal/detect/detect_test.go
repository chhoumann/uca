package detect

import (
	"context"
	"os"
	"path/filepath"
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
	return New(context.Background())
}

func TestNodeLatestVersionCaches(t *testing.T) {
	env := New(context.Background())
	env.latestCache = map[string]string{agents.KindNpm + "\x00" + "pkg": "9.9.9"}
	// A cached value is returned without running any command.
	if got := env.NodeLatestVersion(context.Background(), agents.KindNpm, "pkg"); got != "9.9.9" {
		t.Fatalf("NodeLatestVersion cached = %q, want 9.9.9", got)
	}
}

func TestLatestVersionDispatch(t *testing.T) {
	env := New(context.Background())
	for _, m := range []string{agents.KindNative, agents.KindPip, agents.KindUv, agents.KindVSCode} {
		if got := env.LatestVersion(context.Background(), m, "pkg"); got != "" {
			t.Fatalf("LatestVersion(%q) = %q, want empty", m, got)
		}
	}
}

func TestBrewLatestLivePath(t *testing.T) {
	env := fakeEnv(t, map[string]string{
		"brew": "#!/bin/sh\necho '{\"formulae\":[{\"versions\":{\"stable\":\"1.2.3\"}}]}'\n",
	})
	if got := env.brewLatest(context.Background(), "copilot-cli"); got != "1.2.3" {
		t.Fatalf("brewLatest = %q, want 1.2.3", got)
	}
	if got := env.LatestVersion(context.Background(), agents.KindBrew, "copilot-cli"); got != "1.2.3" {
		t.Fatalf("LatestVersion(brew) = %q, want 1.2.3", got)
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
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

	env := &Env{hasNpm: true, binPathCache: map[string]string{}, npmBin: dir}
	env.npmBinOnce.Do(func() {})

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
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)

	env := &Env{hasNpm: true, binPathCache: map[string]string{}, npmBin: targetDir}
	env.npmBinOnce.Do(func() {})

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
