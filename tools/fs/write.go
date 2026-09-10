package fs

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

// WriteRequest replaces one complete text file beneath the backend authority.
type WriteRequest struct {
	Path    string `json:"path" jsonschema:"minLength=1" jsonschema_description:"File path, absolute or relative to the workspace root. Parent directories are created automatically."`
	Content string `json:"content" jsonschema_description:"Complete text content for the file. Existing content is replaced. Must not contain NUL bytes."`
}

// WriteResponse makes the committed byte count observable to the model.
type WriteResponse struct {
	BytesWritten int `json:"bytes_written"`
}

var _ toolcontract.Tool = (*WriteTool)(nil)

// WriteTool is the model-facing adapter for the narrow Writer port.
type WriteTool struct {
	executor Writer
	typed    toolcontract.Func[WriteRequest, WriteResponse]
}

// NewWriteTool requires write authority explicitly and derives one stable schema.
func NewWriteTool(executor Writer) (*WriteTool, error) {
	if lo.IsNil(executor) {
		return nil, ErrNilExecutor
	}
	t := &WriteTool{executor: executor}
	typed, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{
			Name: "write",
			Description: "Create a file or replace the complete contents of an existing file. " +
				"Use edit for a targeted change to an existing file. Parent directories are created automatically.",
		},
		t.write,
	)
	if err != nil {
		return nil, fmt.Errorf("fs.NewWriteTool: %w", err)
	}
	t.typed = typed
	return t, nil
}

func (w *WriteTool) Definition() chat.ToolDefinition {
	return w.typed.Definition()
}

func (w *WriteTool) Call(ctx context.Context, invocation toolcontract.Invocation) (chat.ToolOutput, error) {
	return w.typed.Call(ctx, invocation)
}

func (w *WriteTool) write(ctx context.Context, req WriteRequest) (WriteResponse, error) {
	res, err := w.executor.Write(ctx, req)
	if err != nil {
		return WriteResponse{}, fmt.Errorf("fs.write: %w", err)
	}
	return res, nil
}
