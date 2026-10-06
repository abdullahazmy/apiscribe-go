// Package repl implements the interactive session and the shared command
// handlers used by the one-shot subcommands.
package repl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/peterh/liner"

	"github.com/abdullahazmy/apiscribe-go/internal/agent"
	"github.com/abdullahazmy/apiscribe-go/internal/config"
	"github.com/abdullahazmy/apiscribe-go/internal/export"
	"github.com/abdullahazmy/apiscribe-go/internal/image"
	"github.com/abdullahazmy/apiscribe-go/internal/prompts"
	"github.com/abdullahazmy/apiscribe-go/internal/ui"
)

var slashCommands = [][2]string{
	{"/scan [focus]", "Scan the backend and write/refresh docs for every endpoint"},
	{"/endpoint <what>", "Document or update a single endpoint, e.g. /endpoint POST /api/orders"},
	{"/image [paths…] [-- note]", "Map app screen(s) to the APIs to call. No path = paste from clipboard"},
	{"/export [md|html|pdf|all]", "Build API_DOCUMENTATION.{md,html,pdf} in <docs>/dist"},
	{"/docs", "List the documentation files"},
	{"/effort [level]", "Show or set reasoning effort (" + strings.Join(config.Efforts, ", ") + ")"},
	{"/cost", "Show token usage and estimated cost for this session"},
	{"/clear", "Start a fresh conversation (docs on disk are kept)"},
	{"/help", "Show this help"},
	{"/exit", "Quit"},
}

func printHelp() {
	fmt.Println(ui.Bold("\nCommands"))
	for _, c := range slashCommands {
		fmt.Printf("  %s %s\n", ui.Accent(fmt.Sprintf("%-28s", c[0])), c[1])
	}
	fmt.Println(ui.Bold("\nTips"))
	fmt.Println(ui.Dim("  • Anything else you type is a chat message — ask questions or request changes to the docs."))
	fmt.Println(ui.Dim("  • Drag an image file into the terminal (or paste its path) to map that screen."))
	fmt.Println(ui.Dim("  • ctrl+c interrupts Claude; ctrl+c at an empty prompt (or ctrl+d) exits."))
	fmt.Println()
}

// ParseFormats parses "md,html" / "all" into export formats.
func ParseFormats(arg string) ([]string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" || arg == "all" {
		return export.Formats, nil
	}
	var out, bad []string
	for _, f := range regexp.MustCompile(`[,\s]+`).Split(arg, -1) {
		if f == "" {
			continue
		}
		if slices.Contains(export.Formats, f) {
			out = append(out, f)
		} else {
			bad = append(bad, f)
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("unknown format(s): %s. Use md, html, pdf or all", strings.Join(bad, ", "))
	}
	return out, nil
}

// RunExport renders the docs and prints the result.
func RunExport(cfg *config.Config, formats []string) error {
	fmt.Println(ui.Dim("Exporting " + strings.Join(formats, ", ") + "…"))
	res, err := export.Run(cfg, formats)
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	for _, f := range res.Files {
		if rel, err := filepath.Rel(cwd, f); err == nil && !strings.HasPrefix(rel, "..") {
			f = rel
		}
		ui.OK(f)
	}
	for _, w := range res.Warnings {
		ui.Error(w)
	}
	return nil
}

var noteSep = regexp.MustCompile(`(^|\s)--(\s|$)`)

// CollectImages splits "a.png b.png -- this is checkout" into images and a note.
// With no paths it reads the clipboard.
func CollectImages(args string) ([]image.Loaded, string, error) {
	pathPart, note := args, ""
	if loc := noteSep.FindStringIndex(args); loc != nil {
		pathPart, note = args[:loc[0]], strings.TrimSpace(args[loc[1]:])
	}
	paths := image.ParsePathArgs(pathPart)
	if len(paths) == 0 {
		img, err := image.LoadClipboard()
		if err != nil {
			return nil, "", err
		}
		return []image.Loaded{img}, note, nil
	}
	var imgs []image.Loaded
	for _, p := range paths {
		img, err := image.LoadFile(p)
		if err != nil {
			return nil, "", err
		}
		imgs = append(imgs, img)
	}
	return imgs, note, nil
}

// SendImages attaches screenshots and asks Claude to map them to APIs.
func SendImages(ctx context.Context, a *agent.Agent, imgs []image.Loaded, note string) error {
	names := make([]string, len(imgs))
	blocks := make([]anthropic.BetaContentBlockParamUnion, 0, len(imgs)+1)
	for i, img := range imgs {
		names[i] = img.Name
		blocks = append(blocks, img.Block)
	}
	fmt.Println(ui.Dim("Attached " + strings.Join(names, ", ")))
	blocks = append(blocks, agent.Text(prompts.Image(names, note)))
	return a.Send(ctx, blocks)
}

