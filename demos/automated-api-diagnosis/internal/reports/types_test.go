package reports

import "testing"

func TestStripReasoning(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no reasoning",
			in:   "# Summary\n\nAll good.",
			want: "# Summary\n\nAll good.",
		},
		{
			name: "leading block",
			in:   "<reasoning>\nthinking...\n</reasoning>\n\n# Summary\n\nDetails.",
			want: "# Summary\n\nDetails.",
		},
		{
			name: "block in the middle",
			in:   "# A\n\n<reasoning>hidden</reasoning>\n\n## B\n\ntext",
			want: "# A\n\n\n\n## B\n\ntext",
		},
		{
			name: "multiple blocks",
			in:   "<reasoning>one</reasoning>keep<reasoning>two</reasoning>",
			want: "keep",
		},
		{
			name: "mixed case tags",
			in:   "<Reasoning>x</REASONING>shown",
			want: "shown",
		},
		{
			name: "only reasoning",
			in:   "<reasoning>x</reasoning>",
			want: "",
		},
		{
			name: "other tags untouched",
			in:   "<details>keep</details>",
			want: "<details>keep</details>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripReasoning(tt.in); got != tt.want {
				t.Errorf("StripReasoning(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
