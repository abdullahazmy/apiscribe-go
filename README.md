# apiscribe (Go)

An AI CLI built on Claude that writes API documentation for **frontend and mobile developers**, shipped as a single binary. Run it in your backend repo and it reads the code (routes, controllers, DTOs, validators, middleware, error handlers), then writes docs that show every endpoint's expected request, its responses, and its errors. Give it a screenshot of an app screen and it tells you which APIs that screen should call.

This is the Go version of [apiscribe](https://github.com/abdullahazmy/apiscribe). There is also a [Rust version](https://github.com/abdullahazmy/apiscribe-rs). All three share the same prompts, commands, and output.

```
› /scan
∴ Express app; routes mounted in src/app.js under /api/v1…
● Search("router\.(get|post|put|patch|delete)")
  ⎿  14 matches
● Read(src/routes/cart.js)
  ⎿  31 lines
● Write(endpoints/cart.md)
  ⎿  created · 212 lines
Documented 14 endpoints in 5 files. ⚠️ POST /auth/login has no rate limiting.

› /image ~/Downloads/checkout.png -- this is the checkout screen
› /export
✓ api-docs/dist/API_DOCUMENTATION.md
✓ api-docs/dist/API_DOCUMENTATION.html
✓ api-docs/dist/API_DOCUMENTATION.pdf
```

## Install

Download a binary for Linux, macOS, or Windows from [Releases](https://github.com/abdullahazmy/apiscribe-go/releases), or build it with Go 1.26+:

```bash
go install github.com/abdullahazmy/apiscribe-go@latest   # installs as apiscribe-go
# or
git clone https://github.com/abdullahazmy/apiscribe-go && cd apiscribe-go && go build -o apiscribe .
```

Set `ANTHROPIC_API_KEY` before running it.

PDF export uses an installed Chrome, Chromium, or Edge. It finds the common install locations on its own; to point it at a different binary, set `APISCRIBE_CHROME=/path/to/chrome`. Without a browser, the Markdown and HTML exports still work.

## Usage

### Interactive

```bash
cd my-backend
apiscribe
```

| Command | What it does |
|---|---|
| `/scan [focus]` | Finds every endpoint and writes `README.md` plus `endpoints/<resource>.md`. Run it again to update the docs. |
| `/endpoint <what>` | Documents or updates one endpoint, e.g. `/endpoint POST /api/orders` |
| `/image [paths…] [-- note]` | Maps app screens to the APIs they should call. With no path it reads the image from the clipboard. You can also drag an image file into the terminal. |
| `/export [md\|html\|pdf\|all]` | Builds single-file `API_DOCUMENTATION.md`, `.html`, and `.pdf` files in `api-docs/dist/` |
| `/docs` | Lists the generated files |
| `/effort [level]` | Sets reasoning effort: `low`, `medium`, `high` (default), `xhigh`, or `max` |
| `/cost` | Shows token usage and estimated cost |
| `/clear` | Starts a new conversation. Docs on disk are kept. |

Anything else you type goes to Claude as a chat message. Press ctrl+c while Claude is working to interrupt it. Press ctrl+c at an empty prompt, or ctrl+d, to exit. Tab completes slash commands.

### One-shot (scripts and CI)

```bash
apiscribe generate                      # scan, then export md + html + pdf
apiscribe generate "only the payments module" --format html
apiscribe image login.png home.png -n "customer mobile app"
apiscribe endpoint POST /api/v1/cart/items
apiscribe export --format pdf           # render only, no AI calls
apiscribe -C ../other-service -o docs/api generate
```

Global flags: `-C/--project`, `-o/--docs-dir` (default `api-docs`), `-m/--model` (default `claude-opus-5-5`), and `-e/--effort`. You can also set them through the environment variables `APISCRIBE_MODEL`, `APISCRIBE_EFFORT`, and `APISCRIBE_DOCS_DIR`.

## What the docs contain

For each endpoint:

- auth requirements
- headers
- tables of path, query, and body parameters, with types, constraints, and defaults
- a curl example
- every response status, each with a realistic JSON example that matches the real serializer
- TypeScript types
- client notes covering pagination, retries, rate limits, and side effects

Each screen mapping (`screens/<name>.md`) lists:

- which calls to make when the screen loads, and their order
- which endpoint each UI action calls
- which response field fills each UI element
- loading, empty, and error states
- **missing APIs**: things the screen needs that the backend doesn't provide yet

## Layout

| Path | Purpose |
|---|---|
| `main.go` | The CLI (cobra) |
| `internal/agent` | Streaming tool-use loop on the official [Go SDK](https://github.com/anthropics/anthropic-sdk-go). It uses Claude Opus 5.5 with adaptive thinking, shows progress notes, has prompt caching on, and enables server-side refusal fallback. The history is append-only. |
| `internal/tools` | `list_files`, `read_file`, and `search` (ripgrep, with a pure-Go fallback) can only read inside the project. `write_doc` can only write inside the docs directory. |
| `internal/prompts/text` | The system prompt and task prompts, as plain Markdown |
| `internal/image` | Loads images from a file path or the clipboard (Wayland, X11, macOS, Windows) |
| `internal/export` | goldmark + chroma for HTML, chromedp for PDF; the CSS and JS are embedded in the binary |
| `internal/repl` | The interactive session (liner) |

Releases are built by GoReleaser whenever a `v*` tag is pushed.
