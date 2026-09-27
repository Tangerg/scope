package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type interruptedMutation struct {
	err error
}

func (i interruptedMutation) Write(context.Context, WriteRequest) (WriteResponse, error) {
	return WriteResponse{BytesWritten: 5}, i.err
}

func (i interruptedMutation) Edit(context.Context, EditRequest) (EditResponse, error) {
	return EditResponse{Replacements: 2}, i.err
}

func (i interruptedMutation) ApplyPatch(context.Context, ApplyPatchRequest) (ApplyPatchResponse, error) {
	return ApplyPatchResponse{Files: []PatchFileResponse{{Path: "first", Created: true, Hunks: 1}}, Hunks: 1}, i.err
}

func TestMutationErrorsPreserveAcknowledgementsWithoutInventingOutcome(t *testing.T) {
	backend := interruptedMutation{err: context.DeadlineExceeded}
	write, err := NewWriteTool(backend)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := NewEditTool(backend)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := NewApplyPatchTool(backend)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		tool      tool.Tool
		arguments string
		evidence  string
	}{
		{name: "write", tool: write, arguments: `{"path":"file","content":"hello"}`, evidence: `{"bytes_written":5}`},
		{name: "edit", tool: edit, arguments: `{"path":"file","old_string":"old","new_string":"new"}`, evidence: `{"replacements":2}`},
		{name: "patch", tool: patch, arguments: `{"patch":"opaque remote request"}`, evidence: `{"files":[{"path":"first","hunks":1,"created":true}],"hunks":1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := invokeTestTool(t.Context(), test.tool, test.arguments)
			if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(output, chat.ToolOutput{}) {
				t.Fatalf("interrupted call = %+v, %v", output, err)
			}
			if failure, found := errors.AsType[*tool.Failure](err); found {
				t.Fatalf("interrupted mutation became a completed failure: %v", failure)
			}
			callErr, found := errors.AsType[*tool.CallError](err)
			if !found || string(callErr.Evidence().Details) != test.evidence {
				t.Fatalf("mutation evidence = %v, want %s", err, test.evidence)
			}
		})
	}
}

func TestMutationToolsPreserveBackendOutcomeAuthority(t *testing.T) {
	failure, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindRejected, Output: chat.NewTextToolOutput("permission denied"), Cause: context.Canceled,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := interruptedMutation{err: errors.Join(ErrMutationRejected, failure)}
	write, err := NewWriteTool(backend)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := NewEditTool(backend)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := NewApplyPatchTool(backend)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		tool      tool.Tool
		arguments string
	}{
		{tool: write, arguments: `{"path":"file","content":"hello"}`},
		{tool: edit, arguments: `{"path":"file","old_string":"old","new_string":"new"}`},
		{tool: patch, arguments: `{"patch":"opaque remote request"}`},
	} {
		_, err := invokeTestTool(t.Context(), test.tool, test.arguments)
		got, found := errors.AsType[*tool.Failure](err)
		if !found || got != failure || got.Kind() != tool.FailureKindRejected || !reflect.DeepEqual(got.Output(), failure.Output()) {
			t.Fatalf("backend outcome changed: %v", err)
		}
		if errors.Is(err, context.Canceled) {
			t.Fatal("diagnostic cause escaped the definite outcome boundary")
		}
	}
}

func TestMutationToolPreservesExplicitEvidenceBeforeRejectionClassification(t *testing.T) {
	want := chat.NewTextToolOutput("backend response observed before rejection")
	callErr, err := tool.NewCallError(tool.CallErrorConfig{Cause: ErrMutationRejected, Evidence: want})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := NewWriteTool(interruptedMutation{err: callErr})
	if err != nil {
		t.Fatal(err)
	}
	_, err = invokeTestTool(t.Context(), executable, `{"path":"file","content":"hello"}`)
	got, found := errors.AsType[*tool.CallError](err)
	if !found || got != callErr || !reflect.DeepEqual(got.Evidence(), want) {
		t.Fatalf("explicit evidence was replaced by inferred outcome: %v", err)
	}
	if failure, definite := errors.AsType[*tool.Failure](err); definite {
		t.Fatalf("explicit evidence became a definite failure: %v", failure)
	}
}

func TestLocalMutationRejectionsRemainDefiniteToolFeedback(t *testing.T) {
	root := t.TempDir()
	path := writeTemp(t, root, "existing", "old\n")
	executor := mustLocalExecutor(t, root)
	write, err := NewWriteTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := NewEditTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := NewApplyPatchTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		tool      tool.Tool
		arguments string
	}{
		{name: "binary write", tool: write, arguments: `{"path":"new/dir/file","content":"bad\u0000content"}`},
		{name: "edit mismatch", tool: edit, arguments: `{"path":"existing","old_string":"stale","new_string":"new"}`},
		{name: "patch mismatch", tool: patch, arguments: `{"patch":"--- existing\n+++ existing\n@@ -1 +1 @@\n-stale\n+new\n"}`},
		{name: "malformed patch", tool: patch, arguments: `{"patch":"not a diff"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := invokeTestTool(t.Context(), test.tool, test.arguments)
			failure, found := errors.AsType[*tool.Failure](err)
			if !found || failure.Kind() != tool.FailureKindFailed || !errors.Is(failure.Cause(), ErrMutationRejected) {
				t.Fatalf("pre-commit rejection = %v", err)
			}
			if text, _ := failure.Output().Text(); text == "" {
				t.Fatal("model cannot correct an empty rejection result")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "old\n" {
				t.Fatalf("rejection changed existing file: %q, %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(root, "new")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejection created a directory: %v", err)
			}
		})
	}
}
