package agent_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"

	"github.com/Tangerg/scope/agent/run"
)

type failedEffectDispatcher struct{ outcome run.Settlement }

func (f failedEffectDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}

func (f failedEffectDispatcher) Dispatch(context.Context, run.EffectRequest, run.DeltaEmitter) (run.Settlement, error) {
	return f.outcome, nil
}

func TestFailedSettlementProjectsCanonicalClassificationWithoutDiagnostic(t *testing.T) {
	failure, err := run.NewFailure(run.FailureKindExternal, "test.external.failed", "secret diagnostic")
	if err != nil {
		t.Fatal(err)
	}
	details, err := run.EncodePayload("secret completed domain evidence")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := run.NewFailedSettlement(failure, details)
	if err != nil {
		t.Fatal(err)
	}
	h := newObserverHarness(t)
	handle, _ := startObservedExecution(t, h, failedEffectDispatcher{outcome: outcome})
	if _, err := handle.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.observer.Consume(t.Context(), handle, 0); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, span := range h.spans.Ended() {
		if span.Name() != "agent.effect" {
			continue
		}
		found = true
		if span.Status().Code != codes.Error || span.Status().Description != failure.Code() {
			t.Fatalf("settlement classification was not preserved: %+v", span.Status())
		}
		attributes, err := jsonv2.Marshal(span.Attributes())
		if err != nil {
			t.Fatal(err)
		}
		events, err := jsonv2.Marshal(span.Events())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(attributes), "secret") || strings.Contains(string(events), "secret") {
			t.Fatal("diagnostic or domain evidence reached telemetry")
		}
	}
	if !found {
		t.Fatal("failed effect span is missing")
	}
}
