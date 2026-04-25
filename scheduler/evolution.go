package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/haratosan/torii/channel"
	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/llm"
	"github.com/haratosan/torii/store"
)

const (
	evolutionWindow      = 7 * 24 * time.Hour
	evolutionMinInterval = 23 * time.Hour
	evolutionTimeout     = 5 * time.Minute
	evolutionMaxAdds     = 3
	evolutionMaxUpdates  = 1
	traceFetchLimit      = 2000
)

// traceSummary is the structured digest of a user's recent activity that
// drives the evolution prompt. We build this in Go (not by feeding raw history
// to the LLM) so context stays bounded and the LLM can focus on patterns
// rather than raw transcripts.
type traceSummary struct {
	Window         string
	UserMsgCount   int
	ToolCallCounts map[string]int
	FailureCounts  map[string]int
	Samples        []sampleQuery
}

type sampleQuery struct {
	UserText  string
	ToolChain []string
}

func (s *Scheduler) handleEvolve(ctx context.Context, task *store.Task) {
	if task.UserID == "" {
		s.logger.Warn("evolve task has empty user_id, skipping", "task_id", task.ID)
		s.rescheduleEvolve(task)
		return
	}

	// Rate limit: at most one done run per ~23h per user.
	if last, err := s.store.LastEvolutionRun(task.UserID); err == nil && last != nil {
		if last.Status == "done" && time.Since(last.StartedAt) < evolutionMinInterval {
			s.logger.Info("evolve skipped (rate limit)",
				"user_id", task.UserID, "last_started", last.StartedAt)
			s.rescheduleEvolve(task)
			return
		}
	}

	// Trace summary first — if there is no activity at all, skip cheaply.
	since := time.Now().Add(-evolutionWindow)
	summary, err := s.buildTraceSummary(task.UserID, since)
	if err != nil {
		s.logger.Error("evolve: build trace summary", "error", err, "user_id", task.UserID)
		s.rescheduleEvolve(task)
		return
	}
	if summary.UserMsgCount == 0 {
		runID, _ := s.store.BeginEvolutionRun(task.UserID)
		if runID > 0 {
			_ = s.store.FinishEvolutionRun(runID, "skipped",
				`{"reason":"no activity in window"}`)
		}
		s.logger.Info("evolve skipped (no activity)", "user_id", task.UserID)
		s.rescheduleEvolve(task)
		return
	}

	runID, err := s.store.BeginEvolutionRun(task.UserID)
	if err != nil {
		s.logger.Error("evolve: begin run", "error", err, "user_id", task.UserID)
		s.rescheduleEvolve(task)
		return
	}

	tmpChatID := fmt.Sprintf("evolve:%d:%d", task.ID, runID)
	prompt := buildEvolutionPrompt(summary)

	taskCtx, cancel := context.WithTimeout(ctx, evolutionTimeout)
	defer cancel()
	evoCtx := extension.WithEvolutionLimits(taskCtx, evolutionMaxAdds, evolutionMaxUpdates)

	result, err := s.agent.HandleMessage(evoCtx, channel.Message{
		ChatID:     tmpChatID,
		ToolChatID: "", // Skills/Memory key on UserID, not ChatID
		UserID:     task.UserID,
		Text:       prompt,
	})

	// Always clear the synthetic conversation regardless of outcome.
	s.sessions.Clear(tmpChatID)

	leaked := false
	var leakedSnippet string
	if err == nil && result != nil && !result.Silent && strings.TrimSpace(result.Text) != "" {
		leaked = true
		leakedSnippet = result.Text
		if len(leakedSnippet) > 200 {
			leakedSnippet = leakedSnippet[:200] + "…"
		}
		s.logger.Warn("evolution leaked text (suppressed)",
			"user_id", task.UserID, "snippet", leakedSnippet)
	}

	status := "done"
	addsMade := int32(evolutionMaxAdds)
	updatesMade := int32(evolutionMaxUpdates)
	if lim, ok := extension.EvolutionLimitsFromContext(evoCtx); ok {
		// Counters can go negative when the LLM keeps trying past the cap;
		// clamp at zero for display.
		if rem := lim.AddsRemaining(); rem >= 0 {
			addsMade = int32(evolutionMaxAdds) - rem
		}
		if rem := lim.UpdatesRemaining(); rem >= 0 {
			updatesMade = int32(evolutionMaxUpdates) - rem
		}
	}
	if err != nil {
		status = "error"
	}

	summaryPayload := map[string]any{
		"adds_made":     addsMade,
		"updates_made":  updatesMade,
		"leaked_text":   leaked,
		"trace_msgs":    summary.UserMsgCount,
		"tool_calls":    sumMap(summary.ToolCallCounts),
		"window":        summary.Window,
	}
	if leaked {
		summaryPayload["leaked_snippet"] = leakedSnippet
	}
	if err != nil {
		summaryPayload["error"] = err.Error()
	}
	summaryJSON, _ := json.Marshal(summaryPayload)

	if ferr := s.store.FinishEvolutionRun(runID, status, string(summaryJSON)); ferr != nil {
		s.logger.Error("evolve: finish run", "error", ferr, "run_id", runID)
	}
	s.logger.Info("evolve done",
		"user_id", task.UserID,
		"run_id", runID,
		"status", status,
		"adds", addsMade,
		"updates", updatesMade,
	)

	s.rescheduleEvolve(task)
}

