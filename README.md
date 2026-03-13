<p align="center">
  <img src="assets/torii_logo.png" alt="Torii Logo" width="400">
</p>

# Torii

An extensible AI assistant that connects to Telegram, powered by LLMs (Ollama or OpenRouter). Torii supports tool-calling via external extensions, scheduled tasks, persistent memory, and per-user sessions.

## Features

- **Telegram integration** -- chat with your AI assistant via Telegram
- **Multiple LLM backends** -- Ollama (local) or OpenRouter (cloud)
- **Extension system** -- add custom tools as standalone executables
- **Built-in tools** -- memory, bot profile, shell access, sandbox (containerized), reminders, cron jobs, no-reply (silent response suppression)
- **Scheduler** -- run reminders and cron tasks in the background
- **Session management** -- per-user conversation history
- **Bot commands** -- `/new`, `/status`, `/system`, `/help`
- **Onboarding** -- configurable welcome questions for new users

## Requirements

- Go 1.24+
- Telegram Bot Token (from [@BotFather](https://t.me/BotFather))
- Ollama running locally, or an OpenRouter API key
- (Optional) Sandbox: Apple Containers on macOS (`brew install container` + `container system start`) or Docker on Linux

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
