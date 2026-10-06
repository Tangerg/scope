package childcall_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

func TestBatchAdoptsOnlyCorrelatedOrderedResponses(t *testing.T) {
	waitKey := invocation(t)
	key, _ := agent.ParseChildKey("call")
	second, _ := agent.ParseChildKey("second")
	failedKey, _ := agent.ParseChildKey("failed")
	batch := childcall.Batch{Children: []childcall.Child{{Key: key}, {Key: second}, {Key: failedKey}}}
	failure, _ := agent.NewFailure(agent.FailureKindExternal, "start.denied", "denied")
	starts := []agent.ChildStartResult{
		batchStart(t, startSignal(t, "child", nil)),
		batchStart(t, startSignal(t, "second-child", nil)),
		batchStart(t, startSignal(t, "", &failure)),
	}
	if batch.Phase() != childcall.PhaseAwaitingStart || batch.PendingStarts() != 3 {
		t.Fatal("missing pending starts")
	}
	before := slices.Clone(batch.Children)
	if indices, err := batch.AcceptStarts(starts[:1]); err != nil || !slices.Equal(indices, []int{0}) {
		t.Fatalf("partial starts = %v, %v", indices, err)
	}
	if indices, err := batch.AcceptStarts(starts); err != nil || !slices.Equal(indices, []int{0, 1, 2}) {
		t.Fatalf("starts = %v, %v", indices, err)
	}
	if !reflect.DeepEqual(batch.Children, before) {
		t.Fatal("validation mutated authoritative progress")
	}
	for index, start := range starts {
		id, started := start.ProcessID()
		batch.Children[index].ProcessID, batch.Children[index].Done = id, !started
	}
	if batch.Phase() != childcall.PhaseAwaitingOpening || batch.PendingStarts() != 0 {
		t.Fatal("settled starts did not reach opening")
	}
	spec, err := batch.WaitSpec(waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren())
	if err != nil || len(spec.Children) != 2 || spec.Children[0].String() != "child" || spec.Children[1].String() != "second-child" {
		t.Fatalf("wait = %+v, %v", spec, err)
	}
	opened, err := agent.ParseChildWaitOpened(openingSignal(t, "wait"))
	if err != nil {
		t.Fatal(err)
	}
	waitID, err := batch.AcceptOpening(opened)
	if err != nil {
		t.Fatal(err)
	}
	batch.WaitID = waitID
	if batch.Phase() != childcall.PhaseAwaitingCompletion {
		t.Fatal("open wait did not reach completion")
	}
	first := batchCompletion(t, "wait", true, "child")
	secondResult := batchCompletion(t, "wait", true, "second-child")
	all := batchCompletion(t, "wait", true, "child", "second-child")
	if indices, err := batch.Complete(all, waitKey, spec.Boundary, spec.Condition); err != nil || !slices.Equal(indices, []int{0, 1}) {
		t.Fatalf("all = %v, %v", indices, err)
	}
	if _, err := batch.Complete(first, waitKey, spec.Boundary, spec.Condition); err == nil {
		t.Fatal("partial response satisfied all")
	}
	if indices, err := batch.Complete(secondResult, waitKey, spec.Boundary, agent.AnyChild()); err != nil || !slices.Equal(indices, []int{1}) {
		t.Fatalf("any = %v, %v", indices, err)
	}
	quorum, _ := agent.ChildQuorum(2)
	if _, err := batch.Complete(first, waitKey, spec.Boundary, quorum); err == nil {
		t.Fatal("partial response satisfied quorum")
	}
	if _, err := batch.Complete(all, waitKey, spec.Boundary, quorum); err != nil {
		t.Fatal(err)
	}
	for name, completed := range map[string]agent.ChildWaitSatisfied{
		"wrong wait":      batchCompletion(t, "other", true, "child"),
		"foreign process": batchCompletion(t, "wait", true, "foreign"),
		"reversed":        batchCompletion(t, "wait", true, "second-child", "child"),
		"wrong boundary":  batchCompletion(t, "wait", false, "child"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := batch.Complete(completed, waitKey, spec.Boundary, agent.AnyChild()); err == nil {
				t.Fatal("foreign completion accepted")
			}
		})
	}
	batch.Children[0].Done = true
	if _, err := batch.Complete(first, waitKey, spec.Boundary, agent.AnyChild()); err == nil {
		t.Fatal("repeated outcome accepted")
	}
	if indices, err := batch.Complete(secondResult, waitKey, spec.Boundary, agent.AnyChild()); err != nil || !slices.Equal(indices, []int{1}) {
		t.Fatalf("remaining = %v, %v", indices, err)
	}
}

