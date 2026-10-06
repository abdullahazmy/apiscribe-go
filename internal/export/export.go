// Package export merges the Markdown docs and renders them to a single
// Markdown file, a self-contained HTML page, and a PDF.
package export

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"

	"github.com/abdullahazmy/apiscribe-go/internal/config"
)

var (
	//go:embed assets/style.css
	css string
	//go:embed assets/script.js
	script string
)

// Formats lists the supported export formats.
var Formats = []string{"md", "html", "pdf"}

type docFile struct {
	rel    string // relative to the docs dir, slash-separated
	anchor string
	title  string
	body   string
}

var (
	methodRe  = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|WS|SSE)\s+(\S.*)$`)
	h1Re      = regexp.MustCompile(`(?m)^#\s+(.+)$`)
	mdLinkRe  = regexp.MustCompile(`(?i)\]\(([^)\s]+?\.md)(#[^)\s]*)?\)`)
	headingRe = regexp.MustCompile(`(?s)<h([1-6]) id="([^"]*)">(.*?)</h[1-6]>`)
	tagRe     = regexp.MustCompile(`<[^>]+>`)
	statusRe  = regexp.MustCompile(`^([1-5])\d\d\b`)
	schemeRe  = regexp.MustCompile(`(?i)^[a-z]+://`)
	nonAlnum  = regexp.MustCompile(`[^a-z0-9]+`)
)

func collect(cfg *config.Config) ([]docFile, error) {
	dist := filepath.Join(cfg.DocsDir, "dist")
	var files []string
	err := filepath.WalkDir(cfg.DocsDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p == dist {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			rel, _ := filepath.Rel(cfg.DocsDir, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rank := func(f string) int {
		switch {
		case strings.EqualFold(f, "README.md"):
			return 0
		case strings.HasPrefix(f, "endpoints/"):
			return 1
		case strings.HasPrefix(f, "screens/"):
			return 2
		}
		return 3
	}
	sort.Slice(files, func(i, j int) bool {
		if rank(files[i]) != rank(files[j]) {
			return rank(files[i]) < rank(files[j])
		}
		return files[i] < files[j]
	})
	docs := make([]docFile, 0, len(files))
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(cfg.DocsDir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		title := titleFromPath(rel)
		if m := h1Re.FindStringSubmatch(string(b)); m != nil {
			title = strings.TrimSpace(m[1])
		}
		docs = append(docs, docFile{rel: rel, anchor: fileAnchor(rel), title: title, body: string(b)})
	}
	return docs, nil
}

func fileAnchor(rel string) string {
	base := strings.TrimSuffix(strings.ToLower(rel), ".md")
	return "doc-" + strings.Trim(nonAlnum.ReplaceAllString(base, "-"), "-")
}

func titleFromPath(rel string) string {
	base := strings.NewReplacer("-", " ", "_", " ").Replace(strings.TrimSuffix(path.Base(rel), ".md"))
	if base == "" {
		return rel
	}
	return strings.ToUpper(base[:1]) + base[1:]
}

// slugify matches GitHub's heading anchors, so links Claude writes
// (#post-apiorders) resolve.
func slugify(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte('-')
		}
	}
	return b.String()
}

// rewriteLinks turns cross-file links (endpoints/orders.md#x) into in-page anchors (#x).
func rewriteLinks(d docFile, known map[string]bool) string {
	return mdLinkRe.ReplaceAllStringFunc(d.body, func(whole string) string {
		m := mdLinkRe.FindStringSubmatch(whole)
		target, frag := m[1], m[2]
		if schemeRe.MatchString(target) {
			return whole
		}
		resolved := path.Clean(path.Join(path.Dir(d.rel), target))
		if !known[resolved] {
			return whole
		}
		if frag != "" {
			return "](" + frag + ")"
		}
		return "](#" + fileAnchor(resolved) + ")"
	})
}

var pkgNameRes = []struct {
	file string
	re   *regexp.Regexp
}{
	{"go.mod", regexp.MustCompile(`(?m)^module\s+(\S+)`)},
	{"package.json", regexp.MustCompile(`"name"\s*:\s*"([^"]+)"`)},
	{"pyproject.toml", regexp.MustCompile(`(?m)^name\s*=\s*"([^"]+)"`)},
	{"Cargo.toml", regexp.MustCompile(`(?m)^name\s*=\s*"([^"]+)"`)},
	{"composer.json", regexp.MustCompile(`"name"\s*:\s*"([^"]+)"`)},
}

func projectName(cfg *config.Config) string {
	for _, c := range pkgNameRes {
		if b, err := os.ReadFile(filepath.Join(cfg.ProjectRoot, c.file)); err == nil {
			if m := c.re.FindSubmatch(b); m != nil {
				return path.Base(string(m[1]))
			}
		}
	}
	return filepath.Base(cfg.ProjectRoot)
}

// Result lists written files and non-fatal problems.
type Result struct {
	Files    []string
	Warnings []string
}

// Run exports the docs in the requested formats to <docs>/dist.
func Run(cfg *config.Config, formats []string) (*Result, error) {
	docs, err := collect(cfg)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("no Markdown docs found in %s. Run /scan first", cfg.DocsDir)
	}
	outDir := filepath.Join(cfg.DocsDir, "dist")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, d := range docs {
		known[d.rel] = true
	}
	want := map[string]bool{}
	for _, f := range formats {
		want[f] = true
	}
	name := projectName(cfg)
	generated := time.Now().Format("2006-01-02")
	res := &Result{}

	if want["md"] {
		var b strings.Builder
		fmt.Fprintf(&b, "# %s — API Documentation\n\n_Generated %s by apiscribe._\n\n## Contents\n\n", name, generated)
		for _, d := range docs {
			fmt.Fprintf(&b, "- [%s](#%s)\n", d.title, d.anchor)
		}
		b.WriteString("\n---\n\n")
		for i, d := range docs {
			if i > 0 {
				b.WriteString("\n---\n\n")
			}
			fmt.Fprintf(&b, "<a id=\"%s\"></a>\n\n%s\n", d.anchor, strings.TrimSpace(rewriteLinks(d, known)))
		}
		out := filepath.Join(outDir, "API_DOCUMENTATION.md")
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			return nil, err
		}
		res.Files = append(res.Files, out)
	}

	if want["html"] || want["pdf"] {
		page, err := renderHTML(docs, known, name, generated)
		if err != nil {
			return nil, err
		}
		htmlOut := filepath.Join(outDir, "API_DOCUMENTATION.html")
		if want["html"] {
			if err := os.WriteFile(htmlOut, []byte(page), 0o644); err != nil {
				return nil, err
			}
			res.Files = append(res.Files, htmlOut)
		}
		if want["pdf"] {
			pdfOut := filepath.Join(outDir, "API_DOCUMENTATION.pdf")
			if err := renderPDF(page, pdfOut, name); err != nil {
				res.Warnings = append(res.Warnings, "PDF skipped: "+err.Error())
			} else {
				res.Files = append(res.Files, pdfOut)
			}
		}
	}
	return res, nil
}

