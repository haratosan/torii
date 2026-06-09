package tui

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/haratosan/torii/api"
	"github.com/haratosan/torii/config"
	"github.com/haratosan/torii/store"
)

// resolveDaemonDBPath turns config.Scheduler.DBPath into the actual file the
// installed daemon uses. The launchd plist + systemd unit both set
// WorkingDirectory to ~/.local/share/torii, so a relative path in
// config.yaml (the default "torii.db") resolves there — not in the TUI's
// CWD. We replicate that resolution explicitly so the bootstrap writes to
// the same SQLite the daemon reads from.
func resolveDaemonDBPath(raw string) (string, error) {
	if filepath.IsAbs(raw) {
		return raw, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "torii", raw), nil
}

// tuiUserName is the well-known label for the locally-bootstrapped API user.
// We never collide with admin-created users (api-admin reserves it
// implicitly by convention, not by enforcement).
const tuiUserName = "tui-local"

// bootstrap creates the local API user if it doesn't exist yet, grants it
// the full set of tools the daemon currently exposes, persists the token to
// tui.yaml and returns the resulting config. Idempotent: re-running picks up
// the existing user without rotating its token.
//
// It opens the daemon's SQLite directly. SQLite WAL (used by store.New)
// allows concurrent readers + one writer, so this is safe even while the
// daemon is running — the bootstrap path inserts a row and closes, holding
// the writer lock for milliseconds.
func bootstrap(cfg *config.Config) (*tuiConfig, error) {
	dbPath, err := resolveDaemonDBPath(cfg.Scheduler.DBPath)
	if err != nil {
		return nil, fmt.Errorf("resolve db path: %w", err)
	}
	db, err := store.New(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open torii db at %s: %w", dbPath, err)
	}
	defer db.Close()

	existing, err := db.GetAPIUserByName(tuiUserName)
	if err != nil {
		return nil, fmt.Errorf("lookup %s: %w", tuiUserName, err)
	}
	var token string
	var userID int64
	var alreadyLinked string
	if existing != nil {
		token = existing.BearerToken
		userID = existing.ID
		alreadyLinked = existing.LinkedTelegramUserID
	} else {
		token = api.NewBearerToken()
		u, err := db.CreateAPIUser(tuiUserName, token)
		if err != nil {
			return nil, fmt.Errorf("create api user: %w", err)
		}
		userID = u.ID
	}

	// Link to the admin Telegram user so the TUI shares memory, skills, and
	// notes with the user the bot already knows about. Without the link the
	// TUI would talk to a fresh api:<id> namespace and feel like a stranger.
	// Idempotent: skip if already linked or admin isn't configured.
	if alreadyLinked == "" && cfg.Telegram.AdminUserID != "" {
		if err := db.UpdateAPIUserLinkedTelegram(userID, cfg.Telegram.AdminUserID); err != nil {
			return nil, fmt.Errorf("link to telegram admin: %w", err)
		}
	}

	// Grant access to every tool the daemon currently knows about. We can't
	// see runtime-registered tools (registry isn't wired in this process), so
	// we enumerate the well-known names. Anything missing from the list can
	// still be granted via api-admin after the fact.
	for _, name := range bootstrapToolGrants {
		if err := db.GrantAPITool(userID, name); err != nil {
			return nil, fmt.Errorf("grant %s: %w", name, err)
		}
	}

	listen := cfg.API.Listen
	if listen == "" {
		listen = "127.0.0.1:8088"
	}
	model := cfg.API.ModelLabel
	if model == "" {
		model = "torii"
	}
	tc := &tuiConfig{
		Token:   token,
		BaseURL: "http://" + listen,
		Model:   model,
	}
	if err := saveTUIConfig(tc); err != nil {
		return nil, fmt.Errorf("save tui.yaml: %w", err)
	}
	return tc, nil
}

// bootstrapToolGrants is the allowlist seeded on first run. Names mirror
// the literal Name fields of the builtin Def structs and the extension
// manifest.json files. Granting a tool that isn't actually registered is a
// harmless no-op (the policy check matches on name; missing tools just
// stay unreachable), so it's fine to list optional/feature-gated tools too.
// Extra tools added later can be granted with `api-admin grant tui-local <name>`.
var bootstrapToolGrants = []string{
	// builtins
	"memory", "bot-profile", "shell", "sandbox",
	"remind", "cron", "send-buttons", "knowledge",
	"no-reply", "api-admin", "skills", "mqtt_trigger",
	// in-tree extensions (torii/extensions/)
	"echo", "time", "web-fetch", "web_search",
	// sibling extensions (deployed via their own make install)
	"transcribe", "budoteam", "curl", "email", "image", "weather",
}
