package agent_test

import (
	startrand "crypto/rand"

	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

func TestModelRecoveryPreservesEffectiveContextWithoutReplay(t *testing.T) {
	calls := 0
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		calls++
		return nil, errors.New("response lost")
	})
	definition, err := agent.NewDefinition(agent.DefinitionConfig{PrepareContext: true, Name: "test.model_recovery", Description: "Recover a confirmed response.", CompletionValidator: func(_ context.Context, candidate agent.CompletionCandidate) (agent.CompletionDecision, error) {
		working := candidate.WorkingContext()
		if len(working.Messages) != 1 || working.Messages[0].Text() != "reduced" {
			return agent.CompletionDecision{}, errors.New("recovery lost effective context")
		}
		return agent.CompletionDecision{Accepted: true}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := agent.NewDispatcher(definition, agent.DispatcherConfig{Model: model, ContextProcessor: fixedResponseContext{text: "reduced"}})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{Definition: definition, Dispatcher: dispatcher, ImplementationDigest: run.ComputeDigest([]byte("code")), ConfigurationDigest: run.ComputeDigest([]byte("config"))})
	if err != nil {
		t.Fatal(err)
	}
	store := &recoveryRequestRecorder{MemoryStore: run.NewMemoryStore(), unknown: make(chan run.EffectRequest, 1)}
	engine, err := run.New(t.Context(), run.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	defer engine.Close(context.WithoutCancel(ctx))
	root, err := engine.Start(ctx, deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: interactionInput(t, "original")})
	if err != nil {
		t.Fatal(err)
	}
	defer root.Kill(context.WithoutCancel(ctx), "cleanup")
	var request run.EffectRequest
	select {
	case request = <-store.unknown:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var frozen struct {
		ModelCall struct {
			Request agent.Input `json:"request"`
		} `json:"model_call"`
	}
	if unmarshalErr := jsonv2.Unmarshal(request.Effect().Payload(), &frozen); unmarshalErr != nil || len(frozen.ModelCall.Request.Messages) != 1 || frozen.ModelCall.Request.Messages[0].Text() != "reduced" {
		t.Fatalf("pending model effect lost its exact input: %v", unmarshalErr)
	}
	if _, settleModelResultErr := agent.SettleModelResult(run.Program[agent.Input, agent.Output]{}, request, textResponse("done")); settleModelResultErr == nil {
		t.Fatal("unbound program accepted model result")
	}
	for name, test := range map[string]struct {
		request  run.EffectRequest
		response *chat.Response
	}{
		"missing request":  {response: textResponse("done")},
		"missing response": {request: request},
		"invalid response": {request: request, response: &chat.Response{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, settleErr := agent.SettleModelResult(deployment, test.request, test.response); settleErr == nil {
				t.Fatal("accepted invalid recovery")
			}
		})
	}
	settlement, err := agent.SettleModelResult(deployment, request, textResponse("done"))
	if err != nil {
		t.Fatal(err)
	}
	if resolveErr := root.ResolveUnknownEffect(ctx, request.ID(), settlement); resolveErr != nil {
		t.Fatal(resolveErr)
	}
	result, err := root.Await(ctx)
	if err != nil || result.Termination().Status() != run.StatusCompleted {
		t.Fatalf("result=%s error=%v", result.Termination().Status(), err)
	}
	tree, err := engine.CaptureTree(ctx, root.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := run.ParseTreeSnapshot(tree.JSON())
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range parsed.ProcessSnapshots() {
		if snapshot.Relation().ProcessID() != root.Relation().ProcessID() {
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
