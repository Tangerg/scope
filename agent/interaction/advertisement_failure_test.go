package interaction

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestAdvertisementDefectPreservesCompletedExternalWork(t *testing.T) {
	for _, row := range []struct {
		name      string
		advertise func(context.Context, tool.Invocation, tool.Result) ([]string, error)
	}{
		{"unbound declaration", func(context.Context, tool.Invocation, tool.Result) ([]string, error) { return []string{"missing"}, nil }},
		{"visible tool is not deferred", func(context.Context, tool.Invocation, tool.Result) ([]string, error) { return []string{"write"}, nil }},
		{"callback failure", func(context.Context, tool.Invocation, tool.Result) ([]string, error) {
			return nil, errors.New("invalid declaration")
		}},
		{"callback panic", func(context.Context, tool.Invocation, tool.Result) ([]string, error) { panic("invalid declaration") }},
	} {
		t.Run(row.name, func(t *testing.T) {
			store := run.NewMemoryStore()
			runtime := conformancetest.Runtime(t, run.Config{Store: store})
			calls := 0
			tools := toolSetTest(t, scriptTool{name: "write", advertise: row.advertise, invoke: func(context.Context, tool.Invocation) (tool.Result, error) {
				calls++
				return tool.Result{Output: tool.NewTextOutput("committed write")}, nil
			}})
			program := compileTest(t, runtime, Config{Tools: tools, Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				return callResponse(requested("write", "write-once")), nil
			}))})
			handle := startInteraction(t, runtime, program)
			result, err := handle.Await(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			failure, ok := result.Termination().Failure()
			if !ok || failure.Kind() != run.FailureKindContract || failure.Code() != failureCodeInteractionToolAdvertisementInvalid || calls != 1 {
				t.Fatalf("termination=%+v calls=%d", result.Termination(), calls)
			}
			log := completedLog(t, store, handle.ID())
			rounds := mustValue(SettledResults(program, log))
			if len(rounds) != 1 || !rounds[0].Complete() || rounds[0].Entries()[0].Disposition != ResultSucceeded {
				t.Fatal("successful external result retracted by declaration defect")
			}
			for _, entry := range log.Entries {
				if settled, ok := entry.Record.(run.EffectSettled); ok && settled.Outcome.Status() == run.SettlementStatusUnknown {
					t.Fatal("local declaration defect created external uncertainty")
				}
			}
			recovered := conformancetest.Runtime(t, run.Config{Store: store})
			restored, err := recovered.Recover(t.Context(), program, handle.ID())
			if err != nil {
				t.Fatal(err)
			}
			replayed, err := restored.Await(t.Context())
			if err != nil || replayed.Termination().Status() != run.StatusFailed || calls != 1 {
				t.Fatal("recovery repeated completed external work", err)
			}
		})
	}
}
