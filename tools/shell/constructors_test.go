package shell

import "testing"

func mustLocalExecutor(t testing.TB, config LocalConfig) *LocalExecutor {
	t.Helper()
	executor, err := NewLocalExecutor(config)
	if err != nil {
		t.Fatalf("NewLocalExecutor: %v", err)
	}
	return executor
}

func mustTool(t testing.TB, executor Executor) *Tool {
	t.Helper()
	tool, err := NewTool(Config{Executor: executor})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	return tool
}
