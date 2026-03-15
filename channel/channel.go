package channel

import (
	"context"
)

type TranscribeFn func(ctx context.Context, filePath string) (string, error)

type Message struct {
	ChatID string
	UserID string
	Text   string
	Images [][]byte
}

type Button struct {
	Text  string
	Value string
}

type Response struct {
	ChatID    string
	Text      string
	ImagePath string
	Buttons   [][]Button
}

type MessageHandler func(Message)

type Channel interface {
	Start(ctx context.Context, onMessage MessageHandler) error
	Send(ctx context.Context, resp Response) error
	SendTyping(ctx context.Context, chatID string) error
}
