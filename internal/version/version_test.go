package version

import "testing"

func TestParseOutput(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{name: "empty", out: "", want: "unknown"},
		{name: "version_only", out: "1.2.3\n", want: "1.2.3"},
		{name: "version_only_with_v", out: "v2.0.1\n", want: "v2.0.1"},
		{name: "first_line_default", out: "claude 2.1.19\n", want: "claude 2.1.19"},
		{name: "selects_last_version_only_line", out: "INFO something\n1.1.36\n", want: "1.1.36"},
		{name: "skips_blank_lines", out: "\n\n1.4.0\n\n", want: "1.4.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseOutput(tt.out); got != tt.want {
				t.Fatalf("ParseOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractToken(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "", want: "", ok: false},
		{in: "codex-cli 0.90.0-alpha.5", want: "0.90.0-alpha.5", ok: true},
		{in: "v2.0.1", want: "v2.0.1", ok: true},
		{in: "no version here", want: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := ExtractToken(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("ExtractToken(%q) = %q,%v want %q,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestSame(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{a: "grok 0.2.91 (39d0c6872354)", b: "grok 0.2.91 (39d0c6872354) [stable]", want: true},
		{a: "codex-cli 0.90.0", b: "codex-cli 0.90.0", want: true},
		{a: "codex-cli 0.90.0", b: "codex-cli 0.98.0", want: false},
		{a: "2026.07.01-41b2de7", b: "2026.07.02-9c1f0aa", want: false},
		{a: "unknown", b: "unknown", want: true},
		{a: "unknown", b: "1.2.3", want: false},
		{a: "", b: "1.2.3", want: false},
	}
	for _, tt := range tests {
		if got := Same(tt.a, tt.b); got != tt.want {
			t.Fatalf("Same(%q,%q)=%v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestFormatWithToken(t *testing.T) {
	tests := []struct {
		before string
		newVer string
		want   string
	}{
		{before: "codex-cli 0.90.0-alpha.5", newVer: "0.98.0", want: "codex-cli 0.98.0"},
		{before: "v2.0.1", newVer: "2.0.2", want: "v2.0.2"},
		{before: "unknown", newVer: "1.2.3", want: "1.2.3"},
		{before: "", newVer: "1.2.3", want: "1.2.3"},
	}
	for _, tt := range tests {
		if got := FormatWithToken(tt.before, tt.newVer); got != tt.want {
			t.Fatalf("FormatWithToken(%q,%q)=%q, want %q", tt.before, tt.newVer, got, tt.want)
		}
	}
}

func TestParseLatest(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{name: "bare", out: "0.141.0\n", want: "0.141.0"},
		{name: "quoted", out: "\"0.79.9\"\n", want: "0.79.9"},
		{name: "v_prefix", out: "v2.0.1\n", want: "v2.0.1"},
		{name: "banner_then_version", out: "npm notice using safe-chain\n0.141.0\n", want: "0.141.0"},
		{name: "version_then_trailing_banner", out: "0.141.0\nnpm notice update available\n", want: "0.141.0"},
		{name: "version_then_versioned_banner", out: "0.141.0\nnpm notice New major version 10.0.0 -> 11.5.2\n", want: "0.141.0"},
		{name: "bun_json_object", out: "{\"version\":\"0.79.9\"}\n", want: "0.79.9"},
		{name: "no_version", out: "no version here\n", want: ""},
		{name: "empty", out: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseLatest(tt.out); got != tt.want {
				t.Fatalf("ParseLatest(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

func TestParseBunJSON(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{name: "scalar", out: "\"6.0.3\"\n", want: "6.0.3"},
		{name: "object_version", out: "{\"version\":\"0.79.9\"}\n", want: "0.79.9"},
		{name: "manifest", out: "{\"name\":\"pkg\",\"version\":\"0.79.9\",\"dependencies\":{\"dep\":\"0.3.3\"}}\n", want: "0.79.9"},
		{name: "not_json", out: "0.79.9\n", want: ""},
		{name: "empty", out: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseBunJSON(tt.out); got != tt.want {
				t.Fatalf("ParseBunJSON(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

func TestParseBrewLatest(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{name: "v2", out: `{"formulae":[{"versions":{"stable":"1.2.3"}}]}`, want: "1.2.3"},
		{name: "empty_formulae", out: `{"formulae":[]}`, want: ""},
		{name: "bad_json", out: "not json", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseBrewLatest(tt.out); got != tt.want {
				t.Fatalf("ParseBrewLatest(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int // sign
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2", "1.2.0", 0},
		{"1.2.3+build", "1.2.3", 0},
		{"1.3.0", "1.2.9", 1},
		{"0.142.0-rc.1", "0.141.0", 1},
		{"1.2.0-rc.1", "1.2.0", -1},
		{"1.4.9", "1.5.0", -1},
	}
	sign := func(n int) int {
		switch {
		case n < 0:
			return -1
		case n > 0:
			return 1
		default:
			return 0
		}
	}
	for _, tt := range tests {
		if got := sign(Compare(tt.a, tt.b)); got != tt.want {
			t.Fatalf("Compare(%q,%q) sign = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
