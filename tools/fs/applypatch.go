package fs

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

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
	Created bool   `json:"created,omitzero"`
	Deleted bool   `json:"deleted,omitzero"`
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
				"Express moves with Git rename metadata. The patch must match exactly, and an existing rename destination is never overwritten." + `

Prefer edit when the change is in one place: it takes the text to replace and
needs no line numbers and no counts. Reach for apply_patch when one call has to
change several files or several places at once, or create or delete a file.

The patch argument is plain Git unified diff text, without Markdown fences or
*** Begin Patch / *** Update File markers. Relative paths use the backend
filesystem root. Every hunk needs @@ -oldStart,oldCount +newStart,newCount @@: count
context and removed lines on the old side, context and added lines on the new
side. Prefix each context line with a space, removed line with -, and added
line with +. Include the final newline. Keep hunks small enough to count exactly.

Create notes.txt with one line:
` + "```diff\n" + `diff --git a/notes.txt b/notes.txt
new file mode 100644
--- /dev/null
+++ b/notes.txt
@@ -0,0 +1,1 @@
+first
` + "```\n" + `
After reading notes.txt, replace its line:
` + "```diff\n" + `diff --git a/notes.txt b/notes.txt
--- a/notes.txt
+++ b/notes.txt
@@ -1,1 +1,1 @@
-first
+second
` + "```\n" + `
After reading notes.txt, delete it:
` + "```diff\n" + `diff --git a/notes.txt b/notes.txt
deleted file mode 100644
--- a/notes.txt
+++ /dev/null
@@ -1,1 +0,0 @@
-second
` + "```",
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

// MutationPaths returns the sorted, unique file endpoints named by an invocation.
// A rename includes both its source and destination; /dev/null is never a target.
// Paths are lexically cleaned with Git's a/ and b/ prefixes removed. Relative
// paths remain relative to the backend root and absolute paths remain absolute.
//
// It uses the same patch parser and operation validation as LocalExecutor and
// performs no I/O. A successful query does not establish filesystem authority,
// target identity, hunk applicability, or execution success. Hosts resolve these
// prospective paths for policy and locking; ApplyPatchResponse alone acknowledges
// actual effects. Invalid arguments or unsupported patches return no paths.
func (a *ApplyPatchTool) MutationPaths(arguments []byte) ([]string, error) {
	var request ApplyPatchRequest
	if err := jsonv2.Unmarshal(arguments, &request, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("fs.apply_patch: decode mutation paths: %w", err)
	}
	parsed, err := parseUnifiedPatch(request.Patch)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, file := range parsed.files {
		paths = append(paths, file.touches()...)
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

func (a *ApplyPatchTool) apply(ctx context.Context, req ApplyPatchRequest) (ApplyPatchResponse, error) {
	res, err := a.executor.ApplyPatch(ctx, req)
	if err != nil {
		cause := fmt.Errorf("fs.apply_patch: %w", err)
		encoded, encodeErr := jsonv2.Marshal(res)
		if encodeErr != nil {
			return ApplyPatchResponse{}, errors.Join(cause, encodeErr)
		}
		output := chat.NewTextToolOutput(fmt.Sprintf("%s\nAcknowledged file mutations: %s", cause, encoded))
		output.Details = encoded
		failure, failureErr := toolcontract.NewFailure(toolcontract.FailureConfig{Kind: toolcontract.FailureKindFailed, Cause: cause, Output: output})
		if failureErr != nil {
			return ApplyPatchResponse{}, errors.Join(cause, failureErr)
		}
		return ApplyPatchResponse{}, failure
	}
	return res, nil
}

func (a *ApplyPatchTool) Unwrap() toolcontract.Tool { return a.typed }
