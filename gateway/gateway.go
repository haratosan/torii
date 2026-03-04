package gateway

import (
	"context"
	"log/slog"
	"time"

	"github.com/haratosan/torii/agent"
	"github.com/haratosan/torii/channel"
)

type Message struct {
	ChatID string
	UserID string
	Text   string
}

type Response struct {
	ChatID string
	Text   string
}

type Gateway struct {
	agent        *agent.Agent
	channel      channel.Channel
	agentTimeout time.Duration
	logger       *slog.Logger
}

func New(ch channel.Channel, ag *agent.Agent, agentTimeout time.Duration, logger *slog.Logger) *Gateway {
	return &Gateway{
		agent:        ag,
		channel:      ch,
		agentTimeout: agentTimeout,
		logger:       logger,
	}
}

func (g *Gateway) Run(ctx context.Context) error {
	g.logger.Info("torii starting")

	return g.channel.Start(ctx, func(msg channel.Message) {
		g.logger.Info("message received", "chat_id", msg.ChatID, "user_id", msg.UserID)

		// Start typing indicator
		typingCtx, stopTyping := context.WithCancel(ctx)
		go g.keepTyping(typingCtx, msg.ChatID)

		// Apply agent timeout if configured
		agentCtx := ctx
		var agentCancel context.CancelFunc
		if g.agentTimeout > 0 {
			agentCtx, agentCancel = context.WithTimeout(ctx, g.agentTimeout)
		}

		reply, err := g.agent.HandleMessage(agentCtx, msg)

		if agentCancel != nil {
			agentCancel()
		}
		stopTyping()

		if err != nil {
			g.logger.Error("agent error", "error", err)
			reply = "Sorry, something went wrong."
		}

		if err := g.channel.Send(ctx, channel.Response{ChatID: msg.ChatID, Text: reply}); err != nil {
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
