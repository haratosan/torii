<p align="center">
  <img src="assets/torii_logo.png" alt="Torii Logo" width="400">
</p>

# Torii

An extensible AI assistant that connects to Telegram, powered by LLMs (Ollama or OpenRouter). Torii supports tool-calling via built-in tools, external extensions, and MCP servers. Features include persistent sessions, scheduled tasks, user memory, inline keyboards, and a per-chat knowledge base with RAG.

## Features

- **Telegram integration** -- chat with your AI assistant via Telegram
- **Multiple LLM backends** -- Ollama (local) or OpenRouter (cloud)
- **Extension system** -- add custom tools as standalone executables
- **Built-in tools** -- memory, bot profile, shell access, sandbox (containerized), reminders, cron jobs, inline keyboards, knowledge base, no-reply (silent response suppression)
- **MCP client** -- connect to any MCP server (stdio or SSE) to extend tool capabilities
- **Scheduler** -- run reminders and cron tasks in the background
- **Session persistence** -- per-user conversation history, survives restarts
- **Knowledge base / RAG** -- per-chat semantic document search using Ollama embeddings, with automatic PDF import via vision OCR
- **Bot commands** -- `/new`, `/status`, `/system`, `/help`
- **Onboarding** -- configurable welcome questions for new users

## Requirements

