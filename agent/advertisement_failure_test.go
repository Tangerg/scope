package agent

import (
	"github.com/Tangerg/scope/agent/run/runtest"

	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type completedToolObserver struct{ settled chan ToolSettlement }

func (*completedToolObserver) OnToolStarted(context.Context, ToolInvocation) {}
func (c *completedToolObserver) OnToolSettled(_ context.Context, _ ToolInvocation, settlement ToolSettlement) {
	c.settled <- settlement
}

func TestAdvertisementDefectPreservesCompletedCallThroughRestore(t *testing.T) {
	for _, defect := range []string{"unbound name", "callback error", "callback panic"} {
		t.Run(defect, func(t *testing.T) {
			var calls atomic.Int32
			inner, err := tool.NewFunc(tool.FuncConfig{Name: "active", Description: "Complete work before declaring Tools."}, func(context.Context, struct{}) (string, error) {
				calls.Add(1)
				return "completed write", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			executable := &advertisingTestTool{Tool: inner}
			switch defect {
			case "unbound name":
				executable.declare = []string{"missing"}
			case "callback error":
				executable.failure = errors.New("declaration unavailable")
			case "callback panic":
				executable.panicValue = "invalid declaration projection"
			}
			observer := &completedToolObserver{settled: make(chan ToolSettlement, 1)}
			tools, err := NewToolSet(ToolSetConfig{
				Name: "advertisement.failure", Description: "Preserve completed external work.", Tools: []tool.Tool{executable}, Observer: observer,
				ImplementationDigest: run.ComputeDigest([]byte("advertisement-failure-tools")),
				ConfigurationDigest:  run.ComputeDigest([]byte("advertisement-failure-config")),
			})
			if err != nil {
				t.Fatal(err)
			}
			store := run.NewMemoryStore()
			finished := make(chan run.SettlementStatus, 1)
			engine, err := run.New(t.Context(), run.Config{Store: store, EventListeners: []run.EventListener{
				run.EventListenerFunc(func(_ context.Context, event run.Event) {
					if fact, ok := event.EffectFinished(); ok && fact.Target() == run.EffectTargetDispatcher {
						finished <- fact.SettlementStatus()
					}
				}),
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			input, err := run.EncodePayload(toolCall{Call: chat.ToolCall{ID: "write", Name: "active", Arguments: `{}`}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runtest.Observe(t.Context(), engine, tools.Program(), input)
			if err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if !failed || failure.Kind() != run.FailureKindContract || failure.Code() != "interaction.tool.advertisement_invalid" {
				t.Fatalf("declaration defect=%v", result.Termination())
			}
			if status := <-finished; status != run.SettlementStatusSucceeded {
				t.Fatalf("completed external work became %s", status)
			}
			settlement := <-observer.settled
			if settlement.Unknown() || settlement.Result == nil || settlement.Result.IsError || settlement.Result.Output.Content[0].Text != "completed write" {
				t.Fatalf("Tool observation lost completion: %+v", settlement)
			}
			tree, err := engine.CaptureTree(t.Context(), result.ProcessID())
			if err != nil {
				t.Fatal(err)
			}
			for _, process := range tree.ProcessSnapshots() {
				if len(process.UnknownEffectIDs()) != 0 {
					t.Fatal("local advertisement defect created external uncertainty")
				}
			}
			tree, err = run.ParseTreeSnapshot(tree.JSON())
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := run.New(t.Context(), run.Config{Store: store})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := recovered.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			process, err := recovered.Recover(t.Context(), tools.Program(), tree)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := process.Await(t.Context())
			if err != nil || restored.Termination().Status() != run.StatusFailed || calls.Load() != 1 {
				t.Fatalf("restore=%v error=%v calls=%d", restored.Termination(), err, calls.Load())
			}
		})
	}
}
