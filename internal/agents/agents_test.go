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

func TestDefaultIncludesPiFromEarendilWorks(t *testing.T) {
	defaults := Default()
	var got *Agent
	for i := range defaults {
		if defaults[i].Name == "pi" {
			got = &defaults[i]
			break
		}
	}
	if got == nil {
		t.Fatal("Default() is missing pi")
	}
	if got.Binary != "pi" {
		t.Fatalf("pi Binary = %q, want %q", got.Binary, "pi")
	}
	if len(got.VersionCmd) != 2 || got.VersionCmd[0] != "pi" || got.VersionCmd[1] != "--version" {
		t.Fatalf("pi VersionCmd = %#v, want %#v", got.VersionCmd, []string{"pi", "--version"})
	}

	wantKinds := map[string]bool{
		KindNpm:  false,
		KindPnpm: false,
		KindYarn: false,
		KindBun:  false,
	}
	for _, strat := range got.Strategies {
		if _, ok := wantKinds[strat.Kind]; !ok {
			continue
		}
		wantKinds[strat.Kind] = true
		if strat.Package != "@earendil-works/pi-coding-agent" {
			t.Fatalf("%s pi package = %q, want %q", strat.Kind, strat.Package, "@earendil-works/pi-coding-agent")
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Fatalf("pi is missing %s strategy", kind)
		}
	}
}

func TestDefaultIncludesOmpFromOhMyPi(t *testing.T) {
	defaults := Default()
	var got *Agent
	for i := range defaults {
		if defaults[i].Name == "omp" {
			got = &defaults[i]
			break
		}
	}
	if got == nil {
		t.Fatal("Default() is missing omp")
	}
	if got.Binary != "omp" {
		t.Fatalf("omp Binary = %q, want %q", got.Binary, "omp")
	}
	if len(got.VersionCmd) != 2 || got.VersionCmd[0] != "omp" || got.VersionCmd[1] != "--version" {
		t.Fatalf("omp VersionCmd = %#v, want %#v", got.VersionCmd, []string{"omp", "--version"})
	}

	wantStrategies := []UpdateStrategy{
		{Kind: KindBrew, Package: "omp"},
		{Kind: KindBun, Package: "@oh-my-pi/pi-coding-agent"},
		{Kind: KindNative, Command: []string{"omp", "update"}},
	}
	if len(got.Strategies) != len(wantStrategies) {
		t.Fatalf("omp has %d strategies, want %d: %#v", len(got.Strategies), len(wantStrategies), got.Strategies)
	}
	for i, want := range wantStrategies {
		gotStrategy := got.Strategies[i]
		if gotStrategy.Kind != want.Kind || gotStrategy.Package != want.Package {
			t.Fatalf("omp strategy %d = %#v, want %#v", i, gotStrategy, want)
		}
		if len(gotStrategy.Command) != len(want.Command) {
			t.Fatalf("omp strategy %d command = %#v, want %#v", i, gotStrategy.Command, want.Command)
		}
		for j := range want.Command {
			if gotStrategy.Command[j] != want.Command[j] {
				t.Fatalf("omp strategy %d command = %#v, want %#v", i, gotStrategy.Command, want.Command)
			}
		}
	}

	for _, strat := range got.Strategies {
		switch strat.Kind {
		case KindNpm, KindPnpm, KindYarn:
			t.Fatalf("omp must not include %s strategy", strat.Kind)
		}
	}
}

func TestDefaultIncludesCursorAgentPrimaryAndLegacyFallback(t *testing.T) {
	defaults := Default()
	var got *Agent
	cursorCount := 0
	for i := range defaults {
		if defaults[i].Name == "cursor" {
			got = &defaults[i]
			cursorCount++
		}
		if defaults[i].Name == "agent" {
			t.Fatal("Default() should not expose agent as a separate supported agent")
		}
	}
	if cursorCount != 1 {
		t.Fatalf("Default() has %d cursor agents, want 1", cursorCount)
	}
	if got == nil {
		t.Fatal("Default() is missing cursor")
	}
	if got.Binary != "cursor-agent" {
		t.Fatalf("cursor Binary = %q, want %q", got.Binary, "cursor-agent")
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "agent" {
		t.Fatalf("cursor Aliases = %#v, want %#v", got.Aliases, []string{"agent"})
	}
	if len(got.Strategies) < 2 {
		t.Fatalf("cursor has %d strategies, want at least 2", len(got.Strategies))
	}

	primary := got.Strategies[0]
	if primary.Kind != KindNative || primary.Binary != "agent" {
		t.Fatalf("primary cursor strategy = %#v, want native agent", primary)
	}
	if len(primary.Command) != 2 || primary.Command[0] != "agent" || primary.Command[1] != "update" {
		t.Fatalf("primary cursor command = %#v, want %#v", primary.Command, []string{"agent", "update"})
	}
	if len(primary.VersionCmd) != 2 || primary.VersionCmd[0] != "agent" || primary.VersionCmd[1] != "--version" {
		t.Fatalf("primary cursor VersionCmd = %#v, want %#v", primary.VersionCmd, []string{"agent", "--version"})
	}
	if primary.HelpContains != "Cursor Agent" {
		t.Fatalf("primary cursor HelpContains = %q, want %q", primary.HelpContains, "Cursor Agent")
	}

	fallback := got.Strategies[1]
	if fallback.Kind != KindNative {
		t.Fatalf("fallback cursor strategy = %#v, want native", fallback)
	}
	if len(fallback.Command) != 2 || fallback.Command[0] != "cursor-agent" || fallback.Command[1] != "update" {
		t.Fatalf("fallback cursor command = %#v, want %#v", fallback.Command, []string{"cursor-agent", "update"})
	}
	if fallback.Binary != "" {
		t.Fatalf("fallback cursor Binary = %q, want agent-level default", fallback.Binary)
	}
	if len(fallback.VersionCmd) != 0 {
		t.Fatalf("fallback cursor VersionCmd = %#v, want agent-level default", fallback.VersionCmd)
	}
}
