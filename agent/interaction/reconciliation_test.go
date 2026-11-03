package interaction

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestPortableToolRecoveryEnforcesTheCompleteResultContract(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	var calls atomic.Int32
	executable := scriptTool{name: "write", direct: true, invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
		calls.Add(1)
		return tool.Result{}, errors.New("receipt was lost")
	}}
	tools := mustValue(NewToolSet(ToolSetConfig{Revision: "recovery-contract", Tools: []tool.Tool{executable}, DeferredTools: []tool.Tool{scriptTool{name: "hidden"}}}))
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return callResponse(requested("write", "call")), nil
	})
	program := compileTest(t, runtime, Config{Tools: tools, Model: CallModel(model)})
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
	question := mustValue(newInputRequest([]byte(`"Approve?"`), []byte(`{"type":"boolean"}`), []byte(`{"private":"state"}`)))
	for _, row := range []struct {
		name        string
		result      tool.Result
		disposition ResultDisposition
		advertised  []string
	}{
		{"invalid disposition", tool.Result{}, ResultInvalid, nil},
		{"malformed details", tool.Result{Output: tool.Output{Details: []byte(`{`)}}, ResultSucceeded, nil},
		{"invalid question", tool.Result{InputRequest: new(tool.InputRequest)}, ResultSucceeded, nil},
		{"question and output", tool.Result{InputRequest: &question, Output: tool.NewTextOutput("already completed")}, ResultSucceeded, nil},
		{"failed question", tool.Result{InputRequest: &question}, ResultFailed, nil},
		{"rejected question", tool.Result{InputRequest: &question}, ResultRejected, nil},
		{"question advertisement", tool.Result{InputRequest: &question}, ResultSucceeded, []string{"hidden"}},
		{"unknown advertisement", tool.Result{}, ResultSucceeded, []string{"missing"}},
		{"visible advertisement", tool.Result{}, ResultSucceeded, []string{"write"}},
		{"failure advertisement", tool.Result{}, ResultFailed, []string{"hidden"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			settlement, err := SettleToolResult(program, request, row.result, row.disposition, row.advertised)
			if !errors.Is(err, ErrInvalidProtocol) || settlement.Valid() {
				t.Fatal("invalid investigated result acquired settlement authority", settlement, err)
			}
		})
	}
	foreign := compileTest(t, runtime, Config{Tools: tools, Model: CallModel(model), MaxModelCalls: run.NewQuota(1)})
	for _, candidate := range []run.Program[Input, Output]{{}, foreign} {
		if settlement, err := SettleToolResult(candidate, request, tool.Result{}, ResultSucceeded, nil); !errors.Is(err, ErrInvalidProtocol) || settlement.Valid() {
			t.Fatal("foreign binding settled the effect", err)
		}
	}
	if settlement, err := SettleToolResult(program, run.EffectRequest{}, tool.Result{}, ResultSucceeded, nil); !errors.Is(err, ErrInvalidProtocol) || settlement.Valid() {
		t.Fatal("recovery fabricated a missing request", err)
	}
	if _, err := SettleModelResult(program, request, finalResponse("wrong variant")); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("tool effect accepted a model result", err)
	}
	if _, err := SettlePreparedContext(program, request, testInput().Messages); !errors.Is(err, ErrInvalidDispatcherConfig) {
		t.Fatal("unbound context processor accepted a recovery result", err)
	}
	for _, disposition := range []ResultDisposition{ResultSucceeded, ResultFailed, ResultRejected} {
		settlement := mustValue(SettleToolResult(program, request, tool.Result{}, disposition, nil))
		observed := mustValue(ToolSettlementFromOutcome(requested("write", "call"), settlement))
		if settlement.Status() != run.SettlementStatusSucceeded || observed.Result == nil || observed.Result.IsError != (disposition != ResultSucceeded) || len(observed.Result.Output.Content) != 0 {
			t.Fatal("empty, definite output changed its semantic disposition")
		}
	}
	result := tool.Result{Output: tool.NewTextOutput("verified once")}
	advertised := []string{"hidden", "hidden"}
	settlement := mustValue(SettleToolResult(program, request, result, ResultSucceeded, advertised))
	result.Output.Content[0].Text = "changed after validation"
	advertised[0] = "missing"
	retained := mustValue(decodeResult(settlement.Payload().JSON())).ToolResult.Completion
	if !slices.Equal(retained.AdvertisedToolNames, []string{"hidden"}) || retained.Output.Content[0].Text != "verified once" {
		t.Fatal("investigated evidence was not frozen at admission")
	}
	if err := handle.ResolveUnknownEffect(t.Context(), request.ID(), settlement); err != nil {
		t.Fatal(err)
	}
	output := awaitOutput(t, handle)
	if calls.Load() != 1 || len(output.DirectToolResults) != 1 || output.DirectToolResults[0].Output.Content[0].Text != "verified once" {
		t.Fatal("recovery repeated the external write or lost admitted evidence")
	}
}

func TestPortableModelRecoveryRejectsForeignOrUndeliverableEvidence(t *testing.T) {
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	var calls atomic.Int32
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		calls.Add(1)
		return nil, errors.New("model receipt was lost")
	})
	program := compileTest(t, runtime, Config{Model: CallModel(model), MaxResponseBytes: 512})
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
	if _, err := SettleToolResult(program, request, tool.Result{}, ResultSucceeded, nil); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatal("model effect accepted a tool result", err)
	}
	for _, row := range []struct {
		response *chat.Response
		failure  error
	}{
		{nil, ErrInvalidProtocol},
		{&chat.Response{}, ErrInvalidProtocol},
		{finalResponse(strings.Repeat("x", 513)), ErrModelResponseTooLarge},
	} {
		if settlement, err := SettleModelResult(program, request, row.response); !errors.Is(err, row.failure) || settlement.Valid() {
			t.Fatal("invalid or oversized model evidence became deliverable", err)
		}
	}
	foreign := compileTest(t, runtime, Config{Model: CallModel(model), MaxResponseBytes: 1024})
	for _, candidate := range []run.Program[Input, Output]{{}, foreign} {
		if settlement, err := SettleModelResult(candidate, request, finalResponse("done")); !errors.Is(err, ErrInvalidProtocol) || settlement.Valid() {
			t.Fatal("model recovery did not enforce exact configuration", err)
		}
		if settlement, err := SettlePreparedContext(candidate, request, testInput().Messages); !errors.Is(err, ErrInvalidProtocol) || settlement.Valid() {
			t.Fatal("context recovery did not enforce exact configuration", err)
		}
	}
	response := finalResponse("verified")
	settlement := mustValue(SettleModelResult(program, request, response))
	response.Output.Message.Parts[0].Text = "changed after validation"
	if err := handle.ResolveUnknownEffect(t.Context(), request.ID(), settlement); err != nil {
		t.Fatal(err)
	}
	if output := awaitOutput(t, handle); calls.Load() != 1 || output.ModelResponse.Text() != "verified" {
		t.Fatal("model recovery repeated work or retained caller mutation")
	}
}
