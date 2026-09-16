package childcall_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

func TestBatchAdoptsOnlyCorrelatedOrderedResponses(t *testing.T) {
	ref, key, waitKey := invocation(t)
	second, _ := agent.ParseChildKey("second")
	failedKey, _ := agent.ParseChildKey("failed")
	batch := childcall.Batch{Children: []childcall.Child{{Key: key, Deployment: ref}, {Key: second, Deployment: ref}, {Key: failedKey, Deployment: ref}}}
	failure, _ := agent.NewFailure(agent.FailureKindExternal, "start.denied", "denied")
	starts := []agent.ChildStartResult{
		batchStart(t, startSignal(t, ref, key, "child", nil)),
		batchStart(t, startSignal(t, ref, second, "second-child", nil)),
		batchStart(t, startSignal(t, ref, failedKey, "", &failure)),
	}
	if batch.Phase() != childcall.AwaitingStart || batch.PendingStarts() != 3 {
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
	if batch.Phase() != childcall.AwaitingOpening || batch.PendingStarts() != 0 {
		t.Fatal("settled starts did not reach opening")
	}
	spec, err := batch.WaitSpec(waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren())
	if err != nil || len(spec.Children) != 2 || spec.Children[0].String() != "child" || spec.Children[1].String() != "second-child" {
		t.Fatalf("wait = %+v, %v", spec, err)
	}
	opened, err := agent.ParseChildWaitOpened(openingSignal(t, "wait", "children", "subtree_drained", []string{"child", "second-child"}, "all"))
	if err != nil {
		t.Fatal(err)
	}
	waitID, err := batch.AcceptOpening(opened, waitKey, spec.Boundary, spec.Condition)
	if err != nil {
		t.Fatal(err)
	}
	batch.WaitID = waitID
	if batch.Phase() != childcall.AwaitingCompletion {
		t.Fatal("open wait did not reach completion")
	}
	first := batchCompletion(t, "wait", "children", "subtree_drained", [][2]string{{"call", "child"}})
	secondResult := batchCompletion(t, "wait", "children", "subtree_drained", [][2]string{{"second", "second-child"}})
	all := batchCompletion(t, "wait", "children", "subtree_drained", [][2]string{{"call", "child"}, {"second", "second-child"}})
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
		"wrong wait":      batchCompletion(t, "other", "children", "subtree_drained", [][2]string{{"call", "child"}}),
		"wrong key":       batchCompletion(t, "wait", "children", "subtree_drained", [][2]string{{"wrong", "child"}}),
		"foreign process": batchCompletion(t, "wait", "children", "subtree_drained", [][2]string{{"call", "foreign"}}),
		"reversed":        batchCompletion(t, "wait", "children", "subtree_drained", [][2]string{{"second", "second-child"}, {"call", "child"}}),
		"wrong boundary":  batchCompletion(t, "wait", "children", "terminal_result", [][2]string{{"call", "child"}}),
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
	ref, key, waitKey := invocation(t)
	id, _ := agent.ParseProcessID("child")
	waitID, _ := agent.ParseWaitID("wait")
	second, _ := agent.ParseChildKey("second")
	pending := childcall.Batch{Children: []childcall.Child{{Key: key, Deployment: ref}, {Key: second, Deployment: ref}}}
	start := batchStart(t, startSignal(t, ref, key, "child", nil))
	duplicate := batchStart(t, startSignal(t, ref, second, "child", nil))
	for _, starts := range [][]agent.ChildStartResult{nil, {duplicate}, {start, duplicate}, {start, start, duplicate}} {
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
	opened, err := agent.ParseChildWaitOpened(openingSignal(t, "wait", "children", "subtree_drained", []string{"child"}, "all"))
	if err != nil {
		t.Fatal(err)
	}
	completed := batchCompletion(t, "wait", "children", "subtree_drained", [][2]string{{"call", "child"}})
	if _, err := pending.WaitSpec(waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren()); err == nil {
		t.Fatal("wait preceded admission")
	}
	if _, err := pending.AcceptOpening(opened, waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren()); err == nil {
		t.Fatal("opening preceded admission")
	}
	if _, err := pending.Complete(completed, waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren()); err == nil {
		t.Fatal("completion preceded opening")
	}
	active := childcall.Batch{Children: []childcall.Child{{Key: key, ProcessID: id}}}
	if _, err := active.AcceptStarts([]agent.ChildStartResult{start}); err == nil {
		t.Fatal("repeated start accepted")
	}
	if _, err := active.AcceptOpening(opened, waitKey, agent.ChildWaitBoundaryDrained, agent.AnyChild()); err == nil {
		t.Fatal("wrong wait condition accepted")
	}
	if _, err := active.AcceptOpening(opened, agent.WaitKey{}, agent.ChildWaitBoundaryDrained, agent.AllChildren()); err == nil {
		t.Fatal("invalid wait accepted")
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
	_, key, waitKey := invocation(t)
	id, _ := agent.ParseProcessID("child")
	waitID, _ := agent.ParseWaitID("wait")
	for _, batch := range []childcall.Batch{
		{Children: []childcall.Child{{}}},
		{Children: []childcall.Child{{Key: key}, {Key: key}}},
		{Children: []childcall.Child{{Key: key, ProcessID: id}, {Key: key, ProcessID: id}}, WaitID: waitID},
	} {
		want := batch.Validate()
		if want == nil {
			t.Fatal("fixture must be structurally invalid")
		}
		_, startErr := batch.AcceptStarts(nil)
		_, specErr := batch.WaitSpec(waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren())
		_, openErr := batch.AcceptOpening(agent.ChildWaitOpened{}, waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren())
		_, completionErr := batch.Complete(agent.ChildWaitSatisfied{}, waitKey, agent.ChildWaitBoundaryDrained, agent.AllChildren())
		_, outcomeErr := batch.MatchOutcomes(nil)
		for operation, err := range map[string]error{"start": startErr, "wait": specErr, "opening": openErr, "completion": completionErr, "outcomes": outcomeErr} {
			if err == nil || err.Error() != want.Error() {
				t.Errorf("%s error = %v, want structural error %v", operation, err, want)
			}
		}
	}
}

func TestBatchMatchesRetainedOutcomesWithoutAWait(t *testing.T) {
	_, key, _ := invocation(t)
	id, _ := agent.ParseProcessID("child")
	batch := childcall.Batch{Children: []childcall.Child{{Key: key, ProcessID: id}}}
	outcomes := batchCompletion(t, "wait", "children", "subtree_drained", [][2]string{{"call", "child"}}).Outcomes()
	if indices, err := batch.MatchOutcomes(outcomes); err != nil || !slices.Equal(indices, []int{0}) {
		t.Fatalf("retained outcomes = %v, %v", indices, err)
	}
	for _, invalid := range [][]agent.ChildOutcome{{{}}, {outcomes[0], outcomes[0]}} {
		if indices, err := batch.MatchOutcomes(invalid); err == nil || indices != nil {
			t.Fatalf("invalid outcomes = %v, %v", indices, err)
		}
	}
}

func batchCompletion(t *testing.T, waitID, key, boundary string, children [][2]string) agent.ChildWaitSatisfied {
	t.Helper()
	var outcomes []agent.ChildOutcome
	for _, child := range children {
		value, err := agent.ParseChildWaitSatisfied(completionSignal(t, waitID, key, boundary, child[0], child[1]))
		if err != nil {
			t.Fatal(err)
		}
		outcomes = append(outcomes, value.Outcomes()...)
	}
	payload := struct {
		Operation string               `json:"operation"`
		Key       string               `json:"key"`
		Boundary  string               `json:"boundary"`
		Outcomes  []agent.ChildOutcome `json:"outcomes"`
	}{"child_wait_satisfied", key, boundary, outcomes}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	value, err := agent.ParseChildWaitSatisfied(signal(t, waitID, json.RawMessage(data)))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
