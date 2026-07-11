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
