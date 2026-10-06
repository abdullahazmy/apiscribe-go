package ui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

// logo is the apiscribe mark: an API doc page with a JSON brace.
var logo = []string{
	"▗▛▀▀▀▀▀▀▀▜▖",
	"▐  {   }  ▌",
	"▐  ━━━━━  ▌",
	"▐  ━━━    ▌",
	"▝▙▄▄▄▄▄▄▄▟▘",
}

type style func(string) string

func plain(s string) string { return s }

type segment struct {
	text  string
	style style
}

type cell []segment

func seg(text string, st style) segment { return segment{text, st} }

// fit truncates (with …) or pads a cell to width columns, optionally centered.
func fit(c cell, width int, center bool) string {
	room := width
	var out strings.Builder
	for _, s := range c {
		st := s.style
		if st == nil {
			st = plain
		}
		n := utf8.RuneCountInString(s.text)
		if n <= room {
			out.WriteString(st(s.text))
			room -= n
			continue
		}
		r := []rune(s.text)
		out.WriteString(st(string(r[:max(0, room-1)]) + "…"))
		room = 0
		break
	}
	used := width - room
	left := 0
	if center {
		left = (width - used) / 2
	}
	return strings.Repeat(" ", left) + out.String() + strings.Repeat(" ", width-used-left)
}

// shortPath uses ~ for home and a leading … when the path is too long.
func shortPath(p string, maxLen int) string {
	if home, err := os.UserHomeDir(); err == nil && (p == home || strings.HasPrefix(p, home+string(filepath.Separator))) {
		p = "~" + p[len(home):]
	}
	r := []rune(p)
	if len(r) <= maxLen {
		return p
	}
	return "…" + string(r[len(r)-maxLen+1:])
}

func ago(t time.Time) string {
	s := time.Since(t)
	switch {
	case s < time.Minute:
		return "just now"
	case s < time.Hour:
		return fmt.Sprintf("%dm ago", int(s.Minutes()))
	case s < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(s.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(s.Hours()/24))
}

// docsSummary returns "4 doc files · updated 2h ago", or a nudge to run /scan.
func docsSummary(docsDir string) cell {
	dist := filepath.Join(docsDir, "dist")
	count := 0
	var newest time.Time
	_ = filepath.WalkDir(docsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p == dist {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			count++
			if info, err := d.Info(); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		return nil
	})
	if count == 0 {
		return cell{seg("No docs yet — run ", Dim), seg("/scan", Accent)}
	}
	plural := "s"
	if count == 1 {
		plural = ""
	}
	return cell{seg(fmt.Sprintf("%d doc file%s", count, plural), nil), seg(" · updated "+ago(newest), Dim)}
}

// BannerInfo is what the welcome box shows.
type BannerInfo struct {
	Version, Model, Effort, ProjectRoot, DocsDir string
}

// RenderBanner draws the welcome box: two columns on wide terminals, one on narrow.
func RenderBanner(info BannerInfo, columns int) []string {
	width := min(max(columns, 40), 100)
	b := Accent
	version := info.Version
	if version != "" && version[0] >= '0' && version[0] <= '9' {
		version = "v" + version
	}
	title := " apiscribe " + version + " "
	top := b("╭───") + Bold(title) + b(strings.Repeat("─", max(0, width-5-utf8.RuneCountInString(title)))+"╮")
	bottom := b("╰" + strings.Repeat("─", width-2) + "╯")
	row := func(inner string) string { return b("│") + inner + b("│") }
	heading := func(s string) string { return Accent(Bold(s)) }

	docsRel, err := filepath.Rel(info.ProjectRoot, info.DocsDir)
	if err != nil || docsRel == "" {
		docsRel = "."
	}
	lw := width - 2 // the only column on narrow terminals
	if width >= 76 {
		lw = 40
	}
	left := []cell{
		{},
		{seg("Welcome to apiscribe!", Bold)},
		{seg("API docs for frontend & mobile teams", Dim)},
		{},
	}
	for _, l := range logo {
		left = append(left, cell{seg(l, Accent)})
	}
	left = append(left,
		cell{},
		cell{seg(info.Model+" · effort "+info.Effort, Dim)},
		cell{seg(shortPath(info.ProjectRoot, lw-4), Dim)},
	)
	right := []cell{
		{},
		{seg("Tips for getting started", heading)},
		{seg("/scan    ", Accent), seg("document every endpoint", nil)},
		{seg("/image   ", Accent), seg("map a screen to its APIs", nil)},
		{seg("/export  ", Accent), seg("build md · html · pdf", nil)},
		{seg("/help    ", Accent), seg("all commands", nil)},
		{},
		{seg("Docs", heading), seg("  "+filepath.ToSlash(docsRel)+"/", Dim)},
		docsSummary(info.DocsDir),
	}

	inner := width - 2
	lines := []string{top}
	if width >= 76 {
		rw := inner - lw - 1
		for i := 0; i < max(len(left), len(right)); i++ {
			var lc, rc cell
			if i < len(left) {
				lc = left[i]
			}
			if i < len(right) {
				rc = right[i]
			}
			lines = append(lines, row(" "+fit(lc, lw-2, true)+" "+b("│")+fit(append(cell{seg(" ", nil)}, rc...), rw, false)))
		}
	} else {
		for _, lc := range left {
			lines = append(lines, row(" "+fit(lc, inner-2, true)+" "))
		}
		for _, rc := range right {
			lines = append(lines, row(fit(append(cell{seg("  ", nil)}, rc...), inner, false)))
		}
	}
	return append(lines, bottom)
}

// PrintBanner prints the welcome box sized to the terminal.
func PrintBanner(info BannerInfo) {
	columns := 80
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		columns = w
	}
	for _, l := range RenderBanner(info, columns) {
		fmt.Println(l)
	}
	fmt.Println()
}
