package channel

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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

	// Send photo if image path is provided
	if resp.ImagePath != "" {
		return t.sendPhoto(ctx, chatID, resp.ImagePath, resp.Text)
	}

	// Telegram has a 4096 character limit per message
	text := resp.Text
	if len(text) > 4096 {
		text = text[:4093] + "..."
	}

	params := &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      markdownToHTML(text),
		ParseMode: models.ParseModeHTML,
	}

	// Build inline keyboard if buttons provided
	if len(resp.Buttons) > 0 {
		var keyboard [][]models.InlineKeyboardButton
		for _, row := range resp.Buttons {
			var kbRow []models.InlineKeyboardButton
			for _, btn := range row {
				callbackData := btn.Value
				// Telegram callback_data limit is 64 bytes
				if len(callbackData) > 64 {
					callbackData = callbackData[:64]
				}
				kbRow = append(kbRow, models.InlineKeyboardButton{
					Text:         btn.Text,
					CallbackData: callbackData,
				})
			}
			keyboard = append(keyboard, kbRow)
		}
		params.ReplyMarkup = &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		}
	}

	_, err = t.bot.SendMessage(ctx, params)
	if err != nil {
		t.logger.Debug("html send failed, retrying plain", "error", err)
		params.ParseMode = ""
		params.Text = text
		_, err = t.bot.SendMessage(ctx, params)
	}
	return err
}

func (t *Telegram) sendPhoto(ctx context.Context, chatID int64, imagePath string, caption string) error {
	f, err := os.Open(imagePath)
	if err != nil {
		return fmt.Errorf("open image: %w", err)
	}
	defer f.Close()

	// Telegram caption limit is 1024 characters
	if len(caption) > 1024 {
		caption = caption[:1021] + "..."
	}

	_, err = t.bot.SendPhoto(ctx, &bot.SendPhotoParams{
		ChatID: chatID,
		Photo: &models.InputFileUpload{
			Filename: filepath.Base(imagePath),
			Data:     f,
		},
		Caption: caption,
	})

	// Clean up the image file after sending
	os.Remove(imagePath)

	if err != nil {
		return fmt.Errorf("send photo: %w", err)
	}
	return nil
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
	// Handle inline keyboard callback queries
	if update.CallbackQuery != nil {
		t.handleCallbackQuery(ctx, b, update.CallbackQuery)
		return
	}

	if update.Message == nil {
		return
	}

	text := update.Message.Text
	if text == "" && update.Message.Voice != nil {
		if t.transcribe == nil {
			t.logger.Warn("voice message received but no transcribe extension installed")
			return
		}
		var err error
		text, err = t.handleVoice(ctx, b, update.Message.Voice)
		if err != nil {
			t.logger.Error("voice transcription failed", "error", err)
			return
		}
	}

	// Handle photo messages
	var images [][]byte
	if len(update.Message.Photo) > 0 {
		// Use caption as text for photo messages
		if text == "" {
			text = update.Message.Caption
		}
		// Download highest resolution photo (last in array)
		best := update.Message.Photo[len(update.Message.Photo)-1]
		imgData, err := t.downloadFile(ctx, b, best.FileID)
		if err != nil {
			t.logger.Error("photo download failed", "error", err)
		} else {
			images = append(images, imgData)
		}
	}

	// Handle PDF documents
	var doc *Document
	if update.Message.Document != nil && update.Message.Document.MimeType == "application/pdf" {
		if text == "" {
			text = update.Message.Caption
		}
		docData, err := t.downloadFile(ctx, b, update.Message.Document.FileID)
		if err != nil {
			t.logger.Error("document download failed", "error", err)
		} else {
			doc = &Document{
				FileName: update.Message.Document.FileName,
				MimeType: update.Message.Document.MimeType,
				Data:     docData,
			}
		}
	}

	if text == "" && len(images) == 0 && doc == nil {
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

	var replyText string
	if update.Message.ReplyToMessage != nil {
		replyText = update.Message.ReplyToMessage.Text
		if replyText == "" && update.Message.ReplyToMessage.Caption != "" {
			replyText = update.Message.ReplyToMessage.Caption
		}
	}

	if t.handler != nil {
		t.handler(Message{
			ChatID:    strconv.FormatInt(chatID, 10),
			UserID:    strconv.FormatInt(userID, 10),
			Text:      text,
			Images:    images,
			Document:  doc,
			ReplyText: replyText,
		})
	}
}

func (t *Telegram) handleCallbackQuery(ctx context.Context, b *bot.Bot, cq *models.CallbackQuery) {
	// Answer the callback to remove the loading indicator
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: cq.ID,
	})

	userID := cq.From.ID
	chatID := cq.Message.Message.Chat.ID

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
			t.logger.Warn("unauthorized callback user", "user_id", userID)
			return
		}
	}

	text := cq.Data
	if text == "" {
		return
	}

	t.logger.Info("telegram callback", "chat_id", chatID, "user_id", userID, "data", text)

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

func (t *Telegram) downloadFile(ctx context.Context, b *bot.Bot, fileID string) ([]byte, error) {
	file, err := b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}

	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", t.token, file.FilePath)

	resp, err := http.Get(fileURL)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	return data, nil
}
