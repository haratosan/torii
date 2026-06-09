package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/haratosan/torii/config"
)

// Run is the entry point invoked from main.go for `torii tui`. It loads
// the daemon config, ensures a local API token exists, opens the history
// DB and starts the Bubbletea program.
//
// Any error before the TUI starts is printed to stderr and surfaces with
// a non-zero exit code via os.Exit — we don't want to swallow a missing
// daemon or a corrupt config into the alt-screen.
func Run(_ []string) {
	// Reuse the daemon's config-search order so the TUI works whether the
	// user ran make install (config at ~/.config/torii) or develops locally
	// (config in CWD).
	configPath := "config.yaml"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if home, err := os.UserHomeDir(); err == nil {
			candidate := home + "/.config/torii/config.yaml"
			if _, err := os.Stat(candidate); err == nil {
				configPath = candidate
			}
		}
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tui: config error: %v\n", err)
		os.Exit(1)
	}

	tc, err := loadTUIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tui: read tui.yaml: %v\n", err)
		os.Exit(1)
	}
	if tc == nil {
		fmt.Fprintln(os.Stderr, "tui: erster Start – lege lokalen API-Token an…")
		tc, err = bootstrap(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tui: bootstrap failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "Hinweis: torii muss installiert sein und seine DB unter "+cfg.Scheduler.DBPath+" liegen.")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "tui: ok, token gespeichert in ~/.config/torii/tui.yaml\n")
	}

	client := newClient(tc)

	// Verify the daemon is up before opening the alt-screen. A daemon-down
	// TUI is useless and a clear stderr message is far better UX than an
	// empty box that errors on the first keystroke.
	if err := client.ping(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "tui: daemon nicht erreichbar unter %s: %v\n", tc.BaseURL, err)
		fmt.Fprintln(os.Stderr, "Hinweis: starte torii mit `torii start` (oder im Foreground mit `torii`).")
		os.Exit(1)
	}

	histPath, err := defaultHistoryPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tui: history path: %v\n", err)
		os.Exit(1)
	}
	hist, err := newHistoryStore(histPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tui: open history db: %v\n", err)
		os.Exit(1)
	}
	defer hist.Close()

	convID, err := hist.newConversation()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tui: new conversation: %v\n", err)
		os.Exit(1)
	}

	m := newModel(client, hist, convID)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		// Bubbletea returns ErrProgramKilled on a normal Ctrl+C path; silence
		// that one specifically and surface everything else with a clear
		// stderr line.
		fmt.Fprintf(os.Stderr, "tui: %v\n", err)
		os.Exit(1)
	}
	_ = slog.Default() // keep import live for future debug logging
}
