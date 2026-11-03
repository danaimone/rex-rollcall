package termtext

import (
	"strings"
	"testing"
)

func TestTruncateFitsDisplayWidth(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"one too long", 11, "one too lo…"},
		{"日本語のタイトル", 7, "日本語…"},
		{"fix 🔒 lock", 7, "fix 🔒…"},
		{"fix 🔒 lock", 6, "fix…"},
		{"fix 🔒 lock", 5, "fix…"},
		{"trailing space here", 10, "trailing…"},
	}
	for _, tt := range tests {
		got := Truncate(tt.in, tt.width)
		if got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
		if Width(got) > tt.width {
			t.Errorf("Truncate(%q, %d) is %d columns wide", tt.in, tt.width, Width(got))
		}
	}
}

func TestWidthCountsWideAndZeroWidthRunes(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"abc", 3},
		{"日本", 4},
		{"🔒", 2},
		{"é", 1},
		{"✻ ◇ π", 5},
	}
	for _, tt := range tests {
		if got := Width(tt.in); got != tt.want {
			t.Errorf("Width(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestWrapBreaksOnWordsWithinWidth(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{"fits", "hello world", 20, []string{"hello world"}},
		{"word boundaries", "the quick brown fox jumps", 10, []string{"the quick", "brown fox", "jumps"}},
		{"bullet hangs", "- alpha beta gamma delta", 12, []string{"- alpha beta", "  gamma", "  delta"}},
		{"keeps indent", "    one two three four", 14, []string{"    one two", "    three four"}},
		{"long word splits", "see /a/very/long/path/name ok", 10, []string{"see", "/a/very/lo", "ng/path/na", "me ok"}},
		{"wide runes", "日本語 日本語 日本語", 13, []string{"日本語 日本語", "日本語"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Wrap(tt.in, tt.width)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("Wrap = %q, want %q", got, tt.want)
			}
			for _, l := range got {
				if Width(l) > tt.width {
					t.Errorf("line %q is %d columns, over %d", l, Width(l), tt.width)
				}
			}
		})
	}
}
