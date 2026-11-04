package fs

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/tools/internal/toolinput"

	"github.com/samber/lo"

	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/textread"
)

const writeToolName = "write"

type WriteRequest struct {
	Path    string `json:"path" jsonschema:"minLength=1" jsonschema_description:"File path, absolute or relative to the workspace root. Parent directories are created automatically."`
	Content string `json:"content" jsonschema_description:"Complete text content for the file. Existing content is replaced. Must not contain NUL bytes."`
}

func (w WriteRequest) Validate() error {
	return toolinput.Rejection(w.validate())
}

func (w WriteRequest) validate() error {
	if w.Path == "" {
		return ErrEmptyPath
	}
	if err := textread.Validate([]byte(w.Content)); err != nil {
		return ErrBinaryFile
	}
	return nil
}

type WriteResponse struct {
	BytesWritten int `json:"bytes_written"`
}

var _ toolcontract.Tool = (*WriteTool)(nil)

// writeFunc is embedded under an unexported name so the tool keeps its
// Func private while promoting the Tool methods.
type writeFunc = toolcontract.Func[WriteRequest, WriteResponse]

type WriteTool struct {
	writeFunc
	executor Writer
}

func NewWriteTool(executor Writer) (*WriteTool, error) {
	if lo.IsNil(executor) {
		return nil, ErrNilExecutor
	}
	t := &WriteTool{executor: executor}
	typed, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{
			Name: writeToolName,
			Description: "Create a file or replace the complete contents of an existing file. " +
				"Use edit for a targeted change to an existing file. Parent directories are created automatically.",
		},
		t.write,
	)
	if err != nil {
		return nil, fmt.Errorf("fs.NewWriteTool: %w", err)
	}
	t.writeFunc = typed
	return t, nil
}

func (w *WriteTool) write(ctx context.Context, req WriteRequest) (WriteResponse, error) {
	res, err := w.executor.Write(ctx, req)
	if err != nil {
		return WriteResponse{}, mutationError("fs.write", res, err)
	}
	return res, nil
}
