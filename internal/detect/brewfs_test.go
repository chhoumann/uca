package detect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chhoumann/uca/internal/agents"
)

// TestBrewLatestCLIFallback exercises the `brew info` CLI fallback via a fake
// brew script (no local tap carries the formula, so the fast path misses).
func TestBrewLatestCLIFallback(t *testing.T) {
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

func TestTapFormulaVersion(t *testing.T) {
	taps := t.TempDir()
	formulaDir := filepath.Join(taps, "user", "homebrew-tap", "Formula")
	if err := os.MkdirAll(formulaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "class Omp < Formula\n  desc \"x\"\n  version \"16.4.0\"\n  url \"https://example.com/v#{version}/omp\"\nend\n"
	if err := os.WriteFile(filepath.Join(formulaDir, "omp.rb"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := tapFormulaVersion([]string{taps}, "omp"); got != "16.4.0" {
		t.Fatalf("tapFormulaVersion = %q, want 16.4.0", got)
	}
	if got := tapFormulaVersion([]string{taps}, "absent"); got != "" {
		t.Fatalf("absent formula = %q, want empty", got)
	}

	// The same formula in a second tap is ambiguous: fall back to brew info.
	rootDir := filepath.Join(taps, "other", "homebrew-tools")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "omp.rb"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := tapFormulaVersion([]string{taps}, "omp"); got != "" {
		t.Fatalf("ambiguous formula = %q, want empty", got)
	}
}

func TestFormulaVersionLiteral(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"literal", "  version \"1.2.3\"\n", "1.2.3"},
		{"no literal (derived from url)", "  url \"https://x/v1.2.3.tar.gz\"\n", ""},
		{"interpolated version skipped", "  url \"https://x/v#{version}/y\"\n  version \"2.0.0\"\n", "2.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formulaVersionLiteral(tt.src); got != tt.want {
				t.Fatalf("formulaVersionLiteral = %q, want %q", got, tt.want)
			}
		})
	}
}
