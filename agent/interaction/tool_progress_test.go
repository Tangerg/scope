package interaction

import (
	"context"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestToolProgressIsPublishedAsDeltasOfItsEffect(t *testing.T) {
	deltas := make(chan run.Delta, 8)
	runtime := conformancetest.Runtime(t, run.Config{Deltas: deltas})
	executable := scriptTool{name: "index", direct: true, invoke: func(ctx context.Context, _ tool.Invocation) (tool.Result, error) {
		if err := tool.ReportProgress(ctx, tool.Progress{Completed: 1, Total: 2, Message: "halfway"}); err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Output: tool.NewTextOutput("indexed")}, nil
	}}
	program := compileTest(t, runtime, Config{Tools: toolSetTest(t, executable), Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("index", "call")), nil
	}))})
	handle := startInteraction(t, runtime, program)
	awaitOutput(t, handle)
	delta := <-deltas
	progress, err := ParseToolProgress(delta.Payload())
	if err != nil {
		t.Fatal(err)
	}
	if delta.ProcessID() != handle.ID() || progress != (tool.Progress{Completed: 1, Total: 2, Message: "halfway"}) {
		t.Fatalf("delta=%v progress=%+v", delta.ProcessID(), progress)
	}
	if _, err := ParseToolProgress([]byte(`{"progress":{"completed":3,"total":2}}`)); err == nil {
		t.Fatal("inconsistent progress decoded")
	}
}
