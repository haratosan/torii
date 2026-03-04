package channel

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type Telegram struct {
	token        string
	allowedUsers []int64
	transcribe   TranscribeFn
	logger       *slog.Logger
	handler      MessageHandler
	bot          *bot.Bot
}

func NewTelegram(token string, allowedUsers []int64, transcribe TranscribeFn, logger *slog.Logger) *Telegram {
	return &Telegram{
		token:        token,
		allowedUsers: allowedUsers,
		transcribe:   transcribe,
		logger:       logger,
	}
}

func (t *Telegram) Start(ctx context.Context, onMessage MessageHandler) error {
	t.handler = onMessage

	opts := []bot.Option{
		bot.WithDefaultHandler(t.handleUpdate),
	}

	b, err := bot.New(t.token, opts...)
	if err != nil {
		return fmt.Errorf("telegram bot: %w", err)
	}
	t.bot = b

	t.logger.Info("telegram bot starting")
	b.Start(ctx)
	return nil
}

func (t *Telegram) Send(ctx context.Context, resp Response) error {
	chatID, err := strconv.ParseInt(resp.ChatID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid chat id: %w", err)
	}

	// Telegram has a 4096 character limit per message
	text := resp.Text
	if len(text) > 4096 {
		text = text[:4093] + "..."
	}

	_, err = t.bot.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      markdownToHTML(text),
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		t.logger.Debug("html send failed, retrying plain", "error", err)
		_, err = t.bot.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   text,
		})
	}
	return err
}

func (t *Telegram) SendTyping(ctx context.Context, chatID string) error {
	id, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return err
	}
	_, err = t.bot.SendChatAction(ctx, &bot.SendChatActionParams{
		ChatID: id,
		Action: models.ChatActionTyping,
	})
	return err
}

func (t *Telegram) handleUpdate(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}

	text := update.Message.Text
	if text == "" && update.Message.Voice != nil && t.transcribe != nil {
		var err error
		text, err = t.handleVoice(ctx, b, update.Message.Voice)
		if err != nil {
			t.logger.Error("voice transcription failed", "error", err)
			return
		}
	}
	if text == "" {
		return
	}

	userID := update.Message.From.ID
	chatID := update.Message.Chat.ID

	// Check allowed users
	if len(t.allowedUsers) > 0 {
		allowed := false
		for _, id := range t.allowedUsers {
			if id == userID {
				allowed = true
				break
			}
		}
		if !allowed {
			t.logger.Warn("unauthorized user", "user_id", userID)
			return
		}
	}

	t.logger.Info("telegram message", "chat_id", chatID, "user_id", userID, "text", text)

	if t.handler != nil {
		t.handler(Message{
			ChatID: strconv.FormatInt(chatID, 10),
			UserID: strconv.FormatInt(userID, 10),
			Text:   text,
		})
	}
}

func (t *Telegram) handleVoice(ctx context.Context, b *bot.Bot, voice *models.Voice) (string, error) {
	file, err := b.GetFile(ctx, &bot.GetFileParams{FileID: voice.FileID})
	if err != nil {
		return "", fmt.Errorf("get file: %w", err)
	}

	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", t.token, file.FilePath)

	resp, err := http.Get(fileURL)
	if err != nil {
		return "", fmt.Errorf("download voice: %w", err)
	}
	defer resp.Body.Close()

	tmpFile, err := os.CreateTemp("", "torii-voice-*.ogg")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return "", fmt.Errorf("write voice file: %w", err)
	}
	tmpFile.Close()

	text, err := t.transcribe(ctx, tmpFile.Name())
	if err != nil {
		return "", fmt.Errorf("transcribe: %w", err)
	}

	t.logger.Info("voice transcribed", "text", text)
	return text, nil
}
