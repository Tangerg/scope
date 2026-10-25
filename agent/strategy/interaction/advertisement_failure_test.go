package interaction

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	agent "github.com/Tangerg/scope/agent"
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
				ImplementationDigest: agent.ComputeDigest([]byte("advertisement-failure-tools")),
				ConfigurationDigest:  agent.ComputeDigest([]byte("advertisement-failure-config")),
			})
			if err != nil {
				t.Fatal(err)
			}
			store := agent.NewMemoryTreeCommitter()
			finished := make(chan agent.SettlementStatus, 1)
			engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store, EventListeners: []agent.EventListener{
				agent.EventListenerFunc(func(_ context.Context, event agent.Event) {
					if fact, ok := event.EffectFinished(); ok && fact.Target() == agent.EffectTargetDispatcher {
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
			input, err := agent.EncodePayload(toolCall{Call: chat.ToolCall{ID: "write", Name: "active", Arguments: `{}`}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := engine.Run(t.Context(), tools.Deployment(), input)
			if err != nil {
				t.Fatal(err)
			}
			failure, failed := result.Termination().Failure()
			if !failed || failure.Kind() != agent.FailureKindContract || failure.Code() != "interaction.tool.advertisement_invalid" {
				t.Fatalf("declaration defect=%v", result.Termination())
			}
			if status := <-finished; status != agent.SettlementStatusSucceeded {
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
			tree, err = agent.ParseTreeSnapshot(tree.JSON())
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := recovered.Close(context.WithoutCancel(t.Context())); closeErr != nil {
					t.Error(closeErr)
				}
			})
			process, err := recovered.RestoreTree(t.Context(), tools.Deployment(), tree)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := process.Await(t.Context())
			if err != nil || restored.Termination().Status() != agent.StatusFailed || calls.Load() != 1 {
				t.Fatalf("restore=%v error=%v calls=%d", restored.Termination(), err, calls.Load())
			}
		})
	}
}
