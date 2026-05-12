<p align="center">
  <img src="assets/torii_logo.png" alt="Torii Logo" width="400">
</p>

# Torii

An extensible AI assistant that connects to Telegram, powered by a local Ollama LLM. Torii supports tool-calling via built-in tools, external extensions, and MCP servers. Features include persistent sessions, scheduled tasks, user memory, inline keyboards, and a per-chat knowledge base with RAG.

## Features

- **Telegram integration** -- chat with your AI assistant via Telegram
- **Local LLM** -- powered by Ollama
- **Extension system** -- add custom tools as standalone executables
- **Built-in tools** -- memory (line-based, append/replace/remove), skills (durable playbooks), bot profile, shell access, sandbox (containerized), reminders, cron jobs, inline keyboards, knowledge base, no-reply (silent response suppression)
- **MCP client** -- connect to any MCP server (stdio or SSE) to extend tool capabilities
- **Scheduler** -- run reminders and cron tasks in the background
- **Session persistence** -- per-user conversation history, survives restarts
- **Knowledge base / RAG** -- per-chat semantic document search using Ollama embeddings, with automatic PDF import via vision OCR
- **Auto self-evolution** -- daily background reflection job per user that turns recurring tool-call patterns into durable skills (read-only memory, fully silent)
- **OpenAI-compatible API** -- `/v1/chat/completions` and `/v1/models` so OpenWebUI, curl, or the OpenAI SDK can drive the same agent (with tools, memory, skills) over Tailscale
- **Bot commands** -- `/new`, `/status`, `/system`, `/help`
- **Onboarding** -- configurable welcome questions for new users

## Requirements

