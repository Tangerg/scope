package run_test

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
)

func TestExternalPackageCanComposeAndRunDefinition(t *testing.T) {
	definition, err := newEchoDefinition()
	if err != nil {
		t.Fatal(err)
	}
	expectedPayload, err := jsonv2.Marshal(echoInput{Value: "done"})
	if err != nil {
		t.Fatal(err)
	}
	expectedEffect, err := run.NewDispatcherEffect(expectedPayload)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := runtest.NewScriptedDispatcher(runtest.ScriptedDispatcherConfig{
		ReplayPolicy: run.ReplayPolicyNever,
		Calls: []runtest.ScriptedCall{{
			ExpectedEffect:    &expectedEffect,
			Deltas:            []json.RawMessage{json.RawMessage(`{"text":"do"}`)},
			SettlementStatus:  run.SettlementStatusSucceeded,
			SettlementPayload: json.RawMessage(`{"value":"done"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	decorator := &countingDispatcher{next: dispatcher}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: decorator,
		ImplementationDigest: run.ComputeDigest([]byte("example-echo-implementation")),
		ConfigurationDigest:  run.ComputeDigest([]byte("example-echo-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	observations := &runtest.ObservationRecorder{}
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore(),
		EventListeners: []run.EventListener{observations},
		DeltaListeners: []run.DeltaListener{observations},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := deployment.Descriptor().EncodeInput(echoInput{Value: "done"})
	if err != nil {
		t.Fatal(err)
	}
	runtest.RunDefinitionConformance(t, runtest.DefinitionConformanceConfig{
		Definition: definition, Input: input,
	})
	result, err := runtest.Observe(context.Background(), engine, deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	output, ok := result.Termination().Output()
	if !ok {
		t.Fatal("completed Result has no Output")
	}
	value, err := deployment.Descriptor().DecodeOutput[echoOutput](output)
	if err != nil || value.Value != "done" {
		t.Fatalf("output=%+v err=%v", value, err)
	}
	if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
		t.Fatal(closeErr)
	}
	if decorator.attempts.Load() != 1 || decorator.Policy(expectedEffect).Replay != run.ReplayPolicyNever {
		t.Fatalf("attempts=%d replay policy=%s", decorator.attempts.Load(), decorator.Policy(expectedEffect).Replay)
	}
	if dispatcher.Remaining() != 0 || len(dispatcher.Requests()) != 1 {
		t.Fatalf("remaining dispatches=%d requests=%d", dispatcher.Remaining(), len(dispatcher.Requests()))
	}
	if len(observations.Events()) == 0 || len(observations.Deltas()) != 1 {
		t.Fatalf("events=%d deltas=%d", len(observations.Events()), len(observations.Deltas()))
	}
	waitContext, cancelWait := context.WithTimeout(t.Context(), time.Second)
	defer cancelWait()
	finished, err := observations.AwaitEvent(waitContext, func(event run.Event) bool {
		return event.Name() == run.EventProcessFinished
	})
	if err != nil || finished.Relation().ProcessID() != result.ProcessID() {
		t.Fatalf("finished event=%+v error=%v", finished, err)
	}
	t.Run("decoration preserves same-identity replay", func(t *testing.T) {
		request := dispatcher.Requests()[0]
		decorator := &countingDispatcher{next: echoDispatcher{}}
		var deltas []json.RawMessage
		for range 2 {
			if decorator.Policy(request.Effect()).Replay != run.ReplayPolicySameIdentity {
				t.Fatal("decoration changed replay policy")
			}
			settlement, err := decorator.Dispatch(t.Context(), request, func(payload json.RawMessage) {
				deltas = append(deltas, bytes.Clone(payload))
			})
			if err != nil || settlement.Status() != run.SettlementStatusSucceeded ||
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

func TestExampleDefinitionDoesNotPublishFailedSettlementAsOutput(t *testing.T) {
	definition, err := newEchoDefinition()
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := runtest.NewScriptedDispatcher(runtest.ScriptedDispatcherConfig{
		ReplayPolicy: run.ReplayPolicyNever,
		Calls: []runtest.ScriptedCall{{
			SettlementStatus: run.SettlementStatusFailed, SettlementPayload: json.RawMessage(`{"value":"failed attempt"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("example-echo-failure")),
		ConfigurationDigest:  run.ComputeDigest([]byte("failed-settlement")),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	input, err := run.EncodePayload(echoInput{Value: "requested"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtest.Observe(t.Context(), engine, deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	failure, failed := result.Termination().Failure()
	if !failed || failure.Kind() != run.FailureKindExternal || failure.Code() != "example.echo.failed" {
		t.Fatalf("failed settlement became a completed echo: %+v", result.Termination())
	}
	if _, present := result.Termination().Output(); present {
		t.Fatal("failed settlement payload escaped as a successful output")
	}
}
