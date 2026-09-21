package detect

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNodePackageForBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures are POSIX-only")
	}
	for _, tt := range []struct{ name, manifest, want string }{
		{"scoped", `{"name":"@opencode/cli","bin":{"opencode":"bin/cli"}}`, "@opencode/cli"},
		{"string bin", `{"name":"@scope/opencode","bin":"bin/cli"}`, "@scope/opencode"},
		{"different target", `{"name":"other","bin":{"opencode":"bin/other"}}`, ""},
		{"different command", `{"name":"other","bin":{"other":"bin/cli"}}`, ""},
		{"invalid manifest", `{`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			pkg := filepath.Join(root, "node_modules", "@opencode", "cli")
			for _, dir := range []string{bin, filepath.Join(pkg, "bin")} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(pkg, "bin", "cli")
			if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(tt.manifest), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(bin, "opencode")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			env := New(context.Background())
			if got := env.NodePackageForBinary("opencode"); got != tt.want {
				t.Fatalf("owner = %q, want %q", got, tt.want)
			}
			if got := env.NodePackageForBinary("missing"); got != "" {
				t.Fatalf("missing owner = %q", got)
			}
		})
	}
}
