package fs

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

// EditRequest drives one atomic exact-text replacement in the executor.
// Whitespace is significant, including indentation and string literal content.
type EditRequest struct {
	Path       string `json:"path" jsonschema:"minLength=1" jsonschema_description:"File path, absolute or relative to the workspace root."`
	OldString  string `json:"old_string" jsonschema:"required" jsonschema_description:"Exact text to find, copied verbatim from the file (the read tool returns raw text — there is no line-number prefix to strip). Keep it to the few unique lines needed; fails when the match is not unique unless replace_all=true."`
	NewString  string `json:"new_string" jsonschema:"required" jsonschema_description:"Replacement text. Preserve the surrounding indentation exactly. Must differ from old_string and must not contain NUL bytes."`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema_description:"Replace every occurrence. Default false. Use this for renaming a symbol across the file."`
}

// EditResponse makes replacement cardinality observable to the model.
type EditResponse struct {
	Replacements int `json:"replacements"`
}

var _ toolcontract.Tool = (*EditTool)(nil)

// EditTool is the thin LLM-facing adapter for [Editor.Edit]. The
// executor owns validation and atomic replacement under the same contract.
type EditTool struct {
	executor Editor
	typed    toolcontract.Func[EditRequest, EditResponse]
}

// NewEditTool retains only the atomic edit capability, not a full filesystem
// backend.
func NewEditTool(executor Editor) (*EditTool, error) {
	if lo.IsNil(executor) {
		return nil, ErrNilExecutor
	}
	t := &EditTool{executor: executor}
	typed, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{
			Name: "edit",
			Description: "Replace exact text in one file. Read the file first so old_string reflects its current contents. " +
				"Copy old_string verbatim from read output and keep it to the few unique lines needed. " +
				"Set replace_all=true only when every occurrence in this file should change.",
		},
		t.edit,
	)
	if err != nil {
		return nil, fmt.Errorf("fs.NewEditTool: %w", err)
	}
	t.typed = typed
	return t, nil
}

func (e *EditTool) Definition() chat.ToolDefinition {
	return e.typed.Definition()
}

func (e *EditTool) Call(ctx context.Context, invocation toolcontract.Invocation) (chat.ToolOutput, error) {
	return e.typed.Call(ctx, invocation)
}

func (e *EditTool) edit(ctx context.Context, req EditRequest) (EditResponse, error) {
	res, err := e.executor.Edit(ctx, req)
	if err != nil {
		return EditResponse{}, fmt.Errorf("fs.edit: %w", err)
	}
	return res, nil
}

// Unwrap exposes the typed input contract through tool decorators.
func (e *EditTool) Unwrap() toolcontract.Tool { return e.typed }
