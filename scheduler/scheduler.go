package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/haratosan/torii/agent"
	"github.com/haratosan/torii/channel"
	"github.com/haratosan/torii/store"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	store    *store.Store
	channel  channel.Channel
	agent    *agent.Agent
	interval time.Duration
	logger   *slog.Logger
}

func New(db *store.Store, ch channel.Channel, ag *agent.Agent, interval time.Duration, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		store:    db,
		channel:  ch,
		agent:    ag,
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
	// Process through agent
	taskCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	reply, err := s.agent.HandleMessage(taskCtx, channel.Message{
		ChatID: task.ChatID,
		UserID: task.UserID,
		Text:   task.Description,
	})
	if err != nil {
		s.logger.Error("scheduler: cron agent", "error", err, "task_id", task.ID)
	} else {
		if err := s.channel.Send(ctx, channel.Response{ChatID: task.ChatID, Text: reply}); err != nil {
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

func nextCronRun(schedule string, after time.Time) (time.Time, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(schedule)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(after), nil
}