func TestBatchRejectsMalformedAndOutOfPhaseProgress(t *testing.T) {
	waitKey := invocation(t)
	key, _ := agent.ParseChildKey("call")
	id, _ := agent.ParseProcessID("child")
	waitID, _ := agent.ParseWaitID("wait")
	second, _ := agent.ParseChildKey("second")
	pending := childcall.Batch{Children: []childcall.Child{{Key: key}, {Key: second}}}
	start := batchStart(t, startSignal(t, "child", nil))
	duplicate := batchStart(t, startSignal(t, "child", nil))
	for _, starts := range [][]agent.ChildStartResult{nil, {start, duplicate}, {start, start, duplicate}} {
		if _, err := pending.AcceptStarts(starts); err == nil {
			t.Fatal("invalid starts accepted")
		}
	}
	one := childcall.Batch{Children: pending.Children[:1]}
	if _, err := one.AcceptStarts([]agent.ChildStartResult{start, duplicate}); err == nil {
		t.Fatal("excess starts accepted")
	}
	pending.Children[0].ProcessID, pending.Children[0].Done = id, true
	if _, err := pending.AcceptStarts([]agent.ChildStartResult{duplicate}); err == nil {
		t.Fatal("prior Process identity reused")
	}
	opened, err := agent.ParseChildWaitOpened(openingSignal(t, "wait"))
	if err != nil {
		t.Fatal(err)
	}
	completed := batchCompletion(t, "wait", true, "child")
	if _, err := pending.WaitSpec(waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren()); err == nil {
		t.Fatal("wait preceded admission")
	}
	if _, err := pending.AcceptOpening(opened); err == nil {
		t.Fatal("opening preceded admission")
	}
	if _, err := pending.Complete(completed, waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren()); err == nil {
		t.Fatal("completion preceded opening")
	}
	active := childcall.Batch{Children: []childcall.Child{{Key: key, ProcessID: id}}}
	if _, err := active.AcceptStarts([]agent.ChildStartResult{start}); err == nil {
		t.Fatal("repeated start accepted")
	}
	if _, err := active.AcceptOpening(agent.ChildWaitOpened{}); err == nil {
		t.Fatal("invalid opening accepted")
	}
	for _, batch := range []childcall.Batch{
		{Children: []childcall.Child{{}}},
		{Children: []childcall.Child{{Key: key}, {Key: key}}},
		{Children: []childcall.Child{{Key: key, ProcessID: id}, {Key: second, ProcessID: id}}},
		{Children: []childcall.Child{{Key: key}}, WaitID: waitID},
	} {
		if batch.Validate() == nil {
			t.Fatal("malformed progress accepted")
		}
		if _, err := batch.AcceptStarts([]agent.ChildStartResult{start}); err == nil {
			t.Fatal("malformed start progress accepted")
		}
		if _, err := batch.WaitSpec(waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren()); err == nil {
			t.Fatal("malformed wait progress accepted")
		}
	}
	active.Children = append(active.Children, active.Children[0])
	active.WaitID = waitID
	if _, err := active.Complete(completed, waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren()); err == nil {
		t.Fatal("malformed completion progress accepted")
	}
}

func batchStart(t *testing.T, signal agent.Signal) agent.ChildStartResult {
	t.Helper()
	start, err := agent.ParseChildStartResult(signal)
	if err != nil {
		t.Fatal(err)
	}
	return start
}

