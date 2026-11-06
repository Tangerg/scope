package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestUnknownResolutionEndsOneLogicalEffectSpan(t *testing.T) {
	h := newObserverHarness(t)
	cause := errors.New("secret dispatch acknowledgment lost")
	handle, _ := startObservedExecution(t, h, testDispatcher{err: cause})
	var unknown run.EffectSettled
	for entry, err := range handle.Observe(t.Context(), 0) {
		if err != nil {
			t.Fatal(err)
		}
		if settled, ok := entry.Record.(run.EffectSettled); ok && settled.Outcome.Status() == run.SettlementStatusUnknown {
			unknown = settled
			break
		}
	}
	if unknown.Failure == nil {
		t.Fatal("runtime failure not persisted")
	}
	settlement, err := run.NewSettlement(run.SettlementStatusSucceeded, json.RawMessage(`"secret-investigated-result"`))
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.ResolveUnknownEffect(t.Context(), unknown.ID, settlement); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.observer.Consume(t.Context(), handle, 0); err != nil {
		t.Fatal(err)
	}
	effects, uncertainties := 0, 0
	for _, span := range h.spans.Ended() {
		if strings.Contains(span.Status().Description, "secret") {
			t.Fatal("diagnostic leaked")
		}
		if span.Name() != "agent.effect" {
			continue
		}
		effects++
		if span.Status().Code == codes.Error {
			t.Fatal("resolved logical effect remained failed")
		}
		for _, event := range span.Events() {
			if event.Name == "agent.effect.unknown" {
				uncertainties++
			}
		}
	}
	if effects != 1 || uncertainties != 1 {
		t.Fatalf("effects=%d uncertainty events=%d", effects, uncertainties)
	}
}

func TestChildWaitProjectsParentageAndEveryMessageVariant(t *testing.T) {
	h := newObserverHarness(t)
	runtime, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore(), Build: "otel-child"})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	gate, err := coordination.Compile[string, run.Payload](runtime.Compiler(), coordination.Config{Name: "test.gate", Description: "Wait for a string", Policy: coordination.InputGate{RequestSchema: schema, AnswerSchema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	stage, err := workflow.Call(workflow.CallConfig{ID: "gate", Child: run.ChildBinding{Program: gate.Erase()}})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := workflow.Compile[string, run.Payload](runtime.Compiler(), workflow.Config{Name: "test.parent", Description: "Join a child", Stages: []workflow.Stage{stage}})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Start(t.Context(), parent, run.StartRequest[string]{ID: run.StartID{Scope: "otel", Key: "child"}, Input: "secret-question"})
	if err != nil {
		t.Fatal(err)
	}
	var owner run.ProcessID
	var wait run.WaitKey
	for entry, err := range handle.Observe(t.Context(), 0) {
		if err != nil {
			t.Fatal(err)
		}
		if step, ok := entry.Record.(run.Stepped); ok && len(step.Transition.Waits()) > 0 {
			owner, wait = step.Process, step.Transition.Waits()[0].Key
			break
		}
	}
	child, ok := runtime.Process(owner)
	if !ok {
		t.Fatal("child is absent")
	}
	if err := child.Answer(t.Context(), wait, "secret-answer"); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Await(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.observer.Consume(t.Context(), handle, 0); err != nil {
		t.Fatal(err)
	}
	spans := h.spans.Ended()
	var childFound bool
	for _, childSpan := range spans {
		if childSpan.Name() != "invoke_agent test.gate" {
			continue
		}
		for _, parentSpan := range spans {
			if parentSpan.Name() == "invoke_agent test.parent" && childSpan.Parent().SpanID() == parentSpan.SpanContext().SpanID() {
				childFound = true
			}
		}
	}
	if !childFound {
		t.Fatal("child invocation lost its parent")
	}
	var metrics metricdata.ResourceMetrics
	if err := h.metrics.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	var messages int64
	for _, scope := range metrics.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != "agent.process.accepted_messages" {
				continue
			}
			sum := metric.Data.(metricdata.Sum[int64])
			for _, point := range sum.DataPoints {
				messages += point.Value
			}
		}
	}
	if messages != 3 {
		t.Fatalf("accepted Signal+Completion+Exit=%d, want 3", messages)
	}
}

func TestExecutionFailureProjectsOnlyItsStableClassification(t *testing.T) {
	h := newObserverHarness(t)
	runtime, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore(), Build: "otel-failure"})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	stage, err := workflow.Transform("fail", func(context.Context, string) (string, error) { return "", errors.New("secret transform diagnostic") })
	if err != nil {
		t.Fatal(err)
	}
	program, err := workflow.Compile[string, run.Payload](runtime.Compiler(), workflow.Config{Name: "test.failure", Description: "Fail before publication", Revision: "failure", Stages: []workflow.Stage{stage}})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[string]{ID: run.StartID{Scope: "otel", Key: "failure"}, Input: "input"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := handle.Await(t.Context())
	if err != nil || result.Termination().Status() != run.StatusFailed {
		t.Fatal(result, err)
	}
	if _, err := h.observer.Consume(t.Context(), handle, 0); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, span := range h.spans.Ended() {
		if span.Name() != "invoke_agent test.failure" {
			continue
		}
		found = true
		if span.Status().Code != codes.Error || strings.Contains(span.Status().Description, "secret") {
			t.Fatal("failure classification lost or leaked")
		}
		for _, event := range span.Events() {
			for _, attribute := range event.Attributes {
				if strings.Contains(attribute.Value.AsString(), "secret") {
					t.Fatal("failure diagnostic leaked")
				}
			}
		}
	}
	if !found {
		t.Fatal("failure span absent")
	}
}
