package fs

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestGlobToolReturnsBoundedWorkspaceMatches(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "a.go", "package a")
	writeTemp(t, root, "b.go", "package b")
	writeTemp(t, root, "ignored.txt", "text")
	executable := mustGlobTool(t, mustLocalExecutor(t, root))
	output, err := invokeTestTool(t.Context(), executable, `{"pattern":"**/*.go","max_results":1}`)
	if err != nil {
		t.Fatal(err)
	}
	var response GlobResponse
	if err := jsonv2.Unmarshal(output.Details, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Paths) != 1 || !slices.Contains([]string{"a.go", "b.go"}, response.Paths[0]) || !response.Truncated {
		t.Fatalf("bounded glob result = %#v", response)
	}
	if _, err := invokeTestTool(t.Context(), executable, `{"pattern":"**/*","path":"../"}`); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("glob outside the executor root = %v", err)
	}
}

func TestReadToolPreservesOversizedLineIdentity(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "large.txt", "ok\n"+strings.Repeat("x", defaultReadLineBytes+1))
	_, err := invokeTestTool(t.Context(), mustReadTool(t, mustLocalExecutor(t, root)), `{"path":"large.txt"}`)
	if !errors.Is(err, ErrLineTooLarge) || LineNumber(err) != 2 {
		t.Fatalf("line limit error = %v, line %d", err, LineNumber(err))
	}
}
