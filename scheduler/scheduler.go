package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/haratosan/torii/agent"
	"github.com/haratosan/torii/channel"
	"github.com/haratosan/torii/llm"
	"github.com/haratosan/torii/session"
	"github.com/haratosan/torii/store"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	store    *store.Store
	channel  channel.Channel
	agent    *agent.Agent
	sessions *session.Store
	interval time.Duration
	logger   *slog.Logger
}

func New(db *store.Store, ch channel.Channel, ag *agent.Agent, sess *session.Store, interval time.Duration, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		store:    db,
		channel:  ch,
		agent:    ag,
		sessions: sess,
		interval: interval,
		logger:   logger,
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.logger.Info("scheduler started", "interval", s.interval)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopped")
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	tasks, err := s.store.DueTasks(time.Now())
	if err != nil {
		s.logger.Error("scheduler: query due tasks", "error", err)
		return
	}

	for _, task := range tasks {
		switch task.Type {
		case "remind":
			s.handleRemind(ctx, task)
		case "cron":
			s.handleCron(ctx, task)
		}
	}
}

func (s *Scheduler) handleRemind(ctx context.Context, task *store.Task) {
	text := fmt.Sprintf("⏰ Reminder: %s", task.Description)
	if err := s.channel.Send(ctx, channel.Response{ChatID: task.ChatID, Text: text}); err != nil {
		s.logger.Error("scheduler: send reminder", "error", err, "task_id", task.ID)
		return
	}
	// One-shot: delete after sending
	if err := s.store.DeleteTask(task.ID); err != nil {
		s.logger.Error("scheduler: delete remind task", "error", err, "task_id", task.ID)
	}
	s.logger.Info("reminder sent", "task_id", task.ID, "chat_id", task.ChatID)
}

func (s *Scheduler) handleCron(ctx context.Context, task *store.Task) {
	tmpChatID := fmt.Sprintf("cron:%d", task.ID)

	result, err := s.runCron(ctx, task, tmpChatID)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		s.logger.Warn("cron agent timed out, retrying once", "task_id", task.ID)
		s.sessions.Clear(tmpChatID)
		result, err = s.runCron(ctx, task, tmpChatID)
	}

	// Clean up temporary session
	s.sessions.Clear(tmpChatID)
	if err != nil {
		s.logger.Error("scheduler: cron agent", "error", err, "task_id", task.ID)
	} else if result.Silent {
		s.logger.Info("cron task silent, skipping send", "task_id", task.ID)
	} else {
		// Append only the final response to the real chat session so the user can reply
		s.sessions.Append(task.ChatID, llm.ChatMessage{
			Role:    llm.RoleAssistant,
			Content: result.Text,
		})
		if err := s.channel.Send(ctx, channel.Response{ChatID: task.ChatID, Text: result.Text}); err != nil {
			s.logger.Error("scheduler: send cron result", "error", err, "task_id", task.ID)
		}
	}

	// Compute next run
	nextRun, err := nextCronRun(task.Schedule, time.Now())
	if err != nil {
		s.logger.Error("scheduler: compute next run", "error", err, "task_id", task.ID)
		return
	}
	if err := s.store.UpdateNextRun(task.ID, nextRun); err != nil {
		s.logger.Error("scheduler: update next run", "error", err, "task_id", task.ID)
	}
	s.logger.Info("cron task executed", "task_id", task.ID, "next_run", nextRun)
}

// runCron executes a cron task's agent call with a fresh 2-minute timeout.
// Extracted so handleCron can retry on ctx-deadline errors without
// duplicating the context/session bookkeeping.
func (s *Scheduler) runCron(ctx context.Context, task *store.Task, tmpChatID string) (*agent.AgentResponse, error) {
	taskCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	return s.agent.HandleMessage(taskCtx, channel.Message{
		ChatID:     tmpChatID,
		ToolChatID: task.ChatID,
		UserID:     task.UserID,
		Text:       task.Description,
	})
}

func nextCronRun(schedule string, after time.Time) (time.Time, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(schedule)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(after), nil
}
