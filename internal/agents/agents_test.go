package agents

import "testing"

func TestDefaultIncludesDroid(t *testing.T) {
	defaults := Default()
	var got *Agent
	for i := range defaults {
		if defaults[i].Name == "droid" {
			got = &defaults[i]
			break
		}
	}
	if got == nil {
		t.Fatal("Default() is missing droid")
	}
	if got.Binary != "droid" {
		t.Fatalf("droid Binary = %q, want %q", got.Binary, "droid")
	}
	if len(got.VersionCmd) != 2 || got.VersionCmd[0] != "droid" || got.VersionCmd[1] != "--version" {
		t.Fatalf("droid VersionCmd = %#v, want %#v", got.VersionCmd, []string{"droid", "--version"})
	}

	wantKinds := map[string]bool{
		KindNpm:    false,
		KindPnpm:   false,
		KindYarn:   false,
		KindBun:    false,
		KindNative: false,
	}
	for _, strat := range got.Strategies {
		if _, ok := wantKinds[strat.Kind]; !ok {
			continue
		}
		wantKinds[strat.Kind] = true
		if strat.Kind == KindNative {
			if len(strat.Command) != 2 || strat.Command[0] != "droid" || strat.Command[1] != "update" {
				t.Fatalf("native droid update command = %#v, want %#v", strat.Command, []string{"droid", "update"})
			}
			continue
		}
		if strat.Package != "droid" {
			t.Fatalf("%s droid package = %q, want %q", strat.Kind, strat.Package, "droid")
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Fatalf("droid is missing %s strategy", kind)
		}
	}
}
