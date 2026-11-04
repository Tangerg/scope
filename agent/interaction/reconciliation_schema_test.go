package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type structuredRecoveryTool struct{ scriptTool }

func (s structuredRecoveryTool) Definition() tool.Definition {
	definition := s.scriptTool.Definition()
	definition.OutputSchema = json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"],"additionalProperties":false}`)
	return definition
}

func TestRecoveryEnforcesTheBoundSuccessSchema(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	var calls atomic.Int32
	invalid := tool.Result{Output: tool.Output{Details: json.RawMessage(`{"id":"not-an-integer"}`)}}
	executable := structuredRecoveryTool{scriptTool: scriptTool{name: "write", direct: true, invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		calls.Add(1)
		return invalid, nil
	}}}
	program := compileTest(t, runtime, Config{Tools: toolSetTest(t, executable), Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("write", "call")), nil
	}))})
	handle := startInteraction(t, runtime, program)
	var request run.EffectRequest
	awaitLog(t, store, handle.ID(), func(log run.Log) bool {
		view := mustValue(log.Inspect())
		process, _ := view.Process(handle.ID())
		if len(process.PendingEffects) == 1 && process.PendingEffects[0].Outcome.Status() == run.SettlementStatusUnknown {
			request = process.PendingEffects[0].Request
			return true
		}
		return false
	})
	settlement, err := SettleToolResult(program, request, invalid, ResultSucceeded, nil)
	if !errors.Is(err, ErrInvalidProtocol) || !errors.Is(err, tool.ErrInvalidResult) || settlement.Valid() {
		t.Fatalf("invalid success acquired settlement authority: %v, %v", settlement, err)
	}
	for _, disposition := range []ResultDisposition{ResultFailed, ResultRejected} {
		failureSettlement, failureErr := SettleToolResult(program, request, tool.Result{Output: tool.NewTextOutput("public diagnostic")}, disposition, nil)
		if failureErr != nil || !failureSettlement.Valid() {
			t.Fatalf("failure diagnostic acquired a success schema obligation: %v", failureErr)
		}
	}
	question := mustValue(newInputRequest([]byte(`"Approve?"`), []byte(`{"type":"boolean"}`), []byte(`{"private":"state"}`)))
	if pendingSettlement, pendingErr := SettleToolResult(program, request, tool.Result{InputRequest: &question}, ResultSucceeded, nil); pendingErr != nil || !pendingSettlement.Valid() {
		t.Fatalf("an unfinished call acquired a success schema obligation: %v", pendingErr)
	}
	valid := tool.Result{Output: tool.Output{Details: json.RawMessage(`{"id":7}`)}}
	settlement = mustValue(SettleToolResult(program, request, valid, ResultSucceeded, nil))
	if err := handle.ResolveUnknownEffect(t.Context(), request.ID(), settlement); err != nil {
		t.Fatal(err)
	}
	output := awaitOutput(t, handle)
	if calls.Load() != 1 || len(output.DirectToolResults) != 1 || string(output.DirectToolResults[0].Output.Details) != `{"id":7}` {
		t.Fatal("recovery repeated the call or discarded its validated evidence")
	}
}