// PrintCost prints token usage and estimated cost.
func PrintCost(a *agent.Agent) {
	u := a.Usage
	line := fmt.Sprintf("%s %d  %s %d  %s %d  %s %d", ui.Dim("input"), u.Input, ui.Dim("output"), u.Output,
		ui.Dim("cache read"), u.CacheRead, ui.Dim("cache write"), u.CacheWrite)
	if c := a.CostUSD(); c >= 0 {
		line += fmt.Sprintf("  %s %s", ui.Dim("≈"), ui.Bold(fmt.Sprintf("$%.4f", c)))
	}
	fmt.Println(line)
}

func listDocs(cfg *config.Config) {
	var out []string
	_ = filepath.WalkDir(cfg.DocsDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(cfg.DocsDir, p)
			out = append(out, "  "+filepath.ToSlash(rel))
		}
		return nil
	})
	if len(out) == 0 {
		fmt.Println(ui.Dim("No docs yet. Run /scan."))
		return
	}
	sort.Strings(out)
	fmt.Println(strings.Join(out, "\n"))
}

// Interruptible runs fn with a context that ctrl+c cancels.
func Interruptible(fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	var once sync.Once
	go func() {
		if _, ok := <-sig; ok {
			once.Do(cancel)
		}
	}()
	return fn(ctx)
}

type session struct {
	cfg   *config.Config
	agent *agent.Agent
}

var errExit = errors.New("exit")

func (s *session) handle(input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}

	// A bare dragged-in image path counts as /image.
	if paths := image.ParsePathArgs(input); len(paths) > 0 && allImages(paths) {
		imgs, _, err := CollectImages(input)
		if err != nil {
			return err
		}
		return Interruptible(func(ctx context.Context) error { return SendImages(ctx, s.agent, imgs, "") })
	}

	if !strings.HasPrefix(input, "/") {
		return Interruptible(func(ctx context.Context) error {
			return s.agent.Send(ctx, []anthropic.BetaContentBlockParamUnion{agent.Text(input)})
		})
	}

	cmd, args, _ := strings.Cut(input, " ")
	args = strings.TrimSpace(args)
	send := func(prompt string) error {
		return Interruptible(func(ctx context.Context) error {
			return s.agent.Send(ctx, []anthropic.BetaContentBlockParamUnion{agent.Text(prompt)})
		})
	}
	switch cmd {
	case "/exit", "/quit":
		return errExit
	case "/help":
		printHelp()
	case "/clear":
		s.agent.Reset()
		ui.OK("Conversation cleared.")
	case "/cost":
		PrintCost(s.agent)
	case "/docs":
		listDocs(s.cfg)
	case "/effort":
		if args == "" {
			fmt.Println("effort: " + s.cfg.Effort)
		} else if !slices.Contains(config.Efforts, args) {
			ui.Error("Use one of: " + strings.Join(config.Efforts, ", "))
		} else {
			s.cfg.Effort = args
			ui.OK("effort set to " + args)
		}
	case "/scan":
		return send(prompts.Scan(args))
	case "/endpoint":
		if args == "" {
			ui.Error("Usage: /endpoint POST /api/orders")
			return nil
		}
		return send(prompts.Endpoint(args))
	case "/image":
		imgs, note, err := CollectImages(args)
		if err != nil {
			return err
		}
		return Interruptible(func(ctx context.Context) error { return SendImages(ctx, s.agent, imgs, note) })
	case "/export":
		formats, err := ParseFormats(args)
		if err != nil {
			return err
		}
		return RunExport(s.cfg, formats)
	default:
		ui.Error("Unknown command " + cmd + ". Type /help.")
	}
	return nil
}

func allImages(paths []string) bool {
	for _, p := range paths {
		if !image.IsImagePath(p) {
			return false
		}
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			return false
		}
	}
	return true
}

// Run starts the interactive session.
func Run(cfg *config.Config, version string) error {
	s := &session{cfg: cfg, agent: agent.New(cfg)}
	ui.PrintBanner(ui.BannerInfo{Version: version, Model: cfg.Model, Effort: cfg.Effort, ProjectRoot: cfg.ProjectRoot, DocsDir: cfg.DocsDir})

	line := liner.NewLiner()
	defer line.Close()
	line.SetCtrlCAborts(true)
	line.SetTabCompletionStyle(liner.TabPrints)
	line.SetCompleter(func(l string) []string {
		if !strings.HasPrefix(l, "/") || strings.Contains(l, " ") {
			return nil
		}
		var out []string
		for _, c := range slashCommands {
			if name := strings.Fields(c[0])[0]; strings.HasPrefix(name, l) {
				out = append(out, name)
			}
		}
		return out
	})

	for {
		input, err := line.Prompt("› ")
		if errors.Is(err, liner.ErrPromptAborted) || errors.Is(err, io.EOF) {
			fmt.Println()
			break
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(input) != "" {
			line.AppendHistory(input)
		}
		if err := s.handle(input); err != nil {
			if errors.Is(err, errExit) {
				break
			}
			ui.Error(agent.ExplainError(err))
		}
		fmt.Println()
	}
	PrintCost(s.agent)
	return nil
}
