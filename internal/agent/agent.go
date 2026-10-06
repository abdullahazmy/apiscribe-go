// Package agent runs the streaming tool-use loop against the Claude API.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/abdullahazmy/apiscribe-go/internal/config"
	"github.com/abdullahazmy/apiscribe-go/internal/prompts"
	"github.com/abdullahazmy/apiscribe-go/internal/tools"
	"github.com/abdullahazmy/apiscribe-go/internal/ui"
)

const maxToolRounds = 200

var betas = []anthropic.AnthropicBeta{
	// Re-run a safety-classifier refusal on Anthropic's recommended fallback model.
	anthropic.AnthropicBetaServerSideFallback2026_07_01,
	// Surface Claude's short progress notes between tool calls.
	anthropic.AnthropicBetaThinkingDisplayUpdates2026_08_18,
}

// $ per million tokens: input, output, cache read, cache write.
var prices = map[string][4]float64{
	"claude-opus-5-5":   {4, 20, 0.2, 5},
	"claude-sonnet-5-5": {2, 10, 0.2, 2.5},
	"claude-fable-5-1":  {10, 50, 0.25, 12.5},
	"claude-haiku-4-5":  {1, 5, 0.1, 1.25},
}

// Usage accumulates token counts for the session.
type Usage struct{ Input, Output, CacheRead, CacheWrite int64 }

// Agent is a conversation with an append-only history. History is never
// rewritten, which keeps the prompt cache warm and thinking blocks valid.
type Agent struct {
	cfg      *config.Config
	client   anthropic.Client
	system   string
	tools    []anthropic.BetaToolUnionParam
	messages []anthropic.BetaMessageParam
	Usage    Usage
}

// New creates an agent. Credentials come from the environment (ANTHROPIC_API_KEY).
func New(cfg *config.Config) *Agent {
	return &Agent{
		cfg:    cfg,
		client: anthropic.NewClient(),
		system: prompts.System(cfg),
		tools:  tools.Definitions(),
	}
}

// Reset starts a fresh conversation.
func (a *Agent) Reset() { a.messages = nil }

// CostUSD estimates the session cost, or -1 for models without a known price.
func (a *Agent) CostUSD() float64 {
	p, ok := prices[a.cfg.Model]
	if !ok {
		return -1
	}
	u := a.Usage
	return (float64(u.Input)*p[0] + float64(u.Output)*p[1] + float64(u.CacheRead)*p[2] + float64(u.CacheWrite)*p[3]) / 1e6
}

// streamPrinter writes thinking and text deltas to the terminal.
type streamPrinter struct {
	spinner     *ui.Spinner
	atLineStart bool
	inThinking  bool
}

func (p *streamPrinter) write(s string) {
	if s == "" {
		return
	}
	fmt.Print(s)
	p.atLineStart = strings.HasSuffix(s, "\n")
}

func (p *streamPrinter) handle(ev anthropic.BetaRawMessageStreamEventUnion) {
	switch ev.Type {
	case "content_block_start":
		if ev.ContentBlock.Type == "text" && !p.atLineStart {
			p.write("\n")
		}
	case "content_block_delta":
		switch ev.Delta.Type {
		case "thinking_delta":
			if ev.Delta.Thinking == "" {
				return
			}
			p.spinner.Stop()
			if !p.inThinking {
				if !p.atLineStart {
					p.write("\n")
				}
				p.write(ui.Dim("∴ "))
				p.inThinking = true
			}
			p.write(ui.Dim(ev.Delta.Thinking))
		case "text_delta":
			p.spinner.Stop()
			if p.inThinking {
				p.write("\n\n")
				p.inThinking = false
			}
			p.write(ev.Delta.Text)
		}
	case "content_block_stop":
		if p.inThinking {
			p.write("\n")
			p.inThinking = false
		}
	}
}

