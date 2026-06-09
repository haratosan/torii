package agent

import "context"

// EventSink receives intermediate events from the agent loop so that
// streaming clients (e.g. the torii TUI) can show tool calls live instead
// of getting opaque output at the end. nil sinks disable the feature —
// Telegram, scheduler, and MQTT callers all pass nil and see no behavior
// change.
type EventSink interface {
	ToolCall(ctx context.Context, name, arguments string)
	ToolResult(ctx context.Context, name, output, errStr string)
}
