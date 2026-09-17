package interaction_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/agent/strategy/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

func TestUnlimitedRootChildAndToolGrandchildrenRestore(t *testing.T) {
	const rounds = 70
	model := chat.ModelFunc(func(ctx context.Context, _ *chat.Request) (*chat.Response, error) {
		if _, deadline := ctx.Deadline(); deadline {
			return nil, fmt.Errorf("unexpected implicit execution deadline")
		}
		invocation, ok := interaction.ModelInvocationFromContext(ctx)
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
	worker := configuredInteraction(t, interaction.DefinitionConfig{Name: "interaction.unlimited", Description: "Run until the model completes."}, interaction.DispatcherConfig{Model: client}, interaction.ToolSetConfig{Tools: []tool.Tool{tick}})
	call, err := workflow.Call(workflow.CallConfig{ID: "worker", Deployment: worker.Deployment})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{Name: "interaction.unlimited_root", Description: "Run an unlimited worker.", Stages: []workflow.Stage{call}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := agent.NewDeployment(agent.DeploymentConfig{Definition: definition, ImplementationDigest: agent.ComputeDigest([]byte("unlimited-root")), ConfigurationDigest: agent.ComputeDigest([]byte("unlimited-grants"))})
	if err != nil {
		t.Fatal(err)
	}
	resolver := worker.resolveWith(worker.Deployment)
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver, TreeLimits: agent.TreeLimits{MaxActiveChildren: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	process, err := engine.Start(t.Context(), root, interactionInput(t, "continue"))
	if err != nil {
		t.Fatal(err)
	}
	if joinErr := process.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	result, err := process.Await(t.Context())
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("result=%s error=%v termination=%+v", result.Status(), err, result.Termination())
	}
	output, _ := result.Output()
	value, err := output.Decode[interaction.Output]()
	if err != nil || value.ModelCalls != rounds+1 {
		t.Fatalf("model calls=%d error=%v", value.ModelCalls, err)
	}
	tree, err := engine.CaptureTree(t.Context(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.ProcessSnapshots()) != rounds+2 {
		t.Fatalf("retained processes=%d", len(tree.ProcessSnapshots()))
	}
	for _, snapshot := range tree.ProcessSnapshots() {
		if snapshot.Budget() != (agent.Budget{}) || snapshot.Usage().CommittedSteps == 0 {
			t.Fatalf("grant or usage lost at depth %d", snapshot.Relation().Depth())
		}
	}
	encoded, err := agent.ParseTreeSnapshot(tree.JSON())
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver, Limits: agent.Limits{MaxSteps: agent.NewQuota(0), MaxEffects: agent.NewQuota(0), MaxSignals: agent.NewQuota(0)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := recovery.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	restored, err := recovery.RestoreTree(t.Context(), root, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	if restored.Budget() != (agent.Budget{}) {
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
	deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "interaction.unlimited_cancel", Description: "Honor host cancellation."}, interaction.DispatcherConfig{Model: client}, interaction.ToolSetConfig{})
	engine, err := agent.NewEngine(agent.EngineConfig{})
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
	process, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "continue"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	cancel()
	if joinErr := process.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	result, err := process.Await(t.Context())
	if err != nil || result.Status() != agent.StatusCanceled || result.Usage().PreparedEffects != 1 {
		t.Fatalf("cancellation=%s usage=%+v error=%v", result.Status(), result.Usage(), err)
	}
}
