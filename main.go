// Command apiscribe is an AI CLI built on Claude that documents backend
// APIs for frontend and mobile developers.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/spf13/cobra"

	"github.com/abdullahazmy/apiscribe-go/internal/agent"
	"github.com/abdullahazmy/apiscribe-go/internal/config"
	"github.com/abdullahazmy/apiscribe-go/internal/image"
	"github.com/abdullahazmy/apiscribe-go/internal/prompts"
	"github.com/abdullahazmy/apiscribe-go/internal/repl"
	"github.com/abdullahazmy/apiscribe-go/internal/ui"
)

var version = "dev" // set by -ldflags at release time

func main() {
	var opts config.Options
	resolve := func() *config.Config {
		cfg, err := config.Resolve(opts)
		if err != nil {
			ui.Error(err.Error())
			os.Exit(2)
		}
		return cfg
	}

	// oneShot runs a single agent task, prints the cost, and sets the exit code.
	oneShot := func(fn func(ctx context.Context, cfg *config.Config, a *agent.Agent) error) {
		cfg := resolve()
		a := agent.New(cfg)
		err := repl.Interruptible(func(ctx context.Context) error { return fn(ctx, cfg, a) })
		if err != nil {
			ui.Error(agent.ExplainError(err))
		}
		repl.PrintCost(a)
		if err != nil {
			os.Exit(1)
		}
	}

	root := &cobra.Command{
		Use:           "apiscribe",
		Short:         "AI CLI built on Claude that writes API documentation for frontend & mobile developers",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return repl.Run(resolve())
		},
	}
	pf := root.PersistentFlags()
	pf.StringVarP(&opts.Project, "project", "C", "", "backend project root (default: current directory)")
	pf.StringVarP(&opts.DocsDir, "docs-dir", "o", "", "docs output directory, relative to the project (default: api-docs)")
	pf.StringVarP(&opts.Model, "model", "m", "", "Claude model (default: claude-opus-5-5, or $APISCRIBE_MODEL)")
	pf.StringVarP(&opts.Effort, "effort", "e", "", "reasoning effort: "+strings.Join(config.Efforts, ", ")+" (default: high)")

	var noExport bool
	var genFormat string
	generate := &cobra.Command{
		Use:     "generate [focus...]",
		Aliases: []string{"scan"},
		Short:   "Scan the backend, write docs, then export md/html/pdf",
		Run: func(cmd *cobra.Command, args []string) {
			formats, err := repl.ParseFormats(genFormat)
			if err != nil {
				ui.Error(err.Error())
				os.Exit(2)
			}
			oneShot(func(ctx context.Context, cfg *config.Config, a *agent.Agent) error {
				if err := a.Send(ctx, []anthropic.BetaContentBlockParamUnion{agent.Text(prompts.Scan(strings.Join(args, " ")))}); err != nil {
					return err
				}
				if noExport || ctx.Err() != nil {
					return nil
				}
				return repl.RunExport(cfg, formats)
			})
		},
	}
	generate.Flags().BoolVar(&noExport, "no-export", false, "skip the md/html/pdf export step")
	generate.Flags().StringVarP(&genFormat, "format", "f", "all", "export formats: md,html,pdf or all")

	var note string
	imageCmd := &cobra.Command{
		Use:   "image [images...]",
		Short: "Map app screen image(s) to the APIs each screen should call (no args = clipboard)",
		Run: func(cmd *cobra.Command, args []string) {
			var imgs []image.Loaded
			if len(args) == 0 {
				img, err := image.LoadClipboard()
				if err != nil {
					ui.Error(err.Error())
					os.Exit(1)
				}
				imgs = append(imgs, img)
			}
			for _, p := range args {
				img, err := image.LoadFile(p)
				if err != nil {
					ui.Error(err.Error())
					os.Exit(1)
				}
				imgs = append(imgs, img)
			}
			oneShot(func(ctx context.Context, _ *config.Config, a *agent.Agent) error {
				return repl.SendImages(ctx, a, imgs, note)
			})
		},
	}
	imageCmd.Flags().StringVarP(&note, "note", "n", "", "extra context, e.g. \"this is the checkout screen\"")

	endpoint := &cobra.Command{
		Use:   "endpoint <what...>",
		Short: "Document or update a single endpoint, e.g. apiscribe endpoint POST /api/orders",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			oneShot(func(ctx context.Context, _ *config.Config, a *agent.Agent) error {
				return a.Send(ctx, []anthropic.BetaContentBlockParamUnion{agent.Text(prompts.Endpoint(strings.Join(args, " ")))})
			})
		},
	}

	var expFormat string
	exportCmd := &cobra.Command{
		Use:   "export",
		Short: "Render existing docs to API_DOCUMENTATION.{md,html,pdf} (no AI calls)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			formats, err := repl.ParseFormats(expFormat)
			if err != nil {
				return err
			}
			return repl.RunExport(resolve(), formats)
		},
	}
	exportCmd.Flags().StringVarP(&expFormat, "format", "f", "all", "md,html,pdf or all")

	root.AddCommand(generate, imageCmd, endpoint, exportCmd)
	if err := root.Execute(); err != nil {
		ui.Error(err.Error())
		fmt.Fprintln(os.Stderr, ui.Dim("Run 'apiscribe --help' for usage."))
		os.Exit(1)
	}
}
