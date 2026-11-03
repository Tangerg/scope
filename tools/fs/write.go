package fs

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

type WriteRequest struct {
	Path    string `json:"path" jsonschema:"minLength=1" jsonschema_description:"File path, absolute or relative to the workspace root. Parent directories are created automatically."`
	Content string `json:"content" jsonschema_description:"Complete text content for the file. Existing content is replaced. Must not contain NUL bytes."`
}

type WriteResponse struct {
	BytesWritten int `json:"bytes_written"`
}

var _ toolcontract.Tool = (*WriteTool)(nil)

type WriteTool struct {
	toolcontract.Func[WriteRequest, WriteResponse]
	executor Writer
}

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
	t.Func = typed
	return t, nil
}

func (w *WriteTool) write(ctx context.Context, req WriteRequest) (WriteResponse, error) {
	res, err := w.executor.Write(ctx, req)
	if err != nil {
		return WriteResponse{}, mutationError("fs.write", res, err)
	}
	return res, nil
}
