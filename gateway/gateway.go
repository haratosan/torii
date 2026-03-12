package gateway

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/haratosan/torii/agent"
	"github.com/haratosan/torii/channel"
)

type Gateway struct {
	agent         *agent.Agent
	channel       channel.Channel
	agentTimeout  time.Duration
	extensionDirs []string
	logger        *slog.Logger
}

func New(ch channel.Channel, ag *agent.Agent, agentTimeout time.Duration, extensionDirs []string, logger *slog.Logger) *Gateway {
	return &Gateway{
		agent:         ag,
		channel:       ch,
		agentTimeout:  agentTimeout,
		extensionDirs: extensionDirs,
		logger:        logger,
	}
}

func (g *Gateway) Run(ctx context.Context) error {
	g.logger.Info("torii starting")

	return g.channel.Start(ctx, func(msg channel.Message) {
		g.logger.Info("message received", "chat_id", msg.ChatID, "user_id", msg.UserID)

		// Handle bot commands
		if resp, ok := g.agent.HandleCommand(msg); ok {
			if err := g.channel.Send(ctx, channel.Response{ChatID: msg.ChatID, Text: resp}); err != nil {
				g.logger.Error("send error", "error", err)
			}
			return
		}

		// Start typing indicator
		typingCtx, stopTyping := context.WithCancel(ctx)
		go g.keepTyping(typingCtx, msg.ChatID)

		// Apply agent timeout if configured
		agentCtx := ctx
		var agentCancel context.CancelFunc
		if g.agentTimeout > 0 {
			agentCtx, agentCancel = context.WithTimeout(ctx, g.agentTimeout)
		}

		result, err := g.agent.HandleMessage(agentCtx, msg)

		if agentCancel != nil {
			agentCancel()
		}
		stopTyping()

		resp := channel.Response{ChatID: msg.ChatID}

		if err != nil {
			g.logger.Error("agent error", "error", err)
			resp.Text = "Sorry, something went wrong."
		} else if result.Silent {
			return
		} else {
			resp.Text = result.Text
			resp.ImagePath = g.validateImagePath(result.ImagePath)
		}

		if resp.Text == "" && resp.ImagePath == "" {
			resp.Text = "No response from the model."
		}

		if err := g.channel.Send(ctx, resp); err != nil {
			g.logger.Error("send error", "error", err)
		}
	})
}

// validateImagePath checks that the path is safe: resolves symlinks,
// verifies it's within an allowed extension directory, and only allows
// .png and .jpg files.
func (g *Gateway) validateImagePath(imagePath string) string {
	if imagePath == "" {
		return ""
	}

	// Only allow .png and .jpg
	ext := strings.ToLower(filepath.Ext(imagePath))
	if ext != ".png" && ext != ".jpg" {
		g.logger.Warn("image path rejected: invalid extension", "path", imagePath, "ext", ext)
		return ""
	}

	// Resolve symlinks and normalize
	resolved, err := filepath.EvalSymlinks(imagePath)
	if err != nil {
		g.logger.Warn("image path rejected: cannot resolve", "path", imagePath, "error", err)
		return ""
	}
	resolved = filepath.Clean(resolved)

	// Check that resolved path is within one of the configured extension dirs
	for _, dir := range g.extensionDirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		absDir, err = filepath.EvalSymlinks(absDir)
		if err != nil {
			continue
		}
		// Ensure prefix check uses trailing separator to avoid partial matches
		if strings.HasPrefix(resolved, absDir+string(filepath.Separator)) {
			return resolved
		}
	}

	g.logger.Warn("image path rejected: outside allowed dirs", "path", imagePath, "resolved", resolved)
	return ""
}

func (g *Gateway) keepTyping(ctx context.Context, chatID string) {
	if err := g.channel.SendTyping(ctx, chatID); err != nil {
		g.logger.Debug("send typing error", "error", err)
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := g.channel.SendTyping(ctx, chatID); err != nil {
				g.logger.Debug("send typing error", "error", err)
			}
		}
	}
}
