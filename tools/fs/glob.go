package fs

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

// GlobRequest narrows the executor's immutable authority to a relative subtree;
// Path can never replace or broaden that root.
type GlobRequest struct {
	Pattern    string `json:"pattern" jsonschema:"minLength=1" jsonschema_description:"Doublestar path pattern, such as **/*.go or src/**/*.ts."`
	Path       string `json:"path,omitempty" jsonschema_description:"Directory to search under. Defaults to the workspace root."`
	IgnoreCase bool   `json:"ignore_case,omitempty" jsonschema_description:"Match path components case-insensitively. Default false."`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"minimum=1,maximum=1000" jsonschema_description:"Maximum paths to return. Defaults to 100 and cannot exceed 1000."`
}

// GlobResponse distinguishes a complete match set from a bounded prefix.
type GlobResponse struct {
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated,omitempty"`
}

var _ toolcontract.Tool = (*GlobTool)(nil)

// GlobTool is the model-facing adapter for the narrow Globber port.
type GlobTool struct {
	executor Globber
	typed    toolcontract.Func[GlobRequest, GlobResponse]
}

// NewGlobTool requires path-search authority explicitly.
func NewGlobTool(executor Globber) (*GlobTool, error) {
	if lo.IsNil(executor) {
		return nil, ErrNilExecutor
	}
	t := &GlobTool{executor: executor}
	typed, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{
			Name: "glob",
			Description: "List file paths matching a doublestar pattern. Use patterns such as **/*.go or src/**/*.ts. " +
				"Use grep to search file contents.",
		},
		t.glob,
	)
	if err != nil {
		return nil, fmt.Errorf("fs.NewGlobTool: %w", err)
	}
	t.typed = typed
	return t, nil
}

func (g *GlobTool) Definition() chat.ToolDefinition {
	return g.typed.Definition()
}

// ConcurrencyPolicy declares independent calls to the concurrent-safe backend.
func (g *GlobTool) ConcurrencyPolicy() func(toolcontract.Invocation) (string, bool) {
	return func(toolcontract.Invocation) (string, bool) { return "", true }
}

func (g *GlobTool) Call(ctx context.Context, invocation toolcontract.Invocation) (chat.ToolOutput, error) {
	return g.typed.Call(ctx, invocation)
}

func (g *GlobTool) glob(ctx context.Context, req GlobRequest) (GlobResponse, error) {
	res, err := g.executor.Glob(ctx, req)
	if err != nil {
		return GlobResponse{}, fmt.Errorf("fs.glob: %w", err)
	}
	return res, nil
}

// Unwrap exposes the typed input contract through tool decorators.
func (g *GlobTool) Unwrap() toolcontract.Tool { return g.typed }