- Go 1.24+
- Telegram Bot Token (from [@BotFather](https://t.me/BotFather))
- Ollama running locally, or an OpenRouter API key
- (Optional) Sandbox: Apple Containers on macOS (`brew install container` + `container system start`) or Docker on Linux
- (Optional) Knowledge base: Ollama with `nomic-embed-text` model (`ollama pull nomic-embed-text`)
- (Optional) PDF import: `poppler` for PDF-to-image conversion (`brew install poppler` on macOS, `apt install poppler-utils` on Linux) and a vision model in Ollama (`ollama pull llava`)

## Setup

1. Copy the example config and edit it:

```sh
cp config.yaml.example config.yaml
```

2. Set your Telegram token and LLM provider in `config.yaml`.

3. Build and run:

```sh
make run
```

## Configuration

See `config.yaml.example` for all available options. All settings can be configured in `config.yaml` or overridden via environment variables:

Extension-specific environment variables (e.g. `TORII_OPENROUTER_API_KEY`, `TORII_IMAGE_MODEL`) can also be set under `extensions.env` in `config.yaml`.

| Variable                   | Description              |
|----------------------------|--------------------------|
| `TORII_TELEGRAM_TOKEN`     | Telegram bot token       |
| `TORII_LLM_PROVIDER`      | `ollama` or `openrouter` |
| `TORII_OLLAMA_HOST`        | Ollama API URL           |
| `TORII_OLLAMA_MODEL`       | Ollama model name        |
| `TORII_OPENROUTER_API_KEY` | OpenRouter API key       |
| `TORII_OPENROUTER_MODEL`   | OpenRouter model name    |
| `TORII_LOG_LEVEL`          | Log level (e.g. `debug`) |

## Install / Uninstall

Install torii system-wide (per-user) with autostart:

```sh
make install
```

This will:

- Build the binary and extensions
- Copy everything to `~/.local/share/torii/`
- Copy `config.yaml.example` to `~/.config/torii/config.yaml` (if not present)
- Create a symlink at `~/.local/bin/torii`
- Set up autostart via **systemd** (Linux) or **launchd** (macOS)

Make sure `~/.local/bin` is in your `PATH`. Edit `~/.config/torii/config.yaml` before starting.

### Service Commands

After installation, manage the service with:

```sh
torii start     # Start the service
torii stop      # Stop the service
torii restart   # Restart the service
torii status    # Show service status
torii logs      # Tail service logs
```

Running `torii` without a command starts the bot directly (foreground).

To remove:

```sh
make uninstall
```

This stops the service, removes the binary and extensions, but preserves your config at `~/.config/torii/`.

## Session Persistence

Conversation history is automatically persisted to SQLite and restored on restart. No configuration needed — sessions survive bot restarts transparently.

- Messages (including tool calls) are stored in the `session_messages` table
- On first message after restart, history is loaded from DB into memory
- `/new` clears both in-memory and persisted history
- History is trimmed to `session.max_history` in both layers

## MCP Client

Torii can connect to [MCP (Model Context Protocol)](https://modelcontextprotocol.io/) servers to extend its tool capabilities. Both stdio (subprocess) and SSE (HTTP) transports are supported.

```yaml
mcp:
  servers:
    - name: "filesystem"
      transport: "stdio"
      command: "npx"
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/dir"]
    - name: "web-search"
      transport: "sse"
      url: "http://localhost:3001/sse"
```

MCP tools are discovered automatically at startup and integrated into the agent's tool system. Tool priority: builtins > extensions > MCP (name collisions are resolved by this order).

## Inline Keyboards

The LLM can present interactive buttons to users via the `send-buttons` built-in tool. When a user clicks a button, the callback data is routed back through the agent as a new message.

This happens automatically when the LLM decides to present choices — no configuration required.

## Knowledge Base / RAG

Per-chat semantic document search using vector embeddings. Documents are chunked, embedded via Ollama, and stored in SQLite for cosine similarity search.

**Requirements:** Ollama with an embedding model pulled (`ollama pull nomic-embed-text`).

```yaml
knowledge:
  enabled: true
  embedding_model: "nomic-embed-text"
  vision_model: "llava"        # for PDF text extraction
  max_pdf_pages: 20            # max pages per PDF
  chunk_size: 500
  chunk_overlap: 50
  top_k: 5
```

The LLM uses the `knowledge` tool automatically with these actions:
- **add** -- store a document (title + content), chunked and embedded
- **search** -- semantic search across stored documents
- **list** -- show all documents in the chat's knowledge base
- **delete** -- remove a document by ID

**PDF import:** Send a PDF file to the bot via Telegram and it will automatically convert pages to images (via `pdftoppm`), extract text using a vision model (e.g. `llava`), and store the content in the knowledge base. Requires `poppler` installed and a vision model pulled in Ollama.

Knowledge bases are scoped per chat (groups share knowledge, private chats are separate).

## Bot Commands

These Telegram commands are handled directly by the bot without going through the LLM:

| Command | Description |
|---------|-------------|
| `/new` | Clear the current session and start fresh |
| `/status` | Show session message count, LLM provider, and model |
| `/system` | Show the current system prompt |
| `/help` | List available commands |

## Extensions

Extensions are standalone executables that communicate with Torii via stdin/stdout. They live in the `extensions/` directory. Included extensions:

- **torii-echo** -- echoes input back (example extension)
- **torii-time** -- returns the current time
- **torii-web** -- fetches web content

Optional extensions can be installed separately by cloning them into `extensions/`:

- **[torii-transcribe](https://github.com/haratosan/torii-transcribe)** -- transcribes audio using Whisper
- **[torii-image](https://github.com/haratosan/torii-image)** -- generates or edits images via AI (OpenRouter)
- **[torii-weather](https://github.com/haratosan/torii-weather)** -- returns current weather and forecast via Open-Meteo
- **[torii-curl](https://github.com/haratosan/torii-curl)** -- makes HTTP requests to URLs and APIs
- **[torii-email](https://github.com/haratosan/torii-email)** -- reads, searches, and drafts emails via IMAP

Build all extensions:

```sh
make extensions
```

Build a release package:

```sh
make release
```

This creates a `release/` directory with the binary, config example, and all extensions.

## Project Structure

```
main.go          -- entrypoint
agent/           -- agentic tool-calling loop
cmd/             -- CLI subcommands (service management)
builtin/         -- built-in tools (memory, shell, remind, cron, buttons, knowledge, ...)
channel/         -- messaging channels (Telegram)
config/          -- YAML config loading
extension/       -- extension registry and executor
extensions/      -- example extension binaries
gateway/         -- message routing between channel and agent
knowledge/       -- RAG: chunking, embedding, vector search
pdf/             -- PDF-to-text via pdftoppm + vision OCR
llm/             -- LLM provider abstraction (Ollama, OpenRouter)
mcp/             -- MCP client (stdio/SSE) and server manager
scheduler/       -- background task scheduler
session/         -- per-user session/history management (DB-backed)
store/           -- SQLite persistence layer
```

## License

MIT
