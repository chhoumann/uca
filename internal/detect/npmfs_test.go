package detect

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNpmrcPrefix(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), ".npmrc")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	tests := []struct {
		name      string
		content   string
		want      string
		wantFound bool
	}{
		{"absent", "registry=https://example.com\n", "", false},
		{"plain", "prefix=/opt/npm-global\n", "/opt/npm-global", true},
		{"spaced", "prefix = /opt/npm-global\n", "/opt/npm-global", true},
		{"comment ignored", "# prefix=/nope\nprefix=/real\n", "/real", true},
		{"interpolation asks npm", "prefix=${HOME}/npm\n", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := npmrcPrefix(write(t, tt.content))
			if got != tt.want || found != tt.wantFound {
				t.Fatalf("npmrcPrefix = %q,%v want %q,%v", got, found, tt.want, tt.wantFound)
			}
		})
	}

	if got, found := npmrcPrefix(filepath.Join(t.TempDir(), "missing")); got != "" || found {
		t.Fatalf("missing file = %q,%v want empty,false", got, found)
	}
}

func TestNpmrcPrefixExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), ".npmrc")
	if err := os.WriteFile(path, []byte("prefix=~/npm-global\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, found := npmrcPrefix(path)
	if !found || got != filepath.Join(home, "npm-global") {
		t.Fatalf("npmrcPrefix(~) = %q,%v", got, found)
	}
}

func TestNpmPrefixFastEnvWins(t *testing.T) {
	t.Setenv("npm_config_prefix", "/env/prefix")
	t.Setenv("NPM_CONFIG_PREFIX", "")
	if got := npmPrefixFast(); got != "/env/prefix" {
		t.Fatalf("npmPrefixFast = %q, want env value", got)
	}
}

func TestNpmPrefixFastNodeDerivationValidates(t *testing.T) {
	t.Setenv("npm_config_prefix", "")
	t.Setenv("NPM_CONFIG_PREFIX", "")
	t.Setenv("HOME", t.TempDir()) // no ~/.npmrc

	// A node whose prefix has no global node_modules (e.g. a shim dir) must not
	// be trusted.
	shim := t.TempDir()
	bin := filepath.Join(shim, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "node"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if got := npmPrefixFast(); got != "" {
		t.Fatalf("unvalidated node prefix = %q, want empty (ask npm)", got)
	}

	// With lib/node_modules present, the derivation is trusted.
	if err := os.MkdirAll(filepath.Join(shim, "lib", "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := npmPrefixFast(); got != shim {
		t.Fatalf("validated node prefix = %q, want %q", got, shim)
	}
}

func TestListGlobalNodePackages(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"opencode-ai", "@openai/codex", "@scope/other", ".bin", ".package-lock.json"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := listGlobalNodePackages(dir)
	want := map[string]bool{"opencode-ai": true, "@openai/codex": true, "@scope/other": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listGlobalNodePackages = %v, want %v", got, want)
	}
	if listGlobalNodePackages(filepath.Join(dir, "missing")) != nil {
		t.Fatal("missing dir must return nil (fall back to CLI)")
	}
}

func TestReadCodeExtensions(t *testing.T) {
	dir := t.TempDir()
	manifest := `[
	 {"identifier":{"id":"pub.old"},"version":"1.0.0","relativeLocation":"pub.old-1.0.0"},
	 {"identifier":{"id":"pub.old"},"version":"2.0.0","relativeLocation":"pub.old-2.0.0"},
	 {"identifier":{"id":"pub.gone"},"version":"3.0.0","relativeLocation":"pub.gone-3.0.0"},
	 {"identifier":{"id":"pub.keep"},"version":"0.5.0","relativeLocation":"pub.keep-0.5.0"}
	]`
	if err := os.WriteFile(filepath.Join(dir, "extensions.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".obsolete"), []byte(`{"pub.gone-3.0.0":true,"pub.old-1.0.0":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readCodeExtensions(dir)
	want := map[string]string{"pub.old": "2.0.0", "pub.keep": "0.5.0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readCodeExtensions = %v, want %v", got, want)
	}

	if readCodeExtensions("") != nil {
		t.Fatal("empty dir must return nil")
	}
	if readCodeExtensions(t.TempDir()) != nil {
		t.Fatal("missing manifest must return nil (fall back to CLI)")
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
