package main

import (
	"testing"
	"time"
)

func TestApplyEnvDefaults(t *testing.T) {
	t.Setenv("UCA_TIMEOUT", "42s")
	t.Setenv("UCA_CONCURRENCY", "3")
	t.Setenv("UCA_SKIP", "claude,codex")
	t.Setenv("UCA_SERIAL", "1")
	t.Setenv("UCA_SAFE", "yes")

	opts, err := parseFlags([]string{})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.Timeout != 42*time.Second {
		t.Fatalf("Timeout = %v, want 42s from env", opts.Timeout)
	}
	if opts.Concurrency != 3 {
		t.Fatalf("Concurrency = %d, want 3 from env", opts.Concurrency)
	}
	if opts.Skip != "claude,codex" {
		t.Fatalf("Skip = %q, want from env", opts.Skip)
	}
	if !opts.Serial || !opts.Safe {
		t.Fatalf("Serial/Safe not applied from env: %+v", opts)
	}

	// Explicit flags win over the environment.
	opts, err = parseFlags([]string{"--timeout", "5s", "--skip", "amp"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.Timeout != 5*time.Second {
		t.Fatalf("flag --timeout should override env; got %v", opts.Timeout)
	}
	if opts.Skip != "amp" {
		t.Fatalf("flag --skip should override env; got %q", opts.Skip)
	}
	if !opts.Serial {
		t.Fatalf("UCA_SERIAL should still apply when --serial absent; got %+v", opts)
	}
}

func TestApplyEnvDefaultsIgnoresInvalid(t *testing.T) {
	t.Setenv("UCA_TIMEOUT", "not-a-duration")
	opts, err := parseFlags([]string{})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.Timeout != 15*time.Minute {
		t.Fatalf("invalid UCA_TIMEOUT should keep the default 15m; got %v", opts.Timeout)
	}
}

func TestValidateOptions(t *testing.T) {
	tests := []struct {
		name    string
		opts    options
		wantErr bool
	}{
		{name: "ok_default", opts: options{}, wantErr: false},
		{name: "ok_serial", opts: options{Serial: true}, wantErr: false},
		{name: "ok_parallel", opts: options{Parallel: true}, wantErr: false},
		{name: "ok_concurrency_zero", opts: options{Concurrency: 0}, wantErr: false},
		{name: "serial_and_parallel", opts: options{Serial: true, Parallel: true}, wantErr: true},
		{name: "negative_concurrency", opts: options{Concurrency: -1}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOptions(tt.opts)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateOptions(%#v) err = %v, wantErr %v", tt.opts, err, tt.wantErr)
			}
		})
	}
}

func TestParseFlagsReportsUnknownFlag(t *testing.T) {
	if _, err := parseFlags([]string{"--definitely-not-a-flag"}); err == nil {
		t.Fatal("parseFlags() with unknown flag returned nil error, want error")
	}
	opts, err := parseFlags([]string{"--serial", "-n", "--only", "claude"})
	if err != nil {
		t.Fatalf("parseFlags() valid args err = %v", err)
	}
	if !opts.Serial || !opts.DryRun || opts.Only != "claude" {
		t.Fatalf("parseFlags() parsed = %#v, want Serial/DryRun/Only=claude", opts)
	}
}

func TestParseFlagsRejectsPositionalArgs(t *testing.T) {
	// flag.Parse stops at the first positional, so without an explicit check
	// `uca claude --dry-run` would silently drop BOTH arguments and run a full
	// live update. Positionals must be a hard error.
	if _, err := parseFlags([]string{"claude", "--dry-run"}); err == nil {
		t.Fatal("parseFlags() with positional arg returned nil error, want error")
	}
	if _, err := parseFlags([]string{"--only", "claude", "extra"}); err == nil {
		t.Fatal("parseFlags() with trailing positional returned nil error, want error")
	}
}
