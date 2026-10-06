// Package tools implements the file tools Claude uses to read the backend
// and write documentation.
package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/abdullahazmy/apiscribe-go/internal/config"
)

const (
	maxList      = 1500
	maxReadLines = 2000
	maxLineLen   = 2000
	maxMatches   = 250
)

var ignoredDirs = map[string]bool{
	"node_modules": true, ".git": true, ".hg": true, ".svn": true, "dist": true, "build": true, "out": true,
	"coverage": true, "vendor": true, ".venv": true, "venv": true, "__pycache__": true, ".mypy_cache": true,
	".pytest_cache": true, "target": true, "bin": true, "obj": true, ".next": true, ".nuxt": true, ".idea": true,
	".vscode": true, ".gradle": true, ".dart_tool": true, "storage": true, "tmp": true,
}

var docExtensions = map[string]bool{".md": true, ".json": true, ".yaml": true, ".yml": true}

func schema(props map[string]any, required ...string) anthropic.BetaToolInputSchemaParam {
	return anthropic.BetaToolInputSchemaParam{
		Properties:  props,
		Required:    required,
		ExtraFields: map[string]any{"additionalProperties": false},
	}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

// Definitions returns the tool schemas sent to Claude, in a stable order so
// the prompt cache keeps hitting.
func Definitions() []anthropic.BetaToolUnionParam {
	defs := []anthropic.BetaToolParam{
		{
			Name: "list_files",
			Description: anthropic.String("List files under a directory of the backend project (relative to the project root). " +
				"Common build/vendor folders are skipped. Use `glob` (e.g. \"**/*.controller.ts\", \"routes/**\") to filter."),
			InputSchema: schema(map[string]any{
				"path":      str("Directory relative to the project root. Defaults to the root."),
				"glob":      str("Optional glob filter matched against the path relative to `path`."),
				"max_depth": map[string]any{"type": "integer", "description": "Maximum directory depth (default 12)."},
			}),
		},
		{
			Name: "read_file",
			Description: anthropic.String(fmt.Sprintf("Read a text file from the backend project with line numbers. "+
				"Returns at most %d lines per call; use offset/limit for long files.", maxReadLines)),
			InputSchema: schema(map[string]any{
				"path":   str("File path relative to the project root."),
				"offset": map[string]any{"type": "integer", "description": "1-based line to start from."},
				"limit":  map[string]any{"type": "integer", "description": "Number of lines to read."},
			}, "path"),
		},
		{
			Name: "search",
			Description: anthropic.String("Regex search across the backend project's files (ripgrep syntax). Returns file:line:match. " +
				"Great for finding route registrations, decorators, DTOs, validators, middleware, and error handlers."),
			InputSchema: schema(map[string]any{
				"pattern":     str("Regular expression to search for."),
				"path":        str("Directory or file to search, relative to the project root."),
				"glob":        str("Only search files matching this glob, e.g. \"*.py\"."),
				"ignore_case": map[string]any{"type": "boolean"},
			}, "pattern"),
		},
		{
			Name: "write_doc",
			Description: anthropic.String("Create or overwrite a documentation file inside the docs directory. `path` is relative to the docs " +
				"directory (e.g. \"README.md\", \"endpoints/users.md\", \"screens/login.md\"). Only .md, .json, .yaml and .yml files " +
				"are allowed. Write one complete file per call."),
			EagerInputStreaming: anthropic.Bool(true),
			InputSchema: schema(map[string]any{
				"path":    str("Path relative to the docs directory."),
				"content": str("Full file content."),
			}, "path", "content"),
		},
	}
	out := make([]anthropic.BetaToolUnionParam, len(defs))
	for i := range defs {
		out[i] = anthropic.BetaToolUnionParam{OfTool: &defs[i]}
	}
	return out
}

// Outcome is the result of one tool call.
type Outcome struct {
	Content string // sent back to Claude
	IsError bool
	Summary string // short line for the terminal
}

func fail(format string, args ...any) Outcome {
	msg := fmt.Sprintf(format, args...)
	return Outcome{Content: msg, IsError: true, Summary: msg}
}

type listInput struct {
	Path     string `json:"path"`
	Glob     string `json:"glob"`
	MaxDepth int    `json:"max_depth"`
}
type readInput struct {
	Path   *string `json:"path"`
	Offset int     `json:"offset"`
	Limit  int     `json:"limit"`
}
type searchInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignore_case"`
}
type writeInput struct {
	Path    *string `json:"path"`
	Content *string `json:"content"`
}

