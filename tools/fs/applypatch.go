package fs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

// ApplyPatchRequest applies a Git-compatible unified diff. The local executor
// supports create, modify, delete, and Git rename patches, which makes a
// coordinated refactor one call.
type ApplyPatchRequest struct {
	Patch string `json:"patch" jsonschema:"minLength=1" jsonschema_description:"Git-compatible unified diff. Supports create, modify, delete, and rename operations; express moves with Git rename metadata."`
}

// ApplyPatchResponse reports acknowledged file mutations even when ApplyPatch
// returns an error. Files are listed in commit order. An interrupted move is
// reported as a created destination until the source has actually been removed.
type ApplyPatchResponse struct {
	Files []PatchFileResponse `json:"files"`
	Hunks int                 `json:"hunks"`
}

// PatchFileResponse preserves create, delete, and move identity separately.
// LocalExecutor reports paths relative to its authority root, even when patch
// headers use absolute paths.
type PatchFileResponse struct {
	// Path is where the file ended up.
	Path    string `json:"path"`
	Hunks   int    `json:"hunks"`
	Created bool   `json:"created,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
	// MovedFrom is the path the file left, set only for a move. Path alone would
	// say a file exists somewhere new without saying which one stopped existing.
	MovedFrom string `json:"moved_from,omitempty"`
}

var _ toolcontract.Tool = (*ApplyPatchTool)(nil)

// ApplyPatchTool preserves both complete and partial PatchApplier outcomes.
type ApplyPatchTool struct {
	executor PatchApplier
	typed    toolcontract.Func[ApplyPatchRequest, ApplyPatchResponse]
}

// NewApplyPatchTool requires patch authority explicitly and derives one stable
// tool schema.
func NewApplyPatchTool(executor PatchApplier) (*ApplyPatchTool, error) {
	if lo.IsNil(executor) {
		return nil, ErrNilExecutor
	}
	t := &ApplyPatchTool{executor: executor}
	typed, err := toolcontract.NewFunc(
		toolcontract.FuncConfig{
			Name: "apply_patch",
			Description: "Apply one Git-compatible unified diff across one or more files, including create, modify, delete, and rename operations. " +
				"Read existing files first so patch hunks reflect their current contents. Group coordinated multi-file changes in one patch. " +
				"Express moves with Git rename metadata. The patch must match exactly, and an existing rename destination is never overwritten.",
		},
		t.apply,
	)
	if err != nil {
		return nil, fmt.Errorf("fs.NewApplyPatchTool: %w", err)
	}
	t.typed = typed
	return t, nil
}

func (a *ApplyPatchTool) Definition() chat.ToolDefinition {
	return a.typed.Definition()
}

func (a *ApplyPatchTool) Call(ctx context.Context, invocation toolcontract.Invocation) (chat.ToolOutput, error) {
	return a.typed.Call(ctx, invocation)
}

func (a *ApplyPatchTool) apply(ctx context.Context, req ApplyPatchRequest) (ApplyPatchResponse, error) {
	res, err := a.executor.ApplyPatch(ctx, req)
	if err != nil {
		cause := fmt.Errorf("fs.apply_patch: %w", err)
		encoded, encodeErr := json.Marshal(res)
		if encodeErr != nil {
			return ApplyPatchResponse{}, errors.Join(cause, encodeErr)
		}
		output := chat.NewTextToolOutput(fmt.Sprintf("%s\nAcknowledged file mutations: %s", cause, encoded))
		output.Details = encoded
		failure, failureErr := toolcontract.NewFailure(cause, output)
		if failureErr != nil {
			return ApplyPatchResponse{}, errors.Join(cause, failureErr)
		}
		return ApplyPatchResponse{}, failure
	}
	return res, nil
}
