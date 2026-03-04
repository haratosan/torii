package channel

import "context"

type Message struct {
	ChatID string
	UserID string
	Text   string
}

type Response struct {
	ChatID string
	Text   string
}

type MessageHandler func(Message)

type Channel interface {
	Start(ctx context.Context, onMessage MessageHandler) error
	Send(ctx context.Context, resp Response) error
	SendTyping(ctx context.Context, chatID string) error
}
