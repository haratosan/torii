# Torii

An extensible AI assistant that connects to Telegram, powered by LLMs (Ollama or OpenRouter). Torii supports tool-calling via external extensions, scheduled tasks, persistent memory, and per-user sessions.

## Features

- **Telegram integration** -- chat with your AI assistant via Telegram
- **Multiple LLM backends** -- Ollama (local) or OpenRouter (cloud)
- **Extension system** -- add custom tools as standalone executables
- **Built-in tools** -- memory, bot profile, shell access, reminders, cron jobs
- **Scheduler** -- run reminders and cron tasks in the background
- **Session management** -- per-user conversation history
- **Onboarding** -- configurable welcome questions for new users

## Requirements

- Go 1.24+
- Telegram Bot Token (from [@BotFather](https://t.me/BotFather))
- Ollama running locally, or an OpenRouter API key

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

See `config.yaml.example` for all available options. Secrets can also be set via environment variables:

| Variable                  | Description            |
|---------------------------|------------------------|
| `TORII_TELEGRAM_TOKEN`    | Telegram bot token     |
| `TORII_OPENROUTER_API_KEY`| OpenRouter API key     |

## Extensions

Extensions are standalone executables that communicate with Torii via stdin/stdout. They live in the `extensions/` directory. Included extensions:

- **torii-echo** -- echoes input back (example extension)
- **torii-time** -- returns the current time
- **torii-web** -- fetches web content

Optional extensions can be installed separately by cloning them into `extensions/`:

- **[torii-transcribe](https://github.com/haratosan/torii-transcribe)** -- transcribes audio using Whisper

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
builtin/         -- built-in tools (memory, shell, remind, cron, ...)
channel/         -- messaging channels (Telegram)
config/          -- YAML config loading
extension/       -- extension registry and executor
extensions/      -- example extension binaries
gateway/         -- message routing between channel and agent
llm/             -- LLM provider abstraction (Ollama, OpenRouter)
scheduler/       -- background task scheduler
session/         -- per-user session/history management
store/           -- SQLite persistence layer
```

## License

MIT
