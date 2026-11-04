package fs

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

const editToolName = "edit"

// EditRequest drives one atomic exact-text replacement in the executor.
// Whitespace is significant, including indentation and string literal content.
type EditRequest struct {
	Path       string `json:"path" jsonschema:"minLength=1" jsonschema_description:"File path, absolute or relative to the workspace root."`
	OldString  string `json:"old_string" jsonschema:"required" jsonschema_description:"Exact text to find, copied verbatim from the file (the read tool returns raw text — there is no line-number prefix to strip). Keep it to the few unique lines needed; fails when the match is not unique unless replace_all=true."`
	NewString  string `json:"new_string" jsonschema:"required" jsonschema_description:"Replacement text. Preserve the surrounding indentation exactly. Must differ from old_string and must not contain NUL bytes."`
	ReplaceAll bool   `json:"replace_all,omitzero" jsonschema_description:"Replace every occurrence. Default false. Use this for renaming a symbol across the file."`
}

func (e EditRequest) Validate() error {
	if e.Path == "" {
		return ErrEmptyPath
	}
	return e.operation().validate()
}

func (e EditRequest) operation() editOperation {
	return editOperation{OldString: e.OldString, NewString: e.NewString, ReplaceAll: e.ReplaceAll}
}

type EditResponse struct {
	Replacements int `json:"replacements"`
}

var _ toolcontract.Tool = (*EditTool)(nil)

// editFunc is embedded under an unexported name so the tool keeps its
// Func private while promoting the Tool methods.
type editFunc = toolcontract.Func[EditRequest, EditResponse]

type EditTool struct {
	editFunc
	executor Editor
}

func NewEditTool(executor Editor) (*EditTool, error) {
	if lo.IsNil(executor) {
		return nil, ErrNilExecutor
	}
	t := &EditTool{executor: executor}
	typed, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{
			Name: editToolName,
			Description: "Replace exact text in one file. Read the file first so old_string reflects its current contents. " +
				"Copy old_string verbatim from read output and keep it to the few unique lines needed. " +
				"Set replace_all=true only when every occurrence in this file should change.",
		},
		t.edit,
	)
	if err != nil {
		return nil, fmt.Errorf("fs.NewEditTool: %w", err)
	}
	t.editFunc = typed
	return t, nil
}

func (e *EditTool) edit(ctx context.Context, req EditRequest) (EditResponse, error) {
	res, err := e.executor.Edit(ctx, req)
	if err != nil {
		return EditResponse{}, mutationError("fs.edit", res, err)
	}
	return res, nil
}