// decode strictly parses tool input: unknown fields or malformed JSON are
// errors, since eager input streaming means the server no longer validates.
func decode(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// Describe renders a one-line description of a call for the terminal.
func Describe(name string, raw json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	s := func(k string) string {
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	switch name {
	case "list_files":
		parts := []string{firstNonEmpty(s("path"), ".")}
		if g := s("glob"); g != "" {
			parts = append(parts, g)
		}
		return strings.Join(parts, ", ")
	case "read_file", "write_doc":
		return s("path")
	case "search":
		d := `"` + s("pattern") + `"`
		if p := s("path"); p != "" {
			d += " in " + p
		}
		if g := s("glob"); g != "" {
			d += " (" + g + ")"
		}
		return d
	}
	return ""
}

// Run executes a tool call.
func Run(cfg *config.Config, name string, raw json.RawMessage) Outcome {
	bad := func(err error) Outcome {
		return fail("Invalid input for %s: %v. Re-issue the call with complete, valid JSON.", name, err)
	}
	switch name {
	case "list_files":
		var in listInput
		if err := decode(raw, &in); err != nil {
			return bad(err)
		}
		return listFiles(cfg, in)
	case "read_file":
		var in readInput
		if err := decode(raw, &in); err != nil {
			return bad(err)
		}
		if in.Path == nil {
			return bad(errors.New("path is required"))
		}
		return readFile(cfg, in)
	case "search":
		var in searchInput
		if err := decode(raw, &in); err != nil {
			return bad(err)
		}
		if in.Pattern == "" {
			return bad(errors.New("pattern is required"))
		}
		return search(cfg, in)
	case "write_doc":
		var in writeInput
		if err := decode(raw, &in); err != nil {
			return bad(err)
		}
		if in.Path == nil || *in.Path == "" || in.Content == nil {
			return bad(errors.New("path and content are required"))
		}
		return writeDoc(cfg, *in.Path, *in.Content)
	}
	return fail("Unknown tool: %s", name)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func relToRoot(cfg *config.Config, abs string) string {
	r, err := filepath.Rel(cfg.ProjectRoot, abs)
	if err != nil || r == "" {
		return abs
	}
	return filepath.ToSlash(r)
}

func listFiles(cfg *config.Config, in listInput) Outcome {
	base, err := config.Confine(cfg.ProjectRoot, firstNonEmpty(in.Path, "."))
	if err != nil {
		return fail("%v", err)
	}
	var matcher *regexp.Regexp
	if in.Glob != "" {
		matcher = GlobToRegexp(in.Glob)
	}
	maxDepth := in.MaxDepth
	if maxDepth <= 0 || maxDepth > 30 {
		maxDepth = 12
	}
	out, truncated := walkFiles(cfg, base, matcher, maxDepth)
	content := strings.Join(out, "\n")
	if content == "" {
		content = "(no files)"
	}
	summary := fmt.Sprintf("%d files", len(out))
	if truncated {
		content += fmt.Sprintf("\n… truncated at %d entries; narrow with path/glob.", maxList)
		summary = fmt.Sprintf("%d+ files", len(out))
	}
	return Outcome{Content: content, Summary: summary}
}

func walkFiles(cfg *config.Config, base string, matcher *regexp.Regexp, maxDepth int) (out []string, truncated bool) {
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if truncated {
				return
			}
			abs := filepath.Join(dir, e.Name())
			if abs == cfg.DocsDir {
				continue
			}
			if e.IsDir() {
				if ignoredDirs[e.Name()] || (strings.HasPrefix(e.Name(), ".") && e.Name() != ".github") {
					continue
				}
				if depth < maxDepth {
					walk(abs, depth+1)
				}
				continue
			}
			if !e.Type().IsRegular() {
				continue
			}
			if matcher != nil {
				r, _ := filepath.Rel(base, abs)
				if !matcher.MatchString(filepath.ToSlash(r)) && !matcher.MatchString(e.Name()) {
					continue
				}
			}
			out = append(out, relToRoot(cfg, abs))
			if len(out) >= maxList {
				truncated = true
				return
			}
		}
	}
	walk(base, 1)
	return out, truncated
}

func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}