func TestBatchStructuralErrorsPrecedePhaseErrors(t *testing.T) {
	waitKey := invocation(t)
	key, _ := agent.ParseChildKey("call")
	id, _ := agent.ParseProcessID("child")
	waitID, _ := agent.ParseWaitID("wait")
	for _, batch := range []childcall.Batch{
		{Children: []childcall.Child{{}}},
		{Children: []childcall.Child{{Key: key}, {Key: key}}},
		{Children: []childcall.Child{{Key: key, ProcessID: id}, {Key: key, ProcessID: id}}, WaitID: waitID},
	} {
		if !errors.Is(batch.Validate(), childcall.ErrInvalidBatch) {
			t.Fatal("fixture must be structurally invalid")
		}
		_, startErr := batch.AcceptStarts(nil)
		_, specErr := batch.WaitSpec(waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren())
		_, openErr := batch.AcceptOpening(agent.ChildWaitOpened{})
		_, completionErr := batch.Complete(agent.ChildWaitSatisfied{}, waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren())
		_, outcomeErr := batch.MatchOutcomes(nil)
		for operation, err := range map[string]error{"start": startErr, "wait": specErr, "opening": openErr, "completion": completionErr, "outcomes": outcomeErr} {
			if !errors.Is(err, childcall.ErrInvalidBatch) {
				t.Errorf("%s error = %v, want structural error %v", operation, err, childcall.ErrInvalidBatch)
			}
		}
	}
}

func TestBatchMatchesRetainedOutcomesWithoutAWait(t *testing.T) {
	key, _ := agent.ParseChildKey("call")
	id, _ := agent.ParseProcessID("child")
	batch := childcall.Batch{Children: []childcall.Child{{Key: key, ProcessID: id}}}
	outcomes := batchCompletion(t, "wait", true, "child").Outcomes()
	if indices, err := batch.MatchOutcomes(outcomes); err != nil || !slices.Equal(indices, []int{0}) {
		t.Fatalf("retained outcomes = %v, %v", indices, err)
	}
	for _, invalid := range [][]agent.ChildOutcome{{{}}, {outcomes[0], outcomes[0]}} {
		if indices, err := batch.MatchOutcomes(invalid); err == nil || indices != nil {
			t.Fatalf("invalid outcomes = %v, %v", indices, err)
		}
	}
}

func batchCompletion(t *testing.T, waitID string, drained bool, processIDs ...string) agent.ChildWaitSatisfied {
	t.Helper()
	var outcomes []agent.ChildOutcome
	for _, processID := range processIDs {
		value, err := agent.ParseChildWaitSatisfied(completionSignal(t, waitID, drained, processID))
		if err != nil {
			t.Fatal(err)
		}
		outcomes = append(outcomes, value.Outcomes()...)
	}
	boundary := agent.ChildWaitBoundaryResult
	if drained {
		boundary = agent.ChildWaitBoundaryDrained
	}
	payload := struct {
		Operation string                  `json:"operation"`
		Boundary  agent.ChildWaitBoundary `json:"boundary"`
		Outcomes  []agent.ChildOutcome    `json:"outcomes"`
	}{"child_wait_satisfied", boundary, outcomes}
	data, err := jsonv2.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	value, err := agent.ParseChildWaitSatisfied(signal(t, waitID, json.RawMessage(data)))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestBatchRejectsInvalidResponses(t *testing.T) {
	waitKey := invocation(t)
	key, _ := agent.ParseChildKey("call")
	pending := childcall.Batch{Children: []childcall.Child{{Key: key}}}
	if _, err := pending.AcceptStarts([]agent.ChildStartResult{{}}); err == nil {
		t.Fatal("invalid start accepted")
	}
	id, _ := agent.ParseProcessID("child")
	active := childcall.Batch{Children: []childcall.Child{{Key: key, ProcessID: id}}}
	if _, err := active.Complete(batchCompletion(t, "wait", true, "child"), agent.WaitKey{}, agent.ChildWaitBoundaryDrained, agent.AllChildren()); err == nil {
		t.Fatal("completion with an invalid wait accepted")
	}
	if _, err := active.Complete(batchCompletion(t, "wait", true, "child"), waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren()); err == nil {
		t.Fatal("completion preceded its opening")
	}
	if _, err := active.MatchOutcomes([]agent.ChildOutcome{{}}); err == nil {
		t.Fatal("invalid outcome matched")
	}
}
