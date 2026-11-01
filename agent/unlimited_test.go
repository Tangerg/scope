package agent_test

import (
	startrand "crypto/rand"

	"context"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

func TestUnlimitedRootChildAndToolGrandchildrenRestore(t *testing.T) {
	store := run.NewMemoryStore()
	const rounds = 70
	model := chat.ModelFunc(func(ctx context.Context, _ *chat.Request) (*chat.Response, error) {
		if _, deadline := ctx.Deadline(); deadline {
			return nil, fmt.Errorf("unexpected implicit execution deadline")
		}
		invocation, ok := agent.ModelInvocationFromContext(ctx)
		if !ok {
			return nil, fmt.Errorf("missing invocation")
		}
		if invocation.ModelCallSequence() > rounds {
			return textResponse("done"), nil
		}
		return toolCallResponse(chat.ToolCall{ID: fmt.Sprintf("call_%d", invocation.ModelCallSequence()), Name: "tick", Arguments: `{}`}), nil
	})
	tick, err := tool.NewFunc(tool.FuncConfig{Name: "tick", Description: "Complete one unit of work."}, func(context.Context, struct{}) (string, error) { return "done", nil })
	if err != nil {
		t.Fatal(err)
	}
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	worker := configuredInteraction(t, agent.DefinitionConfig{Name: "interaction.unlimited", Description: "Run until the model completes."}, agent.DispatcherConfig{Model: client}, agent.ToolSetConfig{Tools: []tool.Tool{tick}})
	call, err := workflow.Call(workflow.CallConfig{ID: "worker", Deployment: worker.Program})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{Name: "interaction.unlimited_root", Description: "Run an unlimited worker.", Stages: []workflow.Stage{call}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{Definition: definition, ImplementationDigest: run.ComputeDigest([]byte("unlimited-root")), ConfigurationDigest: run.ComputeDigest([]byte("unlimited-grants"))})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := run.New(t.Context(), run.Config{Store: store, TreeLimits: run.TreeLimits{MaxActiveChildren: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	process, err := engine.Start(t.Context(), root, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "continue")})
	if err != nil {
		t.Fatal(err)
	}
	if joinErr := process.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	result, err := process.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("result=%s error=%v termination=%+v", result.Termination().Status(), err, result.Termination())
	}
	output, _ := result.Termination().Output()
	value, err := output.Decode[agent.Output]()
	if err != nil || value.ModelCalls != rounds+1 {
		t.Fatalf("model calls=%d error=%v", value.ModelCalls, err)
	}
	tree, err := engine.CaptureTree(t.Context(), process.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.ProcessSnapshots()) != rounds+2 {
		t.Fatalf("retained processes=%d", len(tree.ProcessSnapshots()))
	}
	for _, snapshot := range tree.ProcessSnapshots() {
		if snapshot.Budget() != (run.Budget{}) || snapshot.Usage().CommittedSteps == 0 {
			t.Fatalf("grant or usage lost at depth %d", snapshot.Relation().Depth())
		}
	}
	encoded, err := run.ParseTreeSnapshot(tree.JSON())
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := run.New(t.Context(), run.Config{Store: store, Budget: run.Budget{Steps: run.NewQuota(0), Effects: run.NewQuota(0), Signals: run.NewQuota(0)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := recovery.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	restored, err := recovery.Recover(t.Context(), root, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	if restored.Budget() != (run.Budget{}) {
		t.Fatal("restore applied the new Engine's finite policy")
	}
}

func TestUnlimitedInteractionHonorsHostCancellation(t *testing.T) {
	entered := make(chan struct{})
	model := chat.ModelFunc(func(ctx context.Context, _ *chat.Request) (*chat.Response, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	client, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		t.Fatal(err)
	}
	deployment := configuredInteraction(t, agent.DefinitionConfig{Name: "interaction.unlimited_cancel", Description: "Honor host cancellation."}, agent.DispatcherConfig{Model: client}, agent.ToolSetConfig{})
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	process, err := engine.Start(ctx, deployment.Program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "continue")})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	cancel()
	if cancelErr := process.Cancel(t.Context(), "end managed task"); cancelErr != nil {
		t.Fatal(cancelErr)
	}
	if joinErr := process.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	result, err := process.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusCanceled || result.Usage().PreparedEffects != 1 {
		t.Fatalf("cancellation=%s usage=%+v error=%v", result.Termination().Status(), result.Usage(), err)
	}
}
