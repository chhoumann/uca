package agentspec

import (
	"reflect"
	"testing"

	"github.com/chhoumann/uca/internal/agents"
)

func TestNodeUpdateCommandPinnedVersion(t *testing.T) {
	got := NodeUpdateCommand(agents.UpdateStrategy{Kind: agents.KindNpm, Package: "pkg", Version: "1.2.3"})
	want := []string{"npm", "install", "-g", "pkg@1.2.3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pinned NodeUpdateCommand = %#v, want %#v", got, want)
	}
	got = NodeUpdateCommand(agents.UpdateStrategy{Kind: agents.KindBun, Package: "pkg"})
	if !reflect.DeepEqual(got, []string{"bun", "add", "-g", "pkg@latest"}) {
		t.Fatalf("unpinned NodeUpdateCommand = %#v", got)
	}
}

func TestNodeUpdateCommandUsesLatestTag(t *testing.T) {
	tests := []struct {
		name  string
		strat agents.UpdateStrategy
		want  []string
	}{
		{name: "npm", strat: agents.UpdateStrategy{Kind: agents.KindNpm, Package: "pkg"}, want: []string{"npm", "install", "-g", "pkg@latest"}},
		{name: "pnpm", strat: agents.UpdateStrategy{Kind: agents.KindPnpm, Package: "pkg"}, want: []string{"pnpm", "add", "-g", "pkg@latest"}},
		{name: "yarn", strat: agents.UpdateStrategy{Kind: agents.KindYarn, Package: "pkg"}, want: []string{"yarn", "global", "add", "pkg@latest"}},
		{name: "bun", strat: agents.UpdateStrategy{Kind: agents.KindBun, Package: "pkg"}, want: []string{"bun", "add", "-g", "pkg@latest"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NodeUpdateCommand(tt.strat); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("NodeUpdateCommand() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNodeBatchUpdateCommand(t *testing.T) {
	tests := []struct {
		name string
		kind string
		pkgs []string
		want []string
	}{
		{name: "npm", kind: agents.KindNpm, pkgs: []string{"a", "b"}, want: []string{"npm", "install", "-g", "a@latest", "b@latest"}},
		{name: "pnpm", kind: agents.KindPnpm, pkgs: []string{"a", "b"}, want: []string{"pnpm", "add", "-g", "a@latest", "b@latest"}},
		{name: "yarn", kind: agents.KindYarn, pkgs: []string{"a", "b"}, want: []string{"yarn", "global", "add", "a@latest", "b@latest"}},
		{name: "bun", kind: agents.KindBun, pkgs: []string{"a", "b"}, want: []string{"bun", "add", "-g", "a@latest", "b@latest"}},
		{name: "npm_skips_empty", kind: agents.KindNpm, pkgs: []string{"a", "", "  ", "b"}, want: []string{"npm", "install", "-g", "a@latest", "b@latest"}},
		{name: "unknown", kind: "nope", pkgs: []string{"a", "b"}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NodeBatchUpdateCommand(tt.kind, tt.pkgs); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("NodeBatchUpdateCommand() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestIsNodeKindAndShouldLock(t *testing.T) {
	if !IsNodeKind(agents.KindNpm) || IsNodeKind(agents.KindNative) {
		t.Fatal("IsNodeKind wrong")
	}
	if !ShouldLockKind(agents.KindBrew) || ShouldLockKind(agents.KindNative) {
		t.Fatal("ShouldLockKind wrong")
	}
}

func TestNodePackageName(t *testing.T) {
	a := agents.Agent{Strategies: []agents.UpdateStrategy{{Kind: agents.KindNative}, {Kind: agents.KindBun, Package: "pkg"}}}
	if got := NodePackageName(a.Strategies); got != "pkg" {
		t.Fatalf("NodePackageName = %q, want pkg", got)
	}
	if got := NodePackageName([]agents.UpdateStrategy{{Kind: agents.KindNative}}); got != "" {
		t.Fatalf("NodePackageName(no node) = %q, want empty", got)
	}
}