// slugIDs gives headings GitHub-style ids, unique across the whole page.
type slugIDs struct{ used map[string]int }

func (s *slugIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	base := slugify(string(value))
	if base == "" {
		base = "section"
	}
	n := s.used[base]
	s.used[base] = n + 1
	if n == 0 {
		return []byte(base)
	}
	return []byte(fmt.Sprintf("%s-%d", base, n))
}

func (s *slugIDs) Put(value []byte) { s.used[string(value)]++ }

type navItem struct{ id, label, method string }

func renderHTML(docs []docFile, known map[string]bool, name, generated string) (string, error) {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			highlighting.NewHighlighting(highlighting.WithFormatOptions(chromahtml.WithClasses(true))),
		),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
	)
	ids := &slugIDs{used: map[string]int{}}

	var sections, groups []string
	for _, d := range docs {
		var buf bytes.Buffer
		ctx := parser.NewContext(parser.WithIDs(ids))
		if err := md.Convert([]byte(rewriteLinks(d, known)), &buf, parser.WithContext(ctx)); err != nil {
			return "", err
		}
		var nav []navItem
		body := headingRe.ReplaceAllStringFunc(buf.String(), func(h string) string {
			m := headingRe.FindStringSubmatch(h)
			depth, id, inner := m[1], m[2], m[3]
			plain := strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(inner, "")))
			if mm := methodRe.FindStringSubmatch(plain); mm != nil {
				method := mm[1]
				inner = fmt.Sprintf(`<span class="method m-%s">%s</span><code class="route">%s</code>`,
					strings.ToLower(method), method, html.EscapeString(mm[2]))
				if depth <= "2" {
					nav = append(nav, navItem{id, mm[2], method})
				}
			} else if depth == "2" {
				nav = append(nav, navItem{id: id, label: plain})
			}
			if depth >= "3" {
				if sm := statusRe.FindStringSubmatch(plain); sm != nil {
					inner = fmt.Sprintf(`<span class="status s%sxx">%s</span>`, sm[1], inner)
				}
			}
			return fmt.Sprintf(`<h%s id="%s"><a class="anchor" href="#%s">#</a>%s</h%s>`, depth, id, id, inner, depth)
		})
		// Wrap tables so wide ones scroll instead of breaking the layout.
		body = strings.ReplaceAll(body, "<table>", `<div class="table-wrap"><table>`)
		body = strings.ReplaceAll(body, "</table>", "</table></div>")
		sections = append(sections, fmt.Sprintf("<section class=\"doc\" id=\"%s\" data-file=\"%s\">\n%s\n</section>",
			d.anchor, html.EscapeString(d.rel), body))

		var items strings.Builder
		for _, n := range nav {
			badge := ""
			if n.method != "" {
				badge = fmt.Sprintf(`<span class="method m-%s">%s</span>`, strings.ToLower(n.method), n.method)
			}
			fmt.Fprintf(&items, `<li><a href="#%s">%s<span class="label">%s</span></a></li>`, n.id, badge, html.EscapeString(n.label))
		}
		group := fmt.Sprintf(`<div class="nav-group"><a class="nav-title" href="#%s">%s</a>`, d.anchor, html.EscapeString(d.title))
		if items.Len() > 0 {
			group += "<ul>" + items.String() + "</ul>"
		}
		groups = append(groups, group+"</div>")
	}

	n := html.EscapeString(name)
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%[1]s — API Documentation</title>
<style>%[2]s</style>
</head>
<body>
<button class="menu" aria-label="Toggle navigation" onclick="document.body.classList.toggle('nav-open')">☰</button>
<aside class="sidebar">
  <div class="brand"><div class="brand-name">%[1]s</div><div class="brand-sub">API Documentation</div></div>
  <input class="filter" type="search" placeholder="Filter endpoints…" aria-label="Filter endpoints">
  <nav>%[3]s</nav>