- Go 1.24+
- Telegram Bot Token (from [@BotFather](https://t.me/BotFather))
- Ollama running locally
- (Optional) Sandbox: Apple Containers on macOS (`brew install container` + `container system start`) or Docker on Linux
- (Optional) Knowledge base: Ollama with `nomic-embed-text` model (`ollama pull nomic-embed-text`)
- (Optional) PDF import: `poppler` for PDF-to-image conversion (`brew install poppler` on macOS, `apt install poppler-utils` on Linux) and a vision model in Ollama (`ollama pull llava`)

## Setup

1. Copy the example config and edit it:

```sh
cp config.yaml.example config.yaml
```

2. Set your Telegram token and Ollama settings in `config.yaml`.

3. Build and run:

```sh
make run
```

## Configuration

See `config.yaml.example` for all available options. All settings can be configured in `config.yaml` or overridden via environment variables:

Extension-specific environment variables (e.g. `TORII_OPENROUTER_API_KEY`, `TORII_IMAGE_MODEL`) can also be set under `extensions.env` in `config.yaml` for extensions that need them.

| Variable               | Description              |
|------------------------|--------------------------|
| `TORII_TELEGRAM_TOKEN` | Telegram bot token       |
| `TORII_OLLAMA_HOST`    | Ollama API URL           |
| `TORII_OLLAMA_MODEL`   | Ollama model name        |
| `TORII_LOG_LEVEL`      | Log level (e.g. `debug`) |

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

## Memory & Skills

Two complementary persistence layers that survive `/new` and Ollama model switches without bloating the system prompt.

**Memory** — user-scoped, line-based notes (`builtin/memory.go`):
- Actions: `add`, `replace` (substring match on `needle`), `remove` (substring), `list`, `get`, `delete`, and `set` (destructive — kept for backward compat).
- Bounded by `memory.max_chars` (default 1500). When full, `add` is rejected with a consolidation hint instead of silently overwriting.
- Auto-injected into the system prompt every turn as a bullet list.

**Skills** — durable playbooks the LLM can write to itself (`builtin/skills.go`, `agent_skills` table):
- Actions: `add`, `update`, `remove`, `list`, `get`. Default scope is `user:<id>`; pass `global=true` for cross-user skills.
- Auto-injected into the system prompt up to `skills.max_chars_in_prompt` (default 4000). Surplus skills are listed by id+title and the LLM fetches on demand via `skills get <id>`.
- Survives `/new` and Ollama model switches (it's just text in the prompt).

```yaml
memory:
  max_chars: 1500
skills:
  enabled: true
  max_chars_in_prompt: 4000
```

## Auto Self-Evolution

A daily background job that scans each active user's recent tool-call patterns and turns them into skills automatically — no buttons, no approval, fully silent. Inspired by [Hermes Agent's self-evolution](https://github.com/NousResearch/hermes-agent-self-evolution) but radically simplified: no eval set, no genetic search; just a well-prompted reflection LLM with hard code-level safety rails.

How it works:
- One `system_evolve` task per active user, scheduled daily at 04:30 (configurable).
- Each run builds a per-user trace summary (last 7 days: tool-call counts, failure rates, 3-5 representative interaction patterns) and feeds it back to the agent in a special evolution prompt.
- The agent calls `skills add` / `skills update` based on what it sees and ends with `no-reply`.
- Output is fully suppressed at the scheduler level — even if the LLM produces text, it never reaches Telegram.

Safety rails (enforced in `extension/executor.go`, not just by the prompt):
- Max 3 `skills add` and 1 `skills update` per run; `skills remove` forbidden.
- Memory tool is **read-only** during the run (any `add`/`replace`/`remove`/`set`/`delete` is rejected).
- Rate-limited to one successful run per ~23 hours per user.
- Every run is audited in the `evolution_runs` SQLite table (status, summary JSON, leaked-text snippet if any).

Inspect what the bot has been learning:
```sh
sqlite3 ~/.local/share/torii/torii.db \
  "SELECT id, user_id, started_at, status, summary FROM evolution_runs ORDER BY id DESC LIMIT 10;"
```

```yaml
skills:
  auto_evolve: true                # daily background reflection (default on)
  evolve_schedule: "30 4 * * *"    # cron expression
```

## OpenAI-Compatible API

Torii can expose its agent (with tools, memory, skills, and knowledge) as an OpenAI-compatible HTTP server, so any client that speaks `/v1/chat/completions` (OpenWebUI, curl, the official OpenAI SDK, …) can talk to it. Tools, memory consolidation, and skill injection happen **inside** torii — clients see only the final assistant message.

**Architecture choices:**
- Bound to `127.0.0.1:8088` by default. Tailscale Serve (or Funnel) handles TLS + reach. No TLS in torii itself.
- Stateless: each request carries the full conversation. Nothing persists to `session_messages`.
- Skills, memory, and user notes still persist (per-user) and are auto-injected into the system prompt — same as Telegram.
- Per-user bearer-token auth + per-user tool allowlist (default deny).

```yaml
api:
  enabled: true
  listen: "127.0.0.1:8088"
  model_label: "torii"

telegram:
  admin_user_id: "523111104"   # required to use the api-admin builtin
```

### Managing API users (via Telegram, as admin)

The Telegram admin uses the `api-admin` builtin to manage everything in natural language:

> "Erstelle einen API-User namens 'webui' und gib ihm Zugriff auf memory, skills, knowledge und web_search. Link ihn auf meinen Telegram-Account."

The bot will run `api-admin create`, `api-admin grant ...`, `api-admin link ...` and surface the bearer token **once** in chat (store it immediately — it cannot be retrieved later). Other admin actions: `list`, `disable`, `enable`, `rotate`, `revoke`, `delete`.

If an API user is **linked** to a Telegram user, they share memory and skills — the agent treats them as the same person. **Unlinked** API users get an isolated `api:<id>` scope.

### Tailscale setup

Bind locally, let Tailscale handle TLS:

```sh
# tailnet-only:
tailscale serve --bg --https=443 http://127.0.0.1:8088
# or public via Funnel:
tailscale funnel --bg --https=443 http://127.0.0.1:8088
```

Endpoint becomes `https://<machine>.<tailnet>.ts.net`.

### Curl smoke test

```sh
curl -s https://<machine>.<tailnet>.ts.net/v1/chat/completions \
  -H "Authorization: Bearer torii_<token>" \
  -H "Content-Type: application/json" \
  -d '{"model":"torii","messages":[{"role":"user","content":"What do you remember about me?"}],"stream":false}'
```

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
| `/status` | Show session message count and model |
| `/system` | Show the current system prompt |
| `/help` | List available commands |

## Extensions

Extensions are standalone executables that communicate with Torii via stdin/stdout. They live in the `extensions/` directory. Included extensions:

- **torii-echo** -- echoes input back (example extension)
- **torii-time** -- returns the current time
- **torii-web** -- fetches a URL and returns readable plaintext (HTML stripped)
- **torii-search** -- web search via [Tavily](https://tavily.com) (free 1k req/month, requires `TAVILY_API_KEY`)

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
api/             -- OpenAI-compatible HTTP server (Bearer auth, per-user tool allowlists)
cmd/             -- CLI subcommands (service management)
builtin/         -- built-in tools (memory, skills, shell, remind, cron, buttons, knowledge, api-admin, ...)
channel/         -- messaging channels (Telegram)
config/          -- YAML config loading
extension/       -- extension registry and executor
extensions/      -- example extension binaries
gateway/         -- message routing between channel and agent
knowledge/       -- RAG: chunking, embedding, vector search
pdf/             -- PDF-to-text via pdftoppm + vision OCR
llm/             -- Ollama provider
mcp/             -- MCP client (stdio/SSE) and server manager
scheduler/       -- background task scheduler (incl. auto self-evolution loop)
session/         -- per-user session/history management (DB-backed)
store/           -- SQLite persistence layer
```

## License

MIT
