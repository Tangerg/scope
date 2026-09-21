package interaction_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

func TestModelRecoveryPreservesEffectiveContextWithoutReplay(t *testing.T) {
	calls := 0
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		calls++
		return nil, errors.New("response lost")
	})
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{Name: "test.model_recovery", Description: "Recover a confirmed response.", CompletionValidator: func(_ context.Context, candidate interaction.CompletionCandidate) (interaction.CompletionDecision, error) {
		working := candidate.WorkingContext()
		if len(working.Messages) != 1 || working.Messages[0].Text() != "reduced" {
			return interaction.CompletionDecision{}, errors.New("recovery lost effective context")
		}
		return interaction.CompletionDecision{Accepted: true}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: model, ModelContextReducer: fixedResponseContext{text: "reduced"}})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{Definition: definition, Dispatcher: dispatcher, ImplementationDigest: agent.ComputeDigest([]byte("code")), ConfigurationDigest: agent.ComputeDigest([]byte("config"))})
	if err != nil {
		t.Fatal(err)
	}
	store := &recoveryRequestRecorder{MemoryTreeCommitter: agent.NewMemoryTreeCommitter(), unknown: make(chan agent.EffectRequest, 1)}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	defer engine.Close(context.WithoutCancel(ctx))
	root, err := engine.Start(ctx, deployment, interactionInput(t, "original"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Kill(context.WithoutCancel(ctx), "cleanup")
	var request agent.EffectRequest
	select {
	case request = <-store.unknown:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	messages := []chat.Message{chat.NewUserMessage(chat.NewTextPart("reduced"))}
	for name, test := range map[string]struct {
		request  agent.EffectRequest
		response *chat.Response
		messages []chat.Message
	}{
		"missing request":  {response: textResponse("done"), messages: messages},
		"missing response": {request: request, messages: messages},
		"missing context":  {request: request, response: textResponse("done")},
		"invalid response": {request: request, response: &chat.Response{}, messages: messages},
		"invalid context":  {request: request, response: textResponse("done"), messages: []chat.Message{{}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, settleErr := dispatcher.SettleModelResult(test.request, test.response, test.messages); settleErr == nil {
				t.Fatal("accepted invalid recovery")
			}
		})
	}
	settlement, err := dispatcher.SettleModelResult(request, textResponse("done"), messages)
	if err != nil {
		t.Fatal(err)
	}
	if resolveErr := root.ResolveUnknownEffect(ctx, settlement); resolveErr != nil {
		t.Fatal(resolveErr)
	}
	result, err := root.Await(ctx)
	if err != nil || result.Status() != agent.StatusCompleted {
		t.Fatalf("result=%s error=%v", result.Status(), err)
	}
	tree, err := engine.CaptureTree(ctx, root.ID())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := agent.ParseTreeSnapshot(tree.JSON())
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range parsed.ProcessSnapshots() {
		if snapshot.ProcessID() != root.ID() {
			continue
		}
		if _, restoreErr := definition.Restore(ctx, snapshot.CommittedExecutionState()); restoreErr != nil {
			t.Fatal(restoreErr)
		}
	}
	if calls != 1 {
		t.Fatalf("model calls=%d", calls)
	}
}
