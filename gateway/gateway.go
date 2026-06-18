package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/haratosan/torii/agent"
	"github.com/haratosan/torii/channel"
)

type PDFImportFn func(ctx context.Context, chatID, fileName string, data []byte) (string, error)

type Gateway struct {
	agent         *agent.Agent
	channel       channel.Channel
	agentTimeout  time.Duration
	extensionDirs []string
	pdfImport     PDFImportFn
	limiter       *rateLimiter
	logger        *slog.Logger
}

func New(ch channel.Channel, ag *agent.Agent, agentTimeout time.Duration, extensionDirs []string, pdfImport PDFImportFn, logger *slog.Logger) *Gateway {
	return &Gateway{
		agent:         ag,
		channel:       ch,
		agentTimeout:  agentTimeout,
		extensionDirs: extensionDirs,
		pdfImport:     pdfImport,
		limiter:       newRateLimiter(12, 5*time.Second),
		logger:        logger,
	}
}

func (g *Gateway) Run(ctx context.Context) error {
	g.logger.Info("torii starting")

	return g.channel.Start(ctx, func(msg channel.Message) {
		g.logger.Info("message received", "chat_id", msg.ChatID, "user_id", msg.UserID)

		// Rate-limit per (chat, user). Telegram is already authenticated by
		// the channel allowlist, so this only protects against a flood from
		// an authorized account — typically a runaway script or a
		// compromised session.
		if g.limiter != nil && !g.limiter.allow(msg.ChatID+":"+msg.UserID) {
			g.logger.Warn("rate-limit drop", "chat_id", msg.ChatID, "user_id", msg.UserID)
			return
		}

		// Handle PDF document import
		if msg.Document != nil && msg.Document.MimeType == "application/pdf" && g.pdfImport != nil {
			typingCtx, stopTyping := context.WithCancel(ctx)
			defer stopTyping()
			go g.keepTyping(typingCtx, msg.ChatID)

			result, err := g.pdfImport(ctx, msg.ChatID, msg.Document.FileName, msg.Document.Data)

			resp := channel.Response{ChatID: msg.ChatID}
			if err != nil {
				g.logger.Error("pdf import error", "error", err)
				resp.Text = fmt.Sprintf("PDF import failed: %v", err)
			} else {
				resp.Text = result
			}
			if err := g.channel.Send(ctx, resp); err != nil {
				g.logger.Error("send error", "error", err)
			}
			return
		}

		// Handle bot commands
		if resp, ok := g.agent.HandleCommand(msg); ok {
			if err := g.channel.Send(ctx, channel.Response{ChatID: msg.ChatID, Text: resp}); err != nil {
				g.logger.Error("send error", "error", err)
			}
			return
		}

		// Start typing indicator
		typingCtx, stopTyping := context.WithCancel(ctx)
		defer stopTyping()
		go g.keepTyping(typingCtx, msg.ChatID)

		// Apply agent timeout if configured
		agentCtx := ctx
		var agentCancel context.CancelFunc
		if g.agentTimeout > 0 {
			agentCtx, agentCancel = context.WithTimeout(ctx, g.agentTimeout)
			defer agentCancel()
		}

		result, err := g.agent.HandleMessage(agentCtx, msg)

		resp := channel.Response{ChatID: msg.ChatID}

		if err != nil {
			g.logger.Error("agent error", "error", err)
			resp.Text = "Sorry, something went wrong."
		} else if result.Silent {
			return
		} else {
			resp.Text = result.Text
			resp.ImagePath = channel.ValidateImagePath(result.ImagePath, g.extensionDirs, g.logger)
			resp.Buttons = result.Buttons
		}

		if resp.Text == "" && resp.ImagePath == "" {
			resp.Text = "No response from the model."
		}

		if err := g.channel.Send(ctx, resp); err != nil {
			g.logger.Error("send error", "error", err)
		}
	})
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