// Send adds one user turn and runs tools until Claude is done. Cancelling
// ctx (ctrl+c) interrupts the turn; the history stays a valid prefix.
func (a *Agent) Send(ctx context.Context, content []anthropic.BetaContentBlockParamUnion) error {
	a.messages = append(a.messages, anthropic.NewBetaUserMessage(content...))
	spinner := &ui.Spinner{}
	defer spinner.Stop()
	jsonRetries := 0

	for round := 0; round < maxToolRounds; round++ {
		spinner.Start("Thinking")
		printer := &streamPrinter{spinner: spinner, atLineStart: true}

		stream := a.client.Beta.Messages.NewStreaming(ctx, anthropic.BetaMessageNewParams{
			Model:        anthropic.Model(a.cfg.Model),
			MaxTokens:    64000,
			Betas:        betas,
			Fallbacks:    anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
			Thinking:     anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{Display: anthropic.BetaThinkingConfigAdaptiveDisplayUpdates}},
			OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(a.cfg.Effort)},
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
			System:       []anthropic.BetaTextBlockParam{{Text: a.system}},
			Tools:        a.tools,
			Messages:     a.messages,
		})
		msg := anthropic.BetaMessage{}
		for stream.Next() {
			ev := stream.Current()
			if err := msg.Accumulate(ev); err != nil {
				stream.Close()
				spinner.Stop()
				return err
			}
			printer.handle(ev)
		}
		spinner.Stop()
		if err := stream.Err(); err != nil {
			if ctx.Err() != nil {
				fmt.Println(ui.Yellow("\n⏹  Interrupted."))
				return nil
			}
			return err
		}
		if !printer.atLineStart {
			fmt.Println()
		}
		a.Usage.Input += msg.Usage.InputTokens
		a.Usage.Output += msg.Usage.OutputTokens
		a.Usage.CacheRead += msg.Usage.CacheReadInputTokens
		a.Usage.CacheWrite += msg.Usage.CacheCreationInputTokens

		if msg.StopReason == anthropic.BetaStopReasonRefusal {
			// Discard the partial response; the history stays a valid prefix.
			cat := ""
			if msg.StopDetails.Category != "" {
				cat = fmt.Sprintf(" (%s)", msg.StopDetails.Category)
			}
			ui.Error("Claude declined this request" + cat + ". Try rephrasing it.")
			return nil
		}

		var toolUses []anthropic.BetaContentBlockUnion
		validJSON := true
		for _, b := range msg.Content {
			if b.Type == "tool_use" {
				toolUses = append(toolUses, b)
				if !json.Valid(b.Input) {
					validJSON = false
				}
			}
		}
		// With eager input streaming the server does not validate tool input;
		// re-issue the turn when a tool input is not even valid JSON.
		if !validJSON && msg.StopReason != anthropic.BetaStopReasonMaxTokens {
			if jsonRetries++; jsonRetries > 2 {
				return errors.New("Claude produced invalid tool input JSON three times in a row")
			}
			fmt.Fprintln(os.Stderr, ui.Dim("(tool input was not valid JSON — retrying the turn)"))
			continue
		}
		jsonRetries = 0

		if msg.StopReason == anthropic.BetaStopReasonMaxTokens {
			if len(toolUses) == 0 {
				a.messages = append(a.messages, msg.ToParam())
				ui.Error("Response hit the output limit. Ask Claude to continue.")
				return nil
			}
			// A tool input cut off at max_tokens is incomplete; never run it.
			a.messages = append(a.messages, truncatedToParam(msg))
			var results []anthropic.BetaContentBlockParamUnion
			for _, t := range toolUses {
				results = append(results, anthropic.NewBetaToolResultBlock(t.ID,
					"Output limit reached before this tool input was complete; it was not executed. Split the work into smaller files and retry.", true))
			}
			a.messages = append(a.messages, anthropic.NewBetaUserMessage(results...))
			continue
		}

		a.messages = append(a.messages, msg.ToParam())
		if msg.StopReason == anthropic.BetaStopReasonPauseTurn {
			continue
		}
		if len(toolUses) == 0 {
			return nil
		}

		var results []anthropic.BetaContentBlockParamUnion
		for _, t := range toolUses {
			ui.ToolLine(prettyName(t.Name), tools.Describe(t.Name, t.Input))
			out := tools.Run(a.cfg, t.Name, t.Input)
			ui.ToolResultLine(out.Summary, out.IsError)
			results = append(results, anthropic.NewBetaToolResultBlock(t.ID, out.Content, out.IsError))
		}
		a.messages = append(a.messages, anthropic.NewBetaUserMessage(results...))
		if ctx.Err() != nil {
			fmt.Println(ui.Yellow("⏹  Interrupted."))
			return nil
		}
	}
	ui.Error(fmt.Sprintf("Stopped after %d tool rounds.", maxToolRounds))
	return nil
}

// truncatedToParam converts a max_tokens message whose tool inputs may be
// partial JSON into a param, replacing unparseable inputs with {}.
func truncatedToParam(msg anthropic.BetaMessage) anthropic.BetaMessageParam {
	for i := range msg.Content {
		if msg.Content[i].Type == "tool_use" && !json.Valid(msg.Content[i].Input) {
			msg.Content[i].Input = json.RawMessage("{}")
		}
	}
	return msg.ToParam()
}

func prettyName(name string) string {
	switch name {
	case "list_files":
		return "List"
	case "read_file":
		return "Read"
	case "search":
		return "Search"
	case "write_doc":
		return "Write"
	}
	return name
}

// Text wraps a string as a content block.
func Text(s string) anthropic.BetaContentBlockParamUnion { return anthropic.NewBetaTextBlock(s) }

// ExplainError turns API errors into one readable line.
func ExplainError(err error) string {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 401:
			return "Not authenticated. Set ANTHROPIC_API_KEY (get one at https://console.anthropic.com)."
		case 403:
			return "Permission denied: " + apiErr.Error()
		case 404:
			return "Not found (check the model name): " + apiErr.Error()
		case 429:
			return "Rate limited — wait a moment and try again."
		case 400:
			return "Bad request: " + apiErr.Error()
		}
		return fmt.Sprintf("API error %d: %s", apiErr.StatusCode, apiErr.Error())
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return "Network error: " + err.Error()
	}
	return err.Error()
}