func (s *Scheduler) rescheduleEvolve(task *store.Task) {
	next, err := nextCronRun(task.Schedule, time.Now())
	if err != nil {
		s.logger.Error("evolve: compute next run", "error", err, "task_id", task.ID)
		return
	}
	if err := s.store.UpdateNextRun(task.ID, next); err != nil {
		s.logger.Error("evolve: update next run", "error", err, "task_id", task.ID)
	}
}

// buildTraceSummary scans the last `evolutionWindow` of session_messages for
// userID and turns them into a compact pattern digest the LLM can act on.
func (s *Scheduler) buildTraceSummary(userID string, since time.Time) (*traceSummary, error) {
	msgs, err := s.store.LoadMessagesByUserSince(userID, since, traceFetchLimit)
	if err != nil {
		return nil, err
	}
	out := &traceSummary{
		Window:         "last 7 days",
		ToolCallCounts: map[string]int{},
		FailureCounts:  map[string]int{},
	}

	// Walk in chronological order (LoadMessagesByUserSince returns ASC).
	// Pattern: a `user` message starts a new sample; subsequent assistant
	// tool_calls and tool results are attributed to it until the next user.
	var current *sampleQuery
	var lastToolName string
	for _, m := range msgs {
		switch m.Role {
		case string(llm.RoleUser):
			out.UserMsgCount++
			text := strings.TrimSpace(m.Content)
			if len(text) > 200 {
				text = text[:200] + "…"
			}
			if current != nil && len(current.ToolChain) > 0 {
				out.Samples = append(out.Samples, *current)
			}
			current = &sampleQuery{UserText: text}
			lastToolName = ""

		case string(llm.RoleAssistant):
			if m.ToolCalls == "" {
				continue
			}
			var tcs []llm.ToolCall
			if err := json.Unmarshal([]byte(m.ToolCalls), &tcs); err != nil {
				continue
			}
			for _, tc := range tcs {
				if tc.Function.Name == "" {
					continue
				}
				out.ToolCallCounts[tc.Function.Name]++
				if current != nil && len(current.ToolChain) < 8 {
					current.ToolChain = append(current.ToolChain, tc.Function.Name)
				}
				lastToolName = tc.Function.Name
			}

		case string(llm.RoleTool):
			if lastToolName != "" && strings.HasPrefix(strings.TrimSpace(m.Content), "Error:") {
				out.FailureCounts[lastToolName]++
			}
		}
	}
	if current != nil && len(current.ToolChain) > 0 {
		out.Samples = append(out.Samples, *current)
	}

	out.Samples = pickDiverseSamples(out.Samples, 5)
	return out, nil
}

