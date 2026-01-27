package main

import "testing"

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
