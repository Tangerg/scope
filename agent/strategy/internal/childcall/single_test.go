package childcall_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
	"github.com/Tangerg/scope/agent/strategy/workflow"
)

func TestSingleHandshakeRestoresAtEveryBoundary(t *testing.T) {
	ref, key, waitKey := invocation(t)
	var progress childcall.Single
	roundTrip(t, &progress, childcall.AwaitingStart)
	start := startSignal(t, ref, key, "child", nil)
	if _, err := progress.AcceptStart(start, key, ref); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, &progress, childcall.AwaitingOpening)
	effect, err := progress.WaitEffect(waitKey, agent.ChildWaitBoundaryDrained)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"operation":"wait_children","spec":{"key":"children","children":["child"],"boundary":"subtree_drained","condition":{"kind":"all"}}}`
	var got, expected any
	if decodeErr := json.Unmarshal(effect.Payload(), &got); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if decodeErr := json.Unmarshal([]byte(want), &expected); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if string(encoded(t, got)) != string(encoded(t, expected)) {
		t.Fatalf("wait effect=%s", effect.Payload())
	}
	opening := openingSignal(t, "wait", "children", "subtree_drained", []string{"child"}, "all")
	waitID, err := progress.AcceptOpening(opening, waitKey, agent.ChildWaitBoundaryDrained)
	if err != nil || waitID.String() != "wait" {
		t.Fatalf("wait ID=%s error=%v", waitID, err)
	}
	roundTrip(t, &progress, childcall.AwaitingCompletion)
	result, err := progress.Complete(completionSignal(t, "wait", "children", "subtree_drained", "call", "child"), key, waitKey, agent.ChildWaitBoundaryDrained)
	if err != nil || result.ProcessID() != progress.ProcessID() || result.Status() != agent.StatusCompleted {
		t.Fatalf("completion=%+v error=%v", result, err)
	}
}

func TestSingleRejectsMismatchedResponsesWithoutAdvancing(t *testing.T) {
	ref, key, waitKey := invocation(t)
	var otherKey agent.ChildKey
	if err := json.Unmarshal([]byte(`"other"`), &otherKey); err != nil {
		t.Fatal(err)
	}
	start := startSignal(t, ref, key, "child", nil)
	var progress childcall.Single
	if _, err := progress.AcceptStart(start, otherKey, ref); err == nil || progress.Phase() != childcall.AwaitingStart {
		t.Fatal("mismatched child start advanced progress")
	}
	if _, err := progress.AcceptStart(start, key, agent.DeploymentRef{}); err == nil || progress.Phase() != childcall.AwaitingStart {
		t.Fatal("mismatched Deployment advanced progress")
	}
	if _, err := progress.AcceptStart(start, key, ref); err != nil {
		t.Fatal(err)
	}
	for name, signal := range map[string]agent.Signal{
		"wait key":    openingSignal(t, "wait", "other", "subtree_drained", []string{"child"}, "all"),
		"boundary":    openingSignal(t, "wait", "children", "terminal_result", []string{"child"}, "all"),
		"child":       openingSignal(t, "wait", "children", "subtree_drained", []string{"other"}, "all"),
		"extra child": openingSignal(t, "wait", "children", "subtree_drained", []string{"child", "other"}, "all"),
		"condition":   openingSignal(t, "wait", "children", "subtree_drained", []string{"child"}, "any"),
	} {
		t.Run("opening/"+name, func(t *testing.T) {
			if _, err := agent.ParseChildWaitOpened(signal); err != nil {
				t.Fatalf("invalid fixture: %v", err)
			}
			if _, err := progress.AcceptOpening(signal, waitKey, agent.ChildWaitBoundaryDrained); err == nil || progress.Phase() != childcall.AwaitingOpening {
				t.Fatal("mismatched wait opening advanced progress")
			}
		})
	}
	if _, err := progress.AcceptOpening(openingSignal(t, "wait", "children", "subtree_drained", []string{"child"}, "all"), waitKey, agent.ChildWaitBoundaryDrained); err != nil {
		t.Fatal(err)
	}
	for name, signal := range map[string]agent.Signal{
		"wait ID":    completionSignal(t, "other", "children", "subtree_drained", "call", "child"),
		"wait key":   completionSignal(t, "wait", "other", "subtree_drained", "call", "child"),
		"boundary":   completionSignal(t, "wait", "children", "terminal_result", "call", "child"),
		"child key":  completionSignal(t, "wait", "children", "subtree_drained", "other", "child"),
		"process ID": completionSignal(t, "wait", "children", "subtree_drained", "call", "other"),
	} {
		t.Run("completion/"+name, func(t *testing.T) {
			if _, err := agent.ParseChildWaitSatisfied(signal); err != nil {
				t.Fatalf("invalid fixture: %v", err)
			}
			if _, err := progress.Complete(signal, key, waitKey, agent.ChildWaitBoundaryDrained); err == nil {
				t.Fatal("mismatched completion returned a result")
			}
		})
	}
	var extra struct {
		Operation string            `json:"operation"`
		Key       string            `json:"key"`
		Boundary  string            `json:"boundary"`
		Outcomes  []json.RawMessage `json:"outcomes"`
	}
	if err := json.Unmarshal(completionSignal(t, "wait", "children", "subtree_drained", "call", "child").Payload(), &extra); err != nil {
		t.Fatal(err)
	}
	var other struct {
		Outcomes []json.RawMessage `json:"outcomes"`
	}
	if err := json.Unmarshal(completionSignal(t, "wait", "children", "subtree_drained", "other", "other").Payload(), &other); err != nil {
		t.Fatal(err)
	}
	extra.Outcomes = append(extra.Outcomes, other.Outcomes...)
	extraSignal := signal(t, "wait", extra)
	if _, err := agent.ParseChildWaitSatisfied(extraSignal); err != nil {
		t.Fatalf("invalid multi-outcome fixture: %v", err)
	}
	if _, err := progress.Complete(extraSignal, key, waitKey, agent.ChildWaitBoundaryDrained); err == nil {
		t.Fatal("single-child completion accepted an additional outcome")
	}
	before := string(encoded(t, progress))
	if _, err := progress.AcceptStart(start, key, ref); err == nil {
		t.Fatal("an open wait accepted a repeated start")
	}
	if _, err := progress.AcceptOpening(openingSignal(t, "other", "children", "subtree_drained", []string{"child"}, "all"), waitKey, agent.ChildWaitBoundaryDrained); err == nil {
		t.Fatal("an open wait accepted a replacement opening")
	}
	if string(encoded(t, progress)) != before {
		t.Fatal("out-of-phase responses changed progress")
	}
}

func TestSingleLeavesStartFailureToStrategy(t *testing.T) {
	ref, key, _ := invocation(t)
	failure, err := agent.NewFailure(agent.FailureKindExternal, "child.start.failed", "unavailable")
	if err != nil {
		t.Fatal(err)
	}
	var progress childcall.Single
	result, err := progress.AcceptStart(startSignal(t, ref, key, "", &failure), key, ref)
	if err != nil {
		t.Fatal(err)
	}
	got, failed := result.Failure()
	if !failed || got.Code() != failure.Code() || got.Message() != failure.Message() || progress.Phase() != childcall.AwaitingStart {
		t.Fatal("start failure was converted or installed as a successful child")
	}
}

func TestSingleRestoreRejectsMalformedProgressAtomically(t *testing.T) {
	for _, payload := range []string{
		`null`, `[]`, `{"wait_id":"wait"}`, `{"process_id":"bad/id"}`,
		`{"process_id":"child","wait_id":""}`, `{"process_id":"child","unknown":true}`,
		`{"process_id":"child","process_id":"other"}`, `{} {}`,
	} {
		t.Run(payload, func(t *testing.T) {
			var progress childcall.Single
			if err := json.Unmarshal([]byte(`{"process_id":"original","wait_id":"wait"}`), &progress); err != nil {
				t.Fatal(err)
			}
			before := string(encoded(t, progress))
			if err := json.Unmarshal([]byte(payload), &progress); err == nil {
				t.Fatal("malformed progress was accepted")
			}
			if string(encoded(t, progress)) != before {
				t.Fatal("failed decode changed the current invocation")
			}
		})
	}
}

func invocation(t *testing.T) (agent.DeploymentRef, agent.ChildKey, agent.WaitKey) {
	t.Helper()
	stage, err := workflow.Transform("identity", func(_ context.Context, value int) (int, error) { return value, nil })
	if err != nil {
		t.Fatal(err)
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: "test.childcall", Description: "Exercise the child protocol.", Stages: []workflow.Stage{stage},
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, ImplementationDigest: agent.ComputeDigest([]byte("implementation")),
		ConfigurationDigest: agent.ComputeDigest([]byte("configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := agent.ParseChildKey("call")
	if err != nil {
		t.Fatal(err)
	}
	waitKey, err := agent.ParseWaitKey("children")
	if err != nil {
		t.Fatal(err)
	}
	return deployment.DeploymentRef(), key, waitKey
}

func roundTrip(t *testing.T, progress *childcall.Single, want childcall.Phase) {
	t.Helper()
	payload := encoded(t, progress)
	var restored childcall.Single
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Phase() != want || string(encoded(t, restored)) != string(payload) {
		t.Fatal("restoration changed the next accepted protocol boundary")
	}
	*progress = restored
}

func startSignal(t *testing.T, ref agent.DeploymentRef, key agent.ChildKey, process string, failure *agent.Failure) agent.Signal {
	t.Helper()
	return signal(t, "", struct {
		Operation     string              `json:"operation"`
		Key           agent.ChildKey      `json:"key"`
		DeploymentRef agent.DeploymentRef `json:"deployment_ref"`
		ProcessID     string              `json:"process_id,omitempty"`
		Failure       *agent.Failure      `json:"failure,omitempty"`
	}{"start_child", key, ref, process, failure})
}

func openingSignal(t *testing.T, waitID, key, boundary string, children []string, condition string) agent.Signal {
	t.Helper()
	payload := fmt.Sprintf(`{"operation":"child_wait_opened","spec":{"key":%q,"children":%s,"boundary":%q,"condition":{"kind":%q}}}`, key, encoded(t, children), boundary, condition)
	return signal(t, waitID, json.RawMessage(payload))
}

func completionSignal(t *testing.T, waitID, waitKey, boundary, childKey, processID string) agent.Signal {
	t.Helper()
	payload := fmt.Sprintf(`{"operation":"child_wait_satisfied","key":%q,"boundary":%q,"outcomes":[{"key":%q,"result":{"process_id":%q,"started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:01Z","output":7,"termination":{"status":"completed","cause":"completion"},"usage":{}}}]}`, waitKey, boundary, childKey, processID)
	return signal(t, waitID, json.RawMessage(payload))
}

func signal(t *testing.T, waitID string, payload any) agent.Signal {
	t.Helper()
	data := encoded(t, struct {
		ID      string `json:"id"`
		WaitID  string `json:"wait_id,omitempty"`
		Payload any    `json:"payload"`
	}{"signal:childcall", waitID, payload})
	var value agent.Signal
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func encoded(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