// pickDiverseSamples returns up to `n` samples favoring longer tool chains
// while keeping the user-text sets diverse via cheap Jaccard dedup. We avoid
// a full clustering pass because n is tiny.
func pickDiverseSamples(in []sampleQuery, n int) []sampleQuery {
	if len(in) <= n {
		return in
	}
	sort.SliceStable(in, func(i, j int) bool {
		return len(in[i].ToolChain) > len(in[j].ToolChain)
	})

	picked := make([]sampleQuery, 0, n)
	pickedSets := make([]map[string]struct{}, 0, n)
	for _, s := range in {
		set := tokenSet(s.UserText)
		duplicate := false
		for _, prev := range pickedSets {
			if jaccard(set, prev) > 0.6 {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		picked = append(picked, s)
		pickedSets = append(pickedSets, set)
		if len(picked) == n {
			break
		}
	}
	return picked
}

func tokenSet(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, t := range strings.Fields(strings.ToLower(s)) {
		if len(t) >= 3 {
			out[t] = struct{}{}
		}
	}
	return out
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func sumMap(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}

// buildEvolutionPrompt renders the system-evolution prompt the LLM sees as
// its user message. Plain text — no model-specific syntax — so it works
// across Ollama backends.
func buildEvolutionPrompt(s *traceSummary) string {
	var sb strings.Builder
	sb.WriteString(`SYSTEM EVOLUTION MODE — automated, no human will read your reply.

You are reviewing your own behavior with this user over the last 7 days to capture
recurring patterns as durable skills. You have access to the `)
	sb.WriteString("`skills`")
	sb.WriteString(` tool (write)
and `)
	sb.WriteString("`memory`")
	sb.WriteString(` tool (READ-ONLY in this mode — any write call will be rejected).
You MUST end this run by calling `)
	sb.WriteString("`no-reply`")
	sb.WriteString(`. Do NOT produce a final text answer.

`)
	sb.WriteString(fmt.Sprintf("Trace summary (window: %s):\n", s.Window))
	sb.WriteString(fmt.Sprintf("- User messages: %d\n", s.UserMsgCount))
	sb.WriteString("- Tool calls: ")
	sb.WriteString(formatCounts(s.ToolCallCounts))
	sb.WriteString("\n")
	if len(s.FailureCounts) > 0 {
		sb.WriteString("- Failures: ")
		sb.WriteString(formatCounts(s.FailureCounts))
		sb.WriteString(` (results starting with "Error:")`)
		sb.WriteString("\n")
	}
	sb.WriteString("\n")

	if len(s.Samples) > 0 {
		sb.WriteString("Sample interaction patterns:\n")
		for i, sm := range s.Samples {
			sb.WriteString(fmt.Sprintf("%d. User: %q\n", i+1, sm.UserText))
			if len(sm.ToolChain) > 0 {
				sb.WriteString("   Tools: ")
				sb.WriteString(strings.Join(sm.ToolChain, " → "))
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString(`YOUR TASK:
1. First call `)
	sb.WriteString("`skills list`")
	sb.WriteString(` to see what's already captured.
2. Identify AT MOST 3 recurring patterns worth turning into durable skills.
3. For each pattern:
   - If a similar-titled skill exists AND is materially incomplete, call
     `)
	sb.WriteString("`skills update`")
	sb.WriteString(` with the improved body. Maximum 1 update this run.
   - Otherwise call `)
	sb.WriteString("`skills add`")
	sb.WriteString(` with a focused title and a body ≤500 chars
     (when to use, which tools in which order, common pitfalls). Default
     scope (user-scoped). Set global=true ONLY if the pattern is clearly
     model- and user-agnostic.
4. Hard limits enforced by the runtime:
   - max 3 `)
	sb.WriteString("`skills add`")
	sb.WriteString(`
   - max 1 `)
	sb.WriteString("`skills update`")
	sb.WriteString(`
   - ZERO `)
	sb.WriteString("`skills remove`")
	sb.WriteString(`
   - memory tool is READ-ONLY (only `)
	sb.WriteString("`get`/`list`")
	sb.WriteString(` succeed)
5. If nothing recurs strongly enough, do nothing — empty runs are fine.
6. End with `)
	sb.WriteString("`no-reply`")
	sb.WriteString(`.
`)
	return sb.String()
}

// formatCounts renders "name ×N, name ×N" sorted by count desc for stable output.
func formatCounts(m map[string]int) string {
	if len(m) == 0 {
		return "(none)"
	}
	type kv struct {
		k string
		v int
	}
	pairs := make([]kv, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = fmt.Sprintf("%s ×%d", p.k, p.v)
	}
	return strings.Join(parts, ", ")
}

// EnsureEvolutionTasks creates a daily system_evolve task for every active
// user that doesn't already have one. Called from main.go at startup.
func EnsureEvolutionTasks(db *store.Store, schedule string, lookback time.Duration) error {
	users, err := db.ActiveUserIDs(time.Now().Add(-lookback))
	if err != nil {
		return err
	}
	if len(users) == 0 {
		return nil
	}
	existing, err := db.ListTasksByType("system_evolve")
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(existing))
	for _, t := range existing {
		have[t.UserID] = true
	}
	for _, uid := range users {
		if uid == "" || have[uid] {
			continue
		}
		next, err := nextCronRun(schedule, time.Now())
		if err != nil {
			return fmt.Errorf("invalid evolve schedule %q: %w", schedule, err)
		}
		if err := db.CreateTask(&store.Task{
			Type:        "system_evolve",
			ChatID:      "",
			UserID:      uid,
			Description: "v1",
			Schedule:    schedule,
			NextRun:     next,
		}); err != nil {
			return err
		}
	}
	return nil
}
