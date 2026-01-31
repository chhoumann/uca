package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseVersionOutput(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "empty",
			out:  "",
			want: "unknown",
		},
		{
			name: "version_only",
			out:  "1.2.3\n",
			want: "1.2.3",
		},
		{
			name: "version_only_with_v",
			out:  "v2.0.1\n",
			want: "v2.0.1",
		},
		{
			name: "first_line_default",
			out:  "claude 2.1.19\n",
			want: "claude 2.1.19",
		},
		{
			name: "selects_last_version_only_line",
			out:  "INFO something\n1.1.36\n",
			want: "1.1.36",
		},
		{
			name: "skips_blank_lines",
			out:  "\n\n1.4.0\n\n",
			want: "1.4.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseVersionOutput(tt.out); got != tt.want {
				t.Fatalf("parseVersionOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShouldRetryNpmInstall(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		output string
		want   bool
	}{
		{
			name:   "enotempty",
			args:   []string{"npm", "install", "-g", "pkg"},
			output: "npm error ENOTEMPTY: directory not empty",
			want:   true,
		},
		{
			name:   "errno",
			args:   []string{"npm", "install", "-g", "pkg"},
			output: "npm error errno -66",
			want:   true,
		},
		{
			name:   "directory_not_empty",
			args:   []string{"npm", "install", "-g", "pkg"},
			output: "npm error directory not empty",
			want:   true,
		},
		{
			name:   "no_match",
			args:   []string{"npm", "install", "-g", "pkg"},
			output: "some other error",
			want:   false,
		},
		{
			name:   "not_install",
			args:   []string{"npm", "i", "-g", "pkg"},
			output: "npm error ENOTEMPTY",
			want:   false,
		},
		{
			name:   "not_npm",
			args:   []string{"bun", "install", "-g", "pkg"},
			output: "npm error ENOTEMPTY",
			want:   false,
		},
		{
			name:   "args_too_short",
			args:   []string{"npm"},
			output: "npm error ENOTEMPTY",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRetryNpmInstall(tt.args, tt.output); got != tt.want {
				t.Fatalf("shouldRetryNpmInstall() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatRetryOutput(t *testing.T) {
	tests := []struct {
		name   string
		first  string
		msg    string
		second string
		want   string
	}{
		{
			name:   "first_empty",
			first:  "",
			msg:    "",
			second: "retry output",
			want:   "retry output",
		},
		{
			name:   "second_empty",
			first:  "first output",
			msg:    "",
			second: "   ",
			want:   "first output",
		},
		{
			name:   "both_present",
			first:  "first output",
			msg:    "",
			second: "second output",
			want:   "first output\n\n(uca) retrying npm install after ENOTEMPTY\nsecond output",
		},
		{
			name:   "with_cleanup_msg",
			first:  "first output",
			msg:    "removed stale npm temp dir /tmp/.pkg-abc",
			second: "second output",
			want:   "first output\n\n(uca) removed stale npm temp dir /tmp/.pkg-abc\n(uca) retrying npm install after ENOTEMPTY\nsecond output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatRetryOutput(tt.first, tt.msg, tt.second)
			if got != tt.want {
				t.Fatalf("formatRetryOutput() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "\n\n\n") {
				t.Fatalf("formatRetryOutput() has extra newlines: %q", got)
			}
		})
	}
}

func TestClassifyUpdateFailure(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		output     string
		wantReason string
		wantHint   string
	}{
		{
			name:       "quota",
			args:       []string{"gemini", "update"},
			output:     "TerminalQuotaError: You have exhausted your capacity on this model.",
			wantReason: reasonQuota,
			wantHint:   "quota exceeded",
		},
		{
			name:       "npm_enotempty",
			args:       []string{"npm", "install", "-g", "pkg"},
			output:     "npm error ENOTEMPTY: directory not empty",
			wantReason: reasonNpmNotEmpty,
			wantHint:   "npm rename failed",
		},
		{
			name:       "enotempty_non_npm",
			args:       []string{"gemini", "update"},
			output:     "ENOTEMPTY",
			wantReason: "",
			wantHint:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReason, gotHint := classifyUpdateFailure(tt.args, tt.output)
			if gotReason != tt.wantReason {
				t.Fatalf("classifyUpdateFailure() reason = %q, want %q", gotReason, tt.wantReason)
			}
			if tt.wantHint != "" && !strings.Contains(gotHint, tt.wantHint) {
				t.Fatalf("classifyUpdateFailure() hint = %q, want to contain %q", gotHint, tt.wantHint)
			}
			if tt.wantHint == "" && gotHint != "" {
				t.Fatalf("classifyUpdateFailure() hint = %q, want empty", gotHint)
			}
		})
	}
}

func TestAppendHint(t *testing.T) {
	tests := []struct {
		name   string
		detail string
		hint   string
		want   string
	}{
		{
			name:   "empty_detail",
			detail: "",
			hint:   "try again",
			want:   "hint: try again",
		},
		{
			name:   "with_detail",
			detail: "binary found",
			hint:   "try again",
			want:   "binary found; hint: try again",
		},
		{
			name:   "empty_hint",
			detail: "binary found",
			hint:   "",
			want:   "binary found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := appendHint(tt.detail, tt.hint); got != tt.want {
				t.Fatalf("appendHint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsNpmInstall(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{
			name: "npm_install",
			args: []string{"npm", "install", "-g", "pkg"},
			want: true,
		},
		{
			name: "npm_i",
			args: []string{"npm", "i", "-g", "pkg"},
			want: false,
		},
		{
			name: "short",
			args: []string{"npm"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNpmInstall(tt.args); got != tt.want {
				t.Fatalf("isNpmInstall() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractNpmRenamePaths(t *testing.T) {
	dir := "/tmp/npm"
	path := filepath.Join(dir, "pi-coding-agent")
	dest := filepath.Join(dir, ".pi-coding-agent-abc")
	tests := []struct {
		name   string
		output string
		wantP  string
		wantD  string
	}{
		{
			name: "path_dest_lines",
			output: "npm error path " + path + "\n" +
				"npm error dest " + dest + "\n",
			wantP: path,
			wantD: dest,
		},
		{
			name:   "rename_line",
			output: "npm error ENOTEMPTY: directory not empty, rename '" + path + "' -> '" + dest + "'\n",
			wantP:  path,
			wantD:  dest,
		},
		{
			name:   "no_match",
			output: "some other error",
			wantP:  "",
			wantD:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotP, gotD := extractNpmRenamePaths(tt.output)
			if gotP != tt.wantP || gotD != tt.wantD {
				t.Fatalf("extractNpmRenamePaths() = %q, %q want %q, %q", gotP, gotD, tt.wantP, tt.wantD)
			}
		})
	}
}

func TestIsSafeNpmRenameTarget(t *testing.T) {
	baseDir := "/tmp/npm"
	path := filepath.Join(baseDir, "pi-coding-agent")
	dest := filepath.Join(baseDir, ".pi-coding-agent-abc")

	tests := []struct {
		name string
		p    string
		d    string
		want bool
	}{
		{
			name: "ok",
			p:    path,
			d:    dest,
			want: true,
		},
		{
			name: "different_dir",
			p:    path,
			d:    filepath.Join("/tmp/other", ".pi-coding-agent-abc"),
			want: false,
		},
		{
			name: "wrong_prefix",
			p:    path,
			d:    filepath.Join(baseDir, ".other-abc"),
			want: false,
		},
		{
			name: "relative",
			p:    "pi-coding-agent",
			d:    ".pi-coding-agent-abc",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSafeNpmRenameTarget(tt.p, tt.d); got != tt.want {
				t.Fatalf("isSafeNpmRenameTarget() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCleanupNpmENotEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pi-coding-agent")
	dest := filepath.Join(dir, ".pi-coding-agent-abc")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	output := "npm error path " + path + "\n" +
		"npm error dest " + dest + "\n"
	msg := cleanupNpmENotEmpty(output)
	if msg == "" {
		t.Fatalf("cleanupNpmENotEmpty() returned empty message")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatalf("cleanupNpmENotEmpty() did not remove %q", dest)
	}
}
