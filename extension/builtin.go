package extension

import "context"

// BuiltinTool runs in-process instead of as a subprocess.
type BuiltinTool struct {
	Def     Manifest
	Handler func(ctx context.Context, req ExtRequest) (*ExtResponse, error)
}
