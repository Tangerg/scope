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
	parsed, err := ParseDelta(delta.Payload())
	if err != nil {
		t.Fatal(err)
	}
	progress, ok := parsed.(ToolProgressDelta)
	if !ok || delta.ProcessID() != handle.ID() || progress.Progress() != (tool.Progress{Completed: 1, Total: 2, Message: "halfway"}) {
		t.Fatalf("delta=%v parsed=%#v", delta.ProcessID(), parsed)
	}
}

func TestParseDeltaAdmitsExactlyOneInteractionVariant(t *testing.T) {
	for name, raw := range map[string]string{
		"inconsistent progress": `{"progress":{"completed":3,"total":2}}`,
		"both variants":         `{"response_delta":{"parts":[{"kind":"text","text":"hi"}]},"progress":{"completed":1,"total":2}}`,
		"neither variant":       `{}`,
		"foreign dispatcher":    `{"elapsed":"1s"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if parsed, err := ParseDelta(mustValue(run.ParsePayload([]byte(raw)))); err == nil {
				t.Fatalf("ParseDelta admitted %s as %#v", raw, parsed)
			}
		})
	}
}
