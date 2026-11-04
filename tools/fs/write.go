package fs

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"

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

type WriteTool struct {
	function toolcontract.Func[WriteRequest, WriteResponse]
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
	t.function = typed
	return t, nil
}

func (w *WriteTool) write(ctx context.Context, req WriteRequest) (WriteResponse, error) {
	res, err := w.executor.Write(ctx, req)
	if err != nil {
		return WriteResponse{}, mutationError("fs.write", res, err)
	}
	return res, nil
}

func (w *WriteTool) Definition() toolcontract.Definition { return w.function.Definition() }

func (w *WriteTool) InputValidator() func([]byte) error { return w.function.InputValidator() }

func (w *WriteTool) Call(ctx context.Context, invocation toolcontract.Invocation) (toolcontract.Result, error) {
	return w.function.Call(ctx, invocation)
}

func (w *WriteRequest) UnmarshalJSON(data []byte) error {
	if w == nil {
		return ErrInvalidInput
	}
	type wireRequest WriteRequest
	var decoded *wireRequest
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	if decoded == nil {
		return ErrInvalidInput
	}
	candidate := WriteRequest(*decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*w = candidate
	return nil
}