</aside>
<main>
  <header class="cover">
    <div class="eyebrow">API Reference</div>
    <h1 class="cover-title">%[1]s</h1>
    <p class="cover-sub">Integration guide for frontend and mobile developers · generated %[4]s</p>
  </header>
  %[5]s
  <footer>Generated by apiscribe · %[4]s</footer>
</main>
<script>%[6]s</script>
</body>
</html>`, n, css, strings.Join(groups, "\n"), generated, strings.Join(sections, "\n"), script), nil
}

func chromeCandidates() []string {
	c := []string{os.Getenv("APISCRIBE_CHROME"), os.Getenv("CHROME_PATH")}
	switch runtime.GOOS {
	case "darwin":
		c = append(c,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge")
	case "windows":
		c = append(c,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`)
	default:
		c = append(c, "/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable", "/usr/bin/microsoft-edge", "/snap/bin/chromium")
	}
	return c
}

func findChrome() (string, error) {
	for _, c := range chromeCandidates() {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("no Chrome/Chromium/Edge found. Install one or set APISCRIBE_CHROME=/path/to/chrome")
}

func renderPDF(pageHTML, out, name string) error {
	exe, err := findChrome()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "apiscribe-*.html")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(pageHTML); err != nil {
		return err
	}
	tmp.Close()

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(exe), chromedp.NoSandbox)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelTimeout()

	const mm = 1 / 25.4 // inches per millimetre
	esc := html.EscapeString(name)
	if err := chromedp.Do(ctx, chromedp.Navigate("file://"+filepath.ToSlash(tmp.Name()))); err != nil {
		return err
	}
	// Printing always uses the print stylesheet, so no media emulation is needed.
	pdf, err := chromedp.Run(ctx, chromedp.PrintToPDF(
		chromedp.PDFPaper(chromedp.PaperA4),
		chromedp.PDFPrintBackground(),
		chromedp.PDFHeaderTemplate(`<div style="font-size:8px;color:#888;width:100%;padding:0 14mm;font-family:sans-serif">`+esc+` · API Documentation</div>`),
		chromedp.PDFFooterTemplate(`<div style="font-size:8px;color:#888;width:100%;padding:0 14mm;text-align:right;font-family:sans-serif"><span class="pageNumber"></span> / <span class="totalPages"></span></div>`),
		func(p *page.PrintToPDFParams) {
			top, side := 18*mm, 14*mm
			p.MarginTop, p.MarginBottom, p.MarginLeft, p.MarginRight = &top, &top, &side, &side
		},
	))
	if err != nil {
		return err
	}
	return os.WriteFile(out, pdf, 0o644)
}
