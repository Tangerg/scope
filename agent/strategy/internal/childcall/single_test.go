package childcall_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

func TestSingleHandshakeRestoresAtEveryBoundary(t *testing.T) {
	waitKey := invocation(t)
	var progress childcall.Single
	roundTrip(t, &progress, childcall.PhaseAwaitingStart)
	start := startSignal(t, "child", nil)
	if _, err := progress.AcceptStart(start); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, &progress, childcall.PhaseAwaitingOpening)
	effect, err := progress.WaitEffect(waitKey, agent.ChildWaitBoundaryDrained)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"operation":"wait_children","spec":{"key":"children","children":["child"],"boundary":"subtree_drained","condition":{"kind":"all"}}}`
	var got, expected any
	if decodeErr := jsonv2.Unmarshal(effect.Payload(), &got); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if decodeErr := jsonv2.Unmarshal([]byte(want), &expected); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("wait effect=%s", effect.Payload())
	}
	waitID, err := progress.AcceptOpening(openingSignal(t, "wait"))
	if err != nil || waitID.String() != "wait" {
		t.Fatalf("wait ID=%s error=%v", waitID, err)
	}
	roundTrip(t, &progress, childcall.PhaseAwaitingCompletion)
	result, err := progress.Complete(completionSignal(t, "wait", true, "child"), waitKey, agent.ChildWaitBoundaryDrained)
	if err != nil || result.Result().ProcessID() != progress.ProcessID() || result.Result().Status() != agent.StatusCompleted {
		t.Fatalf("completion=%+v error=%v", result, err)
	}
}

func TestSingleRejectsMismatchedResponsesWithoutAdvancing(t *testing.T) {
	waitKey := invocation(t)
	start := startSignal(t, "child", nil)
	var progress childcall.Single
	if _, err := progress.AcceptStart(start); err != nil {
		t.Fatal(err)
	}
	if _, err := progress.AcceptOpening(openingSignal(t, "wait")); err != nil {
		t.Fatal(err)
	}
	for name, signal := range map[string]agent.Signal{
		"wait ID":    completionSignal(t, "other", true, "child"),
		"boundary":   completionSignal(t, "wait", false, "child"),
		"process ID": completionSignal(t, "wait", true, "other"),
	} {
		t.Run("completion/"+name, func(t *testing.T) {
			if _, err := agent.ParseChildWaitSatisfied(signal); err != nil {
				t.Fatalf("invalid fixture: %v", err)
			}
			if _, err := progress.Complete(signal, waitKey, agent.ChildWaitBoundaryDrained); err == nil {
				t.Fatal("mismatched completion returned a result")
			}
		})
	}
	var extra struct {
		Operation string            `json:"operation"`
		Outcomes  []json.RawMessage `json:"outcomes"`
	}
	if err := jsonv2.Unmarshal(completionSignal(t, "wait", true, "child").Payload(), &extra); err != nil {
		t.Fatal(err)
	}
	var other struct {
		Outcomes []json.RawMessage `json:"outcomes"`
	}
	if err := jsonv2.Unmarshal(completionSignal(t, "wait", true, "other").Payload(), &other); err != nil {
		t.Fatal(err)
	}
	extra.Outcomes = append(extra.Outcomes, other.Outcomes...)
	extraSignal := signal(t, "wait", extra)
	if _, err := agent.ParseChildWaitSatisfied(extraSignal); err != nil {
		t.Fatalf("invalid multi-outcome fixture: %v", err)
	}
	if _, err := progress.Complete(extraSignal, waitKey, agent.ChildWaitBoundaryDrained); err == nil {
		t.Fatal("single-child completion accepted an additional outcome")
	}
	before := string(encoded(t, progress))
	if _, err := progress.AcceptStart(start); err == nil {
		t.Fatal("an open wait accepted a repeated start")
	}
	if _, err := progress.AcceptOpening(openingSignal(t, "other")); err == nil {
		t.Fatal("an open wait accepted a replacement opening")
	}
	if string(encoded(t, progress)) != before {
		t.Fatal("out-of-phase responses changed progress")
	}
}

func TestSingleLeavesStartFailureToStrategy(t *testing.T) {
	failure, err := agent.NewFailure(agent.FailureKindExternal, "child.start.failed", "unavailable")
	if err != nil {
		t.Fatal(err)
	}
	var progress childcall.Single
	result, err := progress.AcceptStart(startSignal(t, "", &failure))
	if err != nil {
		t.Fatal(err)
	}
	got, failed := result.Failure()
	if !failed || got.Code() != failure.Code() || got.Message() != failure.Message() || progress.Phase() != childcall.PhaseAwaitingStart {
		t.Fatal("start failure was converted or installed as a successful child")
	}
}

func TestSingleDrainedCompletionRequiresKnownSubtreeState(t *testing.T) {
	waitKey := invocation(t)
	var progress childcall.Single
	if _, err := progress.AcceptStart(startSignal(t, "child", nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := progress.AcceptOpening(openingSignal(t, "wait")); err != nil {
		t.Fatal(err)
	}
	before := string(encoded(t, progress))
	unknown := completionSignal(t, "wait", false, "child")
	if _, err := progress.Complete(unknown, waitKey, agent.ChildWaitBoundaryDrained); err == nil {
		t.Fatal("drained completion accepted an unknown subtree")
	}
	if string(encoded(t, progress)) != before {
		t.Fatal("rejected completion changed progress")
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
			if err := jsonv2.Unmarshal([]byte(`{"process_id":"original","wait_id":"wait"}`), &progress); err != nil {
				t.Fatal(err)
			}
			before := string(encoded(t, progress))
			if err := progress.UnmarshalJSON([]byte(payload)); err == nil {
				t.Fatal("malformed progress was accepted")
			}
			if string(encoded(t, progress)) != before {
				t.Fatal("failed decode changed the current invocation")
			}
		})
	}
}

func invocation(t *testing.T) agent.WaitKey {
	t.Helper()
	waitKey, err := agent.ParseWaitKey("children")
	if err != nil {
		t.Fatal(err)
	}
	return waitKey
}

func roundTrip(t *testing.T, progress *childcall.Single, want childcall.Phase) {
	t.Helper()
	payload := encoded(t, progress)
	var restored childcall.Single
	if err := jsonv2.Unmarshal(payload, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Phase() != want || string(encoded(t, restored)) != string(payload) {
		t.Fatal("restoration changed the next accepted protocol boundary")
	}
	*progress = restored
}

func startSignal(t *testing.T, process string, failure *agent.Failure) agent.Signal {
	t.Helper()
	return signal(t, "", struct {
		Operation string         `json:"operation"`
		ProcessID string         `json:"process_id,omitempty"`
		Failure   *agent.Failure `json:"failure,omitzero"`
	}{"start_child", process, failure})
}

func openingSignal(t *testing.T, waitID string) agent.Signal {
	t.Helper()
	return signal(t, waitID, json.RawMessage(`{"operation":"child_wait_opened"}`))
}

// completionSignal answers waitID with processID's completed outcome, carrying
// an empty descendant list exactly when drained.
func completionSignal(t *testing.T, waitID string, drained bool, processID string) agent.Signal {
	t.Helper()
	subtree := ""
	if drained {
		subtree = `,"descendant_unresolved_effects":[]`
	}
	payload := fmt.Sprintf(`{"operation":"child_wait_satisfied","outcomes":[{"result":{"process_id":%q,"started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:01Z","output":7,"termination":{"cause":"completion"},"usage":{"committed_steps":0,"prepared_effects":0,"accepted_signals":0,"dropped_deltas":0}}%s}]}`, processID, subtree)
	return signal(t, waitID, json.RawMessage(payload))
}

func signal(t *testing.T, waitID string, payload any) agent.Signal {
	t.Helper()
	data := encoded(t, struct {
		ID      string `json:"id"`
		WaitID  string `json:"wait_id,omitempty"`
		Payload any    `json:"payload"`
	}{"signal:engine:childcall", waitID, payload})
	var value agent.Signal
	if err := jsonv2.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func encoded(t *testing.T, value any) []byte {
	t.Helper()
	data, err := jsonv2.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSingleOwnsWindowShape(t *testing.T) {
	var progress childcall.Single
	start := startSignal(t, "child", nil)
	opening := openingSignal(t, "wait")
	completion := completionSignal(t, "wait", true, "child")
	var foreign agent.Signal
	if err := jsonv2.Unmarshal([]byte(`{"id":"signal:external","payload":{}}`), &foreign); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []childcall.Phase{childcall.PhaseAwaitingStart, childcall.PhaseAwaitingOpening, childcall.PhaseAwaitingCompletion} {
		frame := start
		if phase == childcall.PhaseAwaitingOpening {
			frame = opening
		}
		if phase == childcall.PhaseAwaitingCompletion {
			frame = completion
		}
		if _, err := progress.Window([]agent.Signal{frame}); err != nil {
			t.Fatal(err)
		}
		for _, window := range [][]agent.Signal{nil, {foreign}, {frame, frame}, {foreign, frame}, {frame, foreign}, {frame, frame, frame}} {
			if _, err := progress.Window(window); err == nil {
				t.Fatalf("phase=%v accepted invalid window", phase)
			}
		}
		switch phase {
		case childcall.PhaseAwaitingStart:
			if _, err := progress.AcceptStart(start); err != nil {
				t.Fatal(err)
			}
		case childcall.PhaseAwaitingOpening:
			if _, err := progress.Window([]agent.Signal{opening, completion}); err != nil {
				t.Fatal(err)
			}
			if _, err := progress.AcceptOpening(opening); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestSingleRejectsUndecodableResponsesWithoutAdvancing(t *testing.T) {
	waitKey := invocation(t)
	foreign := signal(t, "", json.RawMessage(`{"operation":"unrelated"}`))
	var progress childcall.Single
	if _, err := progress.WaitEffect(waitKey, agent.ChildWaitBoundaryDrained); err == nil {
		t.Fatal("wait preceded the child start")
	}
	if _, err := progress.AcceptStart(foreign); err == nil || progress.Phase() != childcall.PhaseAwaitingStart {
		t.Fatal("undecodable start advanced progress")
	}
	if _, err := progress.AcceptStart(startSignal(t, "child", nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := progress.AcceptOpening(signal(t, "wait", json.RawMessage(`{"operation":"unrelated"}`))); err == nil || progress.Phase() != childcall.PhaseAwaitingOpening {
		t.Fatal("undecodable opening advanced progress")
	}
	if _, err := progress.Complete(completionSignal(t, "wait", true, "child"), waitKey, agent.ChildWaitBoundaryDrained); err == nil {
		t.Fatal("completion preceded its opening")
	}
	if _, err := progress.AcceptOpening(openingSignal(t, "wait")); err != nil {
		t.Fatal(err)
	}
	if _, err := progress.Complete(signal(t, "wait", json.RawMessage(`{"operation":"unrelated"}`)), waitKey, agent.ChildWaitBoundaryDrained); err == nil {
		t.Fatal("undecodable completion returned a result")
	}
}
