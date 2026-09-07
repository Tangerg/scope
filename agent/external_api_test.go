package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
)

func TestExternalPackageCanComposeAndRunDefinition(t *testing.T) {
	definition, err := newEchoDefinition()
	if err != nil {
		t.Fatal(err)
	}
	expectedPayload, err := json.Marshal(echoInput{Value: "done"})
	if err != nil {
		t.Fatal(err)
	}
	expectedEffect, err := agent.NewDispatcherEffect(expectedPayload)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := agenttest.NewScriptedDispatcher(agenttest.ScriptedDispatcherConfig{
		ReplayPolicy: agent.ReplayPolicyNever,
		Steps: []agenttest.DispatchStep{{
			ExpectedEffect:    &expectedEffect,
			Deltas:            []json.RawMessage{json.RawMessage(`{"text":"do"}`)},
			SettlementStatus:  agent.SettlementStatusSucceeded,
			SettlementPayload: json.RawMessage(`{"value":"done"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	decorator := &countingDispatcher{next: dispatcher}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: decorator,
		ImplementationDigest: agent.ComputeDigest([]byte("example-echo-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("example-echo-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	observations := &agenttest.ObservationRecorder{}
	engine, err := agent.NewEngine(agent.EngineConfig{
		EventListeners: []agent.EventListener{observations},
		DeltaListeners: []agent.DeltaListener{observations},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := deployment.Descriptor().EncodeInput(echoInput{Value: "done"})
	if err != nil {
		t.Fatal(err)
	}
	agenttest.RunDefinitionConformance(t, agenttest.DefinitionConformanceConfig{
		Definition: definition, Input: input,
	})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	output, ok := result.Output()
	if !ok {
		t.Fatal("completed Result has no Output")
	}
	value, err := deployment.Descriptor().DecodeOutput[echoOutput](output)
	if err != nil || value.Value != "done" {
		t.Fatalf("output=%+v err=%v", value, err)
	}
	if closeErr := engine.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if decorator.attempts.Load() != 1 || decorator.ReplayPolicy(expectedEffect) != agent.ReplayPolicyNever {
		t.Fatalf("attempts=%d replay policy=%s", decorator.attempts.Load(), decorator.ReplayPolicy(expectedEffect))
	}
	if dispatcher.Remaining() != 0 || len(dispatcher.Requests()) != 1 {
		t.Fatalf("remaining dispatches=%d requests=%d", dispatcher.Remaining(), len(dispatcher.Requests()))
	}
	if len(observations.Events()) == 0 || len(observations.Deltas()) != 1 {
		t.Fatalf("events=%d deltas=%d", len(observations.Events()), len(observations.Deltas()))
	}
	waitContext, cancelWait := context.WithTimeout(t.Context(), time.Second)
	defer cancelWait()
	finished, err := observations.AwaitEvent(waitContext, func(event agent.Event) bool {
		return event.Name() == agent.EventProcessFinished
	})
	if err != nil || finished.ProcessID() != result.ProcessID() {
		t.Fatalf("finished event=%+v error=%v", finished, err)
	}
	t.Run("decoration preserves same-identity replay", func(t *testing.T) {
		request := dispatcher.Requests()[0]
		decorator := &countingDispatcher{next: echoDispatcher{}}
		var deltas []json.RawMessage
		for range 2 {
			if decorator.ReplayPolicy(request.Effect()) != agent.ReplayPolicySameIdentity {
				t.Fatal("decoration changed replay policy")
			}
			settlement, err := decorator.Dispatch(t.Context(), request, func(payload json.RawMessage) {
				deltas = append(deltas, bytes.Clone(payload))
			})
			if err != nil || settlement.EffectID() != request.ID() || settlement.Status() != agent.SettlementStatusSucceeded ||
				!bytes.Equal(settlement.Payload(), request.Effect().Payload()) {
				t.Fatalf("settlement=%+v error=%v", settlement, err)
			}
		}
		if decorator.attempts.Load() != 2 || len(deltas) != 2 {
			t.Fatalf("attempts=%d deltas=%d", decorator.attempts.Load(), len(deltas))
		}
		for _, delta := range deltas {
			if !bytes.Equal(delta, request.Effect().Payload()) {
				t.Fatalf("delta=%s", delta)
			}
		}
	})
}
