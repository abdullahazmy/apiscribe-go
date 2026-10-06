package ui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBannerLinesHaveEqualWidth(t *testing.T) {
	info := BannerInfo{Version: "0.1.0", Model: "claude-opus-5-5", Effort: "high",
		ProjectRoot: "/very/long/path/" + strings.Repeat("x", 80), DocsDir: "/tmp/none"}
	for _, cols := range []int{30, 60, 80, 120} {
		lines := RenderBanner(info, cols)
		want := utf8.RuneCountInString(lines[0])
		for i, l := range lines {
			if got := utf8.RuneCountInString(l); got != want {
				t.Fatalf("cols=%d line %d width %d, want %d: %q", cols, i, got, want, l)
			}
		}
	}
}
