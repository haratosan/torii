package extension

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

type Manifest struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Version     string         `json:"version"`
	Type        string         `json:"type"`    // "" (default/binary) or "command"
	Command     string         `json:"command"` // Legacy: shell command with {{param}} placeholders. Prefer Argv.
	// Argv is the preferred form for command-type extensions: each element
	// becomes a separate argv slot, so {{param}} substitutions cannot leak
	// into adjacent shell metacharacters (no `sh -c` involved).
	Argv       []string       `json:"argv"`
	Parameters map[string]any `json:"parameters"`
	Env        []string       `json:"env"`
}

type ExtRequest struct {
	Action string   `json:"action"`
	Input  string   `json:"input"`
	ChatID string   `json:"chat_id"`
	UserID string   `json:"user_id"`
	Images []string `json:"images,omitempty"`
}

type ExtResponse struct {
	Output string         `json:"output"`
	Error  string         `json:"error"`
	Data   map[string]any `json:"data"`
}

type Extension struct {
	Manifest   Manifest
	Executable string
	Dir        string
}

type Registry struct {
	extensions map[string]*Extension
	builtins   map[string]*BuiltinTool
	logger     *slog.Logger
}

func NewRegistry(logger *slog.Logger) *Registry {
	return &Registry{
		extensions: make(map[string]*Extension),
		builtins:   make(map[string]*BuiltinTool),
		logger:     logger,
	}
}

func (r *Registry) Discover(dirs []string) error {
	for _, dir := range dirs {
		// Expand ~
		if len(dir) > 0 && dir[0] == '~' {
			home, err := os.UserHomeDir()
			if err == nil {
				dir = filepath.Join(home, dir[1:])
			}
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			r.logger.Warn("cannot read extension dir", "dir", dir, "error", err)
			continue
		}

		absDir, _ := filepath.Abs(dir)

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			extDir := filepath.Join(absDir, entry.Name())
			manifestPath := filepath.Join(extDir, "manifest.json")

			data, err := os.ReadFile(manifestPath)
			if err != nil {
				continue
			}

			var manifest Manifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				r.logger.Warn("invalid manifest", "path", manifestPath, "error", err)
				continue
			}

			if manifest.Type == "command" {
				r.extensions[manifest.Name] = &Extension{
					Manifest: manifest,
					Dir:      extDir,
				}
				r.logger.Info("extension loaded", "name", manifest.Name, "type", "command")
			} else {
				// Look for executable with the directory name. Resolve
				// symlinks and verify the result is still rooted under the
				// extension directory — without this check, a symlinked
				// entry could point at a binary anywhere on the filesystem.
				execName := entry.Name()
				execPath := filepath.Join(extDir, execName)
				if _, err := os.Stat(execPath); err != nil {
					r.logger.Warn("extension executable not found", "path", execPath)
					continue
				}
				resolvedExec, err := filepath.EvalSymlinks(execPath)
				if err != nil {
					r.logger.Warn("extension executable: cannot resolve symlinks", "path", execPath, "error", err)
					continue
				}
				resolvedDir, err := filepath.EvalSymlinks(extDir)
				if err != nil {
					r.logger.Warn("extension dir: cannot resolve symlinks", "dir", extDir, "error", err)
					continue
				}
				if !strings.HasPrefix(resolvedExec, resolvedDir+string(filepath.Separator)) && resolvedExec != resolvedDir {
					r.logger.Warn("extension executable escapes its directory — skipping", "path", execPath, "resolved", resolvedExec)
					continue
				}

				r.extensions[manifest.Name] = &Extension{
					Manifest:   manifest,
					Executable: resolvedExec,
					Dir:        extDir,
				}
				r.logger.Info("extension loaded", "name", manifest.Name, "path", resolvedExec)
			}
		}
	}

	if len(r.extensions) == 0 {
		r.logger.Warn("no extensions found")
	}

	return nil
}

func (r *Registry) Get(name string) (*Extension, error) {
	ext, ok := r.extensions[name]
	if !ok {
		return nil, fmt.Errorf("unknown extension: %s", name)
	}
	return ext, nil
}

func (r *Registry) List() []*Extension {
	result := make([]*Extension, 0, len(r.extensions))
	for _, ext := range r.extensions {
		result = append(result, ext)
	}
	return result
}

func (r *Registry) RegisterBuiltin(bt *BuiltinTool) {
	r.builtins[bt.Def.Name] = bt
	r.logger.Info("builtin registered", "name", bt.Def.Name)
}

func (r *Registry) GetBuiltin(name string) (*BuiltinTool, bool) {
	bt, ok := r.builtins[name]
	return bt, ok
}

func (r *Registry) ListBuiltins() []*BuiltinTool {
	result := make([]*BuiltinTool, 0, len(r.builtins))
	for _, bt := range r.builtins {
		result = append(result, bt)
	}
	return result
}
