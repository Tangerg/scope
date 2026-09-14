package fs

import "testing"

func mustLocalExecutor(t testing.TB, root string) *LocalExecutor {
	t.Helper()
	executor, err := NewLocalExecutor(root)
	if err != nil {
		t.Fatalf("NewLocalExecutor(%q): %v", root, err)
	}
	t.Cleanup(func() {
		if err := executor.Close(); err != nil {
			t.Error(err)
		}
	})
	return executor
}

func mustReadTool(t testing.TB, executor Reader) *ReadTool {
	t.Helper()
	read, err := NewReadTool(executor)
	if err != nil {
		t.Fatalf("NewReadTool: %v", err)
	}
	return read
}

func mustWriteTool(t testing.TB, executor Writer) *WriteTool {
	t.Helper()
	write, err := NewWriteTool(executor)
	if err != nil {
		t.Fatalf("NewWriteTool: %v", err)
	}
	return write
}

func mustEditTool(t testing.TB, executor Editor) *EditTool {
	t.Helper()
	edit, err := NewEditTool(executor)
	if err != nil {
		t.Fatalf("NewEditTool: %v", err)
	}
	return edit
}

func mustApplyPatchTool(t testing.TB, executor PatchApplier) *ApplyPatchTool {
	t.Helper()
	applyPatch, err := NewApplyPatchTool(executor)
	if err != nil {
		t.Fatalf("NewApplyPatchTool: %v", err)
	}
	return applyPatch
}

func mustGlobTool(t testing.TB, executor Globber) *GlobTool {
	t.Helper()
	glob, err := NewGlobTool(executor)
	if err != nil {
		t.Fatalf("NewGlobTool: %v", err)
	}
	return glob
}

func mustGrepTool(t testing.TB, executor Grepper) *GrepTool {
	t.Helper()
	grep, err := NewGrepTool(executor)
	if err != nil {
		t.Fatalf("NewGrepTool: %v", err)
	}
	return grep
}