func readFile(cfg *config.Config, in readInput) Outcome {
	abs, err := config.Confine(cfg.ProjectRoot, *in.Path)
	if err != nil {
		return fail("%v", err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return fail("%s: file not found", *in.Path)
	}
	if !st.Mode().IsRegular() {
		return fail("%s is not a file", *in.Path)
	}
	if st.Size() > 5<<20 {
		return fail("%s is larger than 5MB; use search instead", *in.Path)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fail("%v", err)
	}
	if isBinary(data) {
		return fail("%s looks like a binary file", *in.Path)
	}
	lines := strings.Split(string(data), "\n")
	start := max(in.Offset, 1) - 1
	limit := in.Limit
	if limit <= 0 || limit > maxReadLines {
		limit = maxReadLines
	}
	end := min(len(lines), start+limit)
	if start > end {
		start = end
	}
	var b strings.Builder
	for i, l := range lines[start:end] {
		if len(l) > maxLineLen {
			l = l[:maxLineLen] + "…"
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%6d\t%s", start+i+1, l)
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "\n… %d more lines (continue with offset=%d)", len(lines)-end, end+1)
	}
	return Outcome{Content: b.String(), Summary: fmt.Sprintf("%d lines", end-start)}
}

func search(cfg *config.Config, in searchInput) Outcome {
	target, err := config.Confine(cfg.ProjectRoot, firstNonEmpty(in.Path, "."))
	if err != nil {
		return fail("%v", err)
	}
	var lines []string
	if _, lookErr := exec.LookPath("rg"); lookErr == nil {
		args := []string{"-n", "--no-heading", "--color=never", "--max-columns=400", "--max-count=50"}
		if in.IgnoreCase {
			args = append(args, "-i")
		}
		if in.Glob != "" {
			args = append(args, "-g", in.Glob)
		}
		for d := range ignoredDirs {
			args = append(args, "-g", "!"+d+"/")
		}
		if rel, err := filepath.Rel(cfg.ProjectRoot, cfg.DocsDir); err == nil {
			args = append(args, "-g", "!"+filepath.ToSlash(rel)+"/")
		}
		args = append(args, "-e", in.Pattern, relToRoot(cfg, target))
		cmd := exec.Command("rg", args...)
		cmd.Dir = cfg.ProjectRoot
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, runErr := cmd.Output()
		var exitErr *exec.ExitError
		switch {
		case runErr == nil:
			lines = splitNonEmpty(string(stdout))
		case errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1:
			// rg: no matches
		default:
			return fail("search failed: %s", firstNonEmpty(strings.TrimSpace(stderr.String()), runErr.Error()))
		}
	} else {
		lines, err = goSearch(cfg, target, in)
		if err != nil {
			return fail("search failed: %v", err)
		}
	}
	total := len(lines)
	shown := lines[:min(total, maxMatches)]
	content := strings.Join(shown, "\n")
	if content == "" {
		content = "(no matches)"
	}
	if total > maxMatches {
		content += fmt.Sprintf("\n… %d more matches; narrow the pattern or path.", total-maxMatches)
	}
	return Outcome{Content: content, Summary: fmt.Sprintf("%d matches", total)}
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// goSearch is the fallback when ripgrep is not installed.
func goSearch(cfg *config.Config, target string, in searchInput) ([]string, error) {
	pat := in.Pattern
	if in.IgnoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, err
	}
	var matcher *regexp.Regexp
	if in.Glob != "" {
		matcher = GlobToRegexp(in.Glob)
	}
	var files []string
	if st, err := os.Stat(target); err == nil && st.Mode().IsRegular() {
		files = []string{relToRoot(cfg, target)}
	} else {
		files, _ = walkFiles(cfg, target, matcher, 30)
	}
	var out []string
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(cfg.ProjectRoot, f))
		if err != nil || isBinary(data) {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				if len(line) > 400 {
					line = line[:400]
				}
				out = append(out, fmt.Sprintf("%s:%d:%s", f, i+1, line))
			}
		}
		if len(out) > maxMatches*4 {
			break
		}
	}
	return out, nil
}

func writeDoc(cfg *config.Config, p, content string) Outcome {
	abs, err := config.Confine(cfg.DocsDir, p)
	if err != nil {
		return fail("%v", err)
	}
	if !docExtensions[strings.ToLower(filepath.Ext(abs))] {
		return fail("Only .md, .json, .yaml and .yml files can be written")
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fail("%v", err)
	}
	_, statErr := os.Stat(abs)
	existed := statErr == nil
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return fail("%v", err)
	}
	n := strings.Count(content, "\n")
	verb := "created"
	if existed {
		verb = "updated"
	}
	rel, _ := filepath.Rel(cfg.DocsDir, abs)
	return Outcome{
		Content: fmt.Sprintf("%s %s (%d lines)", strings.ToUpper(verb[:1])+verb[1:], filepath.ToSlash(rel), n),
		Summary: fmt.Sprintf("%s · %d lines", verb, n),
	}
}

// GlobToRegexp converts a glob supporting **, *, ? and {a,b} to a regexp.
func GlobToRegexp(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '{':
			end := strings.IndexByte(glob[i:], '}')
			if end < 0 {
				b.WriteString(`\{`)
				continue
			}
			alts := strings.Split(glob[i+1:i+end], ",")
			for j, a := range alts {
				alts[j] = regexp.QuoteMeta(a)
			}
			b.WriteString("(?:" + strings.Join(alts, "|") + ")")
			i += end
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return regexp.MustCompile("^$")
	}
	return re
}
