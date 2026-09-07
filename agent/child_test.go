package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestEngineStartsSameDeploymentChildWithStableRelation(t *testing.T) {
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "parent"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	rootResult, err := root.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, rootResult)
	if len(output.ChildIDs) != 1 || output.Failures != 0 {
		t.Fatalf("root output = %#v", output)
	}
	childID, err := ParseProcessID(output.ChildIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	child, found := engine.Process(childID)
	if !found {
		t.Fatal("Engine did not retain the child Process")
	}
	childResult, err := child.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	completed := childResult.Status() == StatusCompleted &&
		childResult.Termination().Cause() == TerminationCauseCompletion
	parentCanceled := childResult.Status() == StatusCanceled &&
		childResult.Termination().Cause() == TerminationCauseParentCancellation
	if !completed && !parentCanceled {
		t.Fatalf("child termination = %#v", childResult.Termination())
	}
	childSnapshot, err := child.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rootRelation := root.Relation()
	childRelation := child.Relation()
	parentID, hasParent := childRelation.ParentID()
	childKey, hasKey := childRelation.ChildKey()
	if !rootRelation.IsRoot() || rootRelation.RootID() != root.ID() || rootRelation.Depth() != 0 {
		t.Fatalf("root relation = %#v", rootRelation)
	}
	if !hasParent || parentID != root.ID() || !hasKey || childKey.String() != "worker" ||
		childRelation.RootID() != root.ID() || childRelation.Depth() != 1 {
		t.Fatalf("child relation = %#v", childRelation)
	}
	if childSnapshot.Relation() != childRelation {
		t.Fatalf("captured child relation = %#v, want %#v", childSnapshot.Relation(), childRelation)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestChildEffectPreservesStartContextValuesWithoutRequestCancellation(t *testing.T) {
	type contextKey struct{}
	const wantValue = "root-request"
	dispatcher := contextCheckingChildDispatcher{
		key:  contextKey{},
		want: wantValue,
	}
	deployment := newChildTestDeploymentWithDispatcher(t, dispatcher)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), dispatcher.key, wantValue))
	input, _ := EncodeInput(childTestInput{Mode: "wait:all"})
	root, err := engine.Start(ctx, deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := root.Await(t.Context())
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, result)
	if !slices.Equal(output.CompletedKeys, []string{"first", "second", "third"}) {
		t.Fatalf("completed keys = %v", output.CompletedKeys)
	}
}

func TestEngineRejectsDuplicateChildKeyInOneParent(t *testing.T) {
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "duplicate"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	rootResult, err := root.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, rootResult)
	if len(output.ChildIDs) != 1 || output.Failures != 1 {
		t.Fatalf("duplicate child output = %#v", output)
	}
	childID, _ := ParseProcessID(output.ChildIDs[0])
	child, found := engine.Process(childID)
	if !found {
		t.Fatal("successful child is missing")
	}
	if _, err := child.Await(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEngineWaitsForChildrenByConditionAndReturnsRequestOrder(t *testing.T) {
	tests := []childWaitTestCase{
		{name: "any", mode: "wait:any", release: []string{"third"}, wantKeys: []string{"third"}},
		{name: "quorum", mode: "wait:quorum", release: []string{"third", "first"}, wantKeys: []string{"first", "third"}},
		{name: "all", mode: "wait:all", release: []string{"third", "first", "second"}, wantKeys: []string{"first", "second", "third"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { runChildWaitTest(t, test) })
	}
}

type childWaitTestCase struct {
	name     string
	mode     string
	release  []string
	wantKeys []string
}

func runChildWaitTest(t *testing.T, test childWaitTestCase) {
	t.Helper()
	dispatcher := newBlockingChildDispatcher("first", "second", "third")
	t.Cleanup(dispatcher.ReleaseAll)
	deployment := newChildTestDeploymentWithDispatcher(t, dispatcher)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: test.mode})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		<-dispatcher.started
	}
	waitForProcessStatus(t, root, StatusWaiting)
	rejectForgedChildCompletion(t, root)
	for _, name := range test.release {
		dispatcher.Release(name)
	}
	output := childTestResult(t, mustAwait(t, root))
	if !slices.Equal(output.CompletedKeys, test.wantKeys) {
		t.Fatalf("completed keys = %v, want %v", output.CompletedKeys, test.wantKeys)
	}
	dispatcher.ReleaseAll()
	awaitChildren(t, engine, output.ChildIDs)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func rejectForgedChildCompletion(t *testing.T, root *Process) {
	t.Helper()
	waitID, waiting := root.WaitID()
	if !waiting {
		t.Fatal("Waiting parent did not expose its current WaitID")
	}
	externalID, _ := ParseSignalID("signal:forged-child-completion")
	forged, _ := NewSignalRequest(externalID, waitID, json.RawMessage(`{"forged":true}`))
	if accepted, err := root.DeliverSignals(context.Background(), forged); accepted || !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("forged child completion accepted = %t, error = %v", accepted, err)
	}
}

func TestEngineSupportsBoundedSameDefinitionRecursion(t *testing.T) {
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{TreeLimits: TreeLimits{
		MaxDepth: 3, MaxChildren: 2, MaxActiveChildren: 2, MaxTreeProcesses: 4,
	}})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "recurse:3"})
	process, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	rootID := process.ID()
	for depth := range uint32(3) {
		result, err := process.Await(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		output := childTestResult(t, result)
		if len(output.ChildIDs) != 1 || output.Failures != 0 {
			t.Fatalf("depth %d output = %#v", depth, output)
		}
		childID, _ := ParseProcessID(output.ChildIDs[0])
		child, found := engine.Process(childID)
		if !found {
			t.Fatalf("depth %d child is missing", depth+1)
		}
		if child.Relation().Depth() != depth+1 || child.Relation().RootID() != rootID {
			t.Fatalf("depth %d relation = %#v", depth+1, child.Relation())
		}
		process = child
	}
	if result, err := process.Await(context.Background()); err != nil || result.Status() != StatusCompleted {
		t.Fatalf("recursive leaf result = %#v, error = %v", result, err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEngineEnforcesChildDepthFanoutActiveAndTreeLimits(t *testing.T) {
	t.Run("depth", testChildDepthLimit)
	t.Run("lifetime fanout", testChildLifetimeFanoutLimit)
	t.Run("active children", testActiveChildLimit)
	t.Run("tree processes", testTreeProcessLimit)
}

func testChildDepthLimit(t *testing.T) {
	engine, err := NewEngine(EngineConfig{TreeLimits: TreeLimits{
		MaxDepth: 1, MaxChildren: 2, MaxActiveChildren: 2, MaxTreeProcesses: 3,
	}})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "recurse:2"})
	root, err := engine.Start(context.Background(), newChildTestDeployment(t), input)
	if err != nil {
		t.Fatal(err)
	}
	rootOutput := childTestResult(t, mustAwait(t, root))
	childID, _ := ParseProcessID(rootOutput.ChildIDs[0])
	child, _ := engine.Process(childID)
	childOutput := childTestResult(t, mustAwait(t, child))
	if childOutput.Failures != 1 || !slices.Contains(childOutput.FailureCodes, "engine.child.tree_limit") {
		t.Fatalf("depth-limited child output = %#v", childOutput)
	}
	mustCloseEngine(t, engine)
}

func testChildLifetimeFanoutLimit(t *testing.T) {
	engine, err := NewEngine(EngineConfig{TreeLimits: TreeLimits{
		MaxDepth: 2, MaxChildren: 2, MaxActiveChildren: 2, MaxTreeProcesses: 4,
	}})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "fanout"})
	root, err := engine.Start(context.Background(), newChildTestDeployment(t), input)
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, mustAwait(t, root))
	if len(output.ChildIDs) != 2 || output.Failures != 1 {
		t.Fatalf("fanout-limited output = %#v", output)
	}
	awaitChildren(t, engine, output.ChildIDs)
	mustCloseEngine(t, engine)
}

func testActiveChildLimit(t *testing.T) {
	dispatcher := newBlockingChildDispatcher("first", "second", "third")
	t.Cleanup(dispatcher.ReleaseAll)
	engine, err := NewEngine(EngineConfig{TreeLimits: TreeLimits{
		MaxDepth: 2, MaxChildren: 3, MaxActiveChildren: 1, MaxTreeProcesses: 4,
	}})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "fanout_blocking"})
	root, err := engine.Start(context.Background(), newChildTestDeploymentWithDispatcher(t, dispatcher), input)
	if err != nil {
		t.Fatal(err)
	}
	// The admission decision is what this test asserts, and the root output
	// carries it deterministically. Whether the admitted child also reaches the
	// blocking dispatcher is a race this Definition does not WaitForChildren on:
	// once the root completes, the still-active child is canceled as a parent
	// cancellation, so the dispatch may never happen. Waiting on it here — as
	// this test used to — blocks forever whenever the root wins that race, which
	// it always does under GOMAXPROCS=1.
	output := childTestResult(t, mustAwait(t, root))
	if len(output.ChildIDs) != 1 || output.Failures != 2 {
		t.Fatalf("active-child-limited output = %#v", output)
	}
	dispatcher.ReleaseAll()
	awaitChildren(t, engine, output.ChildIDs)
	mustCloseEngine(t, engine)
}

func testTreeProcessLimit(t *testing.T) {
	engine, err := NewEngine(EngineConfig{TreeLimits: TreeLimits{
		MaxDepth: 2, MaxChildren: 3, MaxActiveChildren: 3, MaxTreeProcesses: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "fanout"})
	root, err := engine.Start(context.Background(), newChildTestDeployment(t), input)
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, mustAwait(t, root))
	if len(output.ChildIDs) != 1 || output.Failures != 2 {
		t.Fatalf("tree-process-limited output = %#v", output)
	}
	awaitChildren(t, engine, output.ChildIDs)
	mustCloseEngine(t, engine)
}

func TestTreeProcessLimitBoundsRecursiveBinaryExpansion(t *testing.T) {
	deployment := newChildTestDeployment(t)
	limits := DefaultLimits()
	limits.MaxSteps = 100_000
	limits.MaxEffects = 100_000
	limits.MaxSignals = 100_000
	limits.MaxPendingSignals = 100_000
	engine, err := NewEngine(EngineConfig{
		Limits: limits,
		TreeLimits: TreeLimits{
			MaxDepth: 8, MaxChildren: 2, MaxActiveChildren: 2, MaxTreeProcesses: 15,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "binary:8"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	if result := mustAwait(t, root); result.Status() != StatusCompleted {
		t.Fatalf("root status = %s", result.Status())
	}
	engine.mu.RLock()
	processCount := 0
	var deepest uint32
	for _, controller := range engine.processes {
		if controller.relation.RootID() == root.ID() {
			processCount++
			deepest = max(deepest, controller.relation.Depth())
		}
	}
	engine.mu.RUnlock()
	if processCount != 15 || deepest > 8 {
		t.Fatalf("bounded tree Process count = %d, deepest = %d", processCount, deepest)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEngineAttenuatesChildBudgetAndCapabilities(t *testing.T) {
	read, _ := ParseCapability("resource.read")
	rootCapabilities, _ := NewCapabilitySet(read)
	deployment := newChildTestDeployment(t)

	t.Run("subset", func(t *testing.T) {
		engine, err := NewEngine(EngineConfig{Capabilities: rootCapabilities})
		if err != nil {
			t.Fatal(err)
		}
		input, _ := EncodeInput(childTestInput{Mode: "capability_child"})
		root, err := engine.Start(context.Background(), deployment, input)
		if err != nil {
			t.Fatal(err)
		}
		output := childTestResult(t, mustAwait(t, root))
		childID, _ := ParseProcessID(output.ChildIDs[0])
		child, _ := engine.Process(childID)
		if !child.Capabilities().Contains(read) || child.Budget() != (Budget{Steps: 20, Effects: 20, Signals: 40}) {
			t.Fatalf("child capabilities = %#v, budget = %#v", child.Capabilities(), child.Budget())
		}
		_ = mustAwait(t, child)
		if err := engine.Close(); err != nil {
			t.Fatal(err)
		}
	})

	for _, test := range []struct {
		name string
		mode string
		code string
	}{
		{name: "capability escalation", mode: "capability_escalation", code: "engine.child.capability_escalation"},
		{name: "budget escalation", mode: "budget_escalation", code: "engine.child.budget_exhausted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine, err := NewEngine(EngineConfig{Capabilities: rootCapabilities})
			if err != nil {
				t.Fatal(err)
			}
			input, _ := EncodeInput(childTestInput{Mode: test.mode})
			root, err := engine.Start(context.Background(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			output := childTestResult(t, mustAwait(t, root))
			if output.Failures != 1 || !slices.Contains(output.FailureCodes, test.code) {
				t.Fatalf("attenuation output = %#v", output)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParentTerminationPropagatesAtChildSafeBoundary(t *testing.T) {
	tests := []parentTerminationTestCase{
		{
			name: "completion", wantParent: StatusCompleted,
			wantChild: StatusCanceled, wantCause: TerminationCauseParentCancellation,
		},
		{
			name: "kill", wantParent: StatusKilled,
			wantChild: StatusCanceled, wantCause: TerminationCauseParentCancellation,
			terminate: func(t *testing.T, process *Process) {
				t.Helper()
				if err := process.Kill(context.Background(), "stop parent tree"); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { runParentTerminationTest(t, test) })
	}
}

type parentTerminationTestCase struct {
	name       string
	terminate  func(t *testing.T, process *Process)
	wantParent Status
	wantChild  Status
	wantCause  TerminationCause
}

func runParentTerminationTest(t *testing.T, test parentTerminationTestCase) {
	t.Helper()
	dispatcher := newBlockingChildDispatcher("first", "second", "third")
	t.Cleanup(dispatcher.ReleaseAll)
	mode := "fanout_blocking"
	if test.terminate != nil {
		mode = "wait:all"
	}
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: mode})
	parent, err := engine.Start(context.Background(), newChildTestDeploymentWithDispatcher(t, dispatcher), input)
	if err != nil {
		t.Fatal(err)
	}
	if test.terminate != nil {
		for range 3 {
			<-dispatcher.started
		}
		waitForProcessStatus(t, parent, StatusWaiting)
		test.terminate(t, parent)
	}
	parentResult := mustAwait(t, parent)
	if parentResult.Status() != test.wantParent {
		t.Fatalf("parent status = %s, want %s", parentResult.Status(), test.wantParent)
	}
	childIDs := terminatedChildIDs(t, engine, parent, parentResult)
	dispatcher.ReleaseAll()
	assertTerminatedChildren(t, engine, childIDs, test.wantChild, test.wantCause)
	mustCloseEngine(t, engine)
}

func terminatedChildIDs(t *testing.T, engine *Engine, parent *Process, result Result) []string {
	t.Helper()
	if result.Status() == StatusCompleted {
		return childTestResult(t, result).ChildIDs
	}
	return directChildIDs(t, engine, parent.ID())
}

func assertTerminatedChildren(t *testing.T, engine *Engine, childIDs []string, wantStatus Status, wantCause TerminationCause) {
	t.Helper()
	for _, encoded := range childIDs {
		childID, _ := ParseProcessID(encoded)
		child, _ := engine.Process(childID)
		result := mustAwait(t, child)
		if result.Status() != wantStatus || result.Termination().Cause() != wantCause {
			t.Fatalf("child result = status %s cause %s", result.Status(), result.Termination().Cause())
		}
	}
}

func mustCloseEngine(t *testing.T, engine *Engine) {
	t.Helper()
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestParentDeadlinePropagatesAsParentDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dispatcher := newBlockingChildDispatcher("first", "second", "third")
		t.Cleanup(dispatcher.ReleaseAll)
		deployment := newChildTestDeploymentWithDispatcher(t, dispatcher)
		engine, err := NewEngine(EngineConfig{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		input, _ := EncodeInput(childTestInput{Mode: "wait:all"})
		parent, err := engine.Start(ctx, deployment, input)
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			<-dispatcher.started
		}
		parentResult := mustAwait(t, parent)
		if parentResult.Status() != StatusTimedOut || parentResult.Termination().Cause() != TerminationCauseHostDeadline {
			t.Fatalf("parent termination = %#v", parentResult.Termination())
		}
		childIDs := directChildIDs(t, engine, parent.ID())
		dispatcher.ReleaseAll()
		for _, encoded := range childIDs {
			childID, _ := ParseProcessID(encoded)
			child, _ := engine.Process(childID)
			result := mustAwait(t, child)
			if result.Status() != StatusTimedOut || result.Termination().Cause() != TerminationCauseParentDeadline {
				t.Fatalf("child termination = %#v", result.Termination())
			}
		}
		if err := engine.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestChildFailureRemainsExplicitStrategyInput(t *testing.T) {
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "wait:failure"})
	result, err := engine.Run(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != StatusFailed {
		t.Fatalf("parent status = %s", result.Status())
	}
	failure, ok := result.Termination().Failure()
	if !ok || failure.Code() != "test.child.failed" {
		t.Fatalf("parent failure = %#v", failure)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEngineRejectsWaitingOnDescendantThatIsNotDirectChild(t *testing.T) {
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "recurse:2"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	rootOutput := childTestResult(t, mustAwait(t, root))
	childID, _ := ParseProcessID(rootOutput.ChildIDs[0])
	child, _ := engine.Process(childID)
	childOutput := childTestResult(t, mustAwait(t, child))
	grandchildID, _ := ParseProcessID(childOutput.ChildIDs[0])
	waitID, _ := ParseWaitID("wait:ancestor-rejected")
	waitKey, _ := ParseWaitKey("descendant")
	_, _, err = root.controller.runtime.Load().registerChildWait(root.ID(), waitID, ChildWaitSpec{
		Key: waitKey, Children: []ProcessID{grandchildID}, Condition: AllChildren(),
	})
	if !errors.Is(err, ErrInvalidChildWait) {
		t.Fatalf("ancestor wait error = %v, want %v", err, ErrInvalidChildWait)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestChildCompletionDeliveriesAreOrderedByWaitIdentity(t *testing.T) {
	waitC, _ := ParseWaitID("wait:c")
	waitA, _ := ParseWaitID("wait:a")
	waitB, _ := ParseWaitID("wait:b")
	registrations := map[WaitID]*childWaitRegistration{
		waitC: {waitID: waitC},
		waitA: {waitID: waitA},
		waitB: {waitID: waitB},
	}
	ordered := orderedChildWaitRegistrations(registrations)
	want := []WaitID{waitA, waitB, waitC}
	for index, registration := range ordered {
		if registration.waitID != want[index] {
			t.Fatalf(
				"delivery %d WaitID = %s, want %s", index, registration.waitID, want[index],
			)
		}
	}
}

type childTestInput struct {
	Mode string `json:"mode"`
}

type childTestOutput struct {
	ChildIDs      []string `json:"child_ids,omitempty"`
	CompletedKeys []string `json:"completed_keys,omitempty"`
	FailureCodes  []string `json:"failure_codes,omitempty"`
	Failures      int      `json:"failures"`
}

type childTestState struct {
	Phase          string   `json:"phase"`
	Mode           string   `json:"mode"`
	ChildIDs       []string `json:"child_ids,omitempty"`
	WaitID         string   `json:"wait_id,omitempty"`
	ExternalWaitID string   `json:"external_wait_id,omitempty"`
}

type childTestDefinition struct {
	descriptor Descriptor
	reference  DeploymentRef
}

func newChildTestDeployment(t testing.TB) Deployment {
	return newChildTestDeploymentWithDispatcher(t, childTestDispatcher{})
}

func newChildTestDeploymentWithDispatcher(t testing.TB, dispatcher Dispatcher) Deployment {
	t.Helper()
	inputSchema, err := SchemaFor[childTestInput]()
	if err != nil {
		t.Fatal(err)
	}
	outputSchema, err := SchemaFor[childTestOutput]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name: "test.child", Description: "Exercise child Process framework Effects.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	implementation := ComputeDigest([]byte("child-test-implementation"))
	configuration := ComputeDigest([]byte("child-test-configuration"))
	reference, err := newDeploymentRef(descriptor, implementation, configuration)
	if err != nil {
		t.Fatal(err)
	}
	definition := &childTestDefinition{descriptor: descriptor, reference: reference}
	deployment, err := NewDeployment(DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: implementation, ConfigurationDigest: configuration,
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func (c *childTestDefinition) Descriptor() Descriptor { return c.descriptor }

func (c *childTestDefinition) Start(input Input) (Execution, error) {
	decoded, err := input.Decode[childTestInput]()
	if err != nil {
		return nil, err
	}
	return &childTestExecution{
		reference: c.reference,
		state:     childTestState{Phase: "ready", Mode: decoded.Mode},
	}, nil
}

func (c *childTestDefinition) Restore(state ExecutionState) (Execution, error) {
	if state.Kind() != "test.child" {
		return nil, ErrInvalidExecutionState
	}
	var decoded childTestState
	if err := json.Unmarshal(state.Payload(), &decoded); err != nil {
		return nil, err
	}
	return &childTestExecution{reference: c.reference, state: decoded}, nil
}

type childTestExecution struct {
	reference DeploymentRef
	state     childTestState
}

func (c *childTestExecution) Step(_ context.Context, signals []Signal) (Transition, error) {
	switch c.state.Phase {
	case "ready":
		return c.start()
	case "started":
		return c.acceptChildStarts(signals)
	case "wait_opened":
		return c.acceptChildWait(signals)
	case "waiting":
		return c.completeChildren(signals, uint32(len(signals)))
	case "external_wait_opened":
		return c.acceptExternalWait(signals)
	case "external_waiting":
		return c.completeAfterSignal(signals, "external wait response is required")
	case "external_children_waiting":
		if len(signals) != 2 {
			return Transition{}, errors.New("child completion and external answer are required")
		}
		waitID, _ := signals[1].WaitID()
		if waitID.String() != c.state.ExternalWaitID {
			return Transition{}, errors.New("external answer addressed another wait")
		}
		return c.completeChildren(signals[:1], 2)
	case "leaf_effect":
		return c.completeAfterSignal(signals, "leaf Effect settlement is required")
	case "leaf_paused":
		return c.completeEmpty(0)
	default:
		return Transition{}, errors.New("child test execution cannot advance")
	}
}

func (c *childTestExecution) start() (Transition, error) {
	mode := c.state.Mode
	switch {
	case mode == "leaf" || mode == "recurse:0" || mode == "binary:0":
		return c.completeEmpty(0)
	case mode == "leaf_pause":
		c.state.Phase = "leaf_paused"
		return Pause(0, "child paused for tree capture")
	case mode == "leaf_fail":
		c.state.Phase = "done"
		failure, _ := NewFailure(FailureKindExecution, "test.leaf.failed", "leaf failed as requested")
		return Fail(0, failure)
	case mode == "external_wait":
		return c.openExternalWait()
	case strings.HasPrefix(mode, "leaf:"):
		return c.startLeafEffect()
	}
	c.state.Phase = "started"
	switch {
	case strings.HasPrefix(mode, "wait:"):
		return c.startWaitChildren()
	case strings.HasPrefix(mode, "binary:"):
		return c.startBinaryChildren()
	case mode == "fanout" || mode == "fanout_blocking":
		return c.startFanoutChildren()
	default:
		return c.startSingleChild()
	}
}

func (c *childTestExecution) openExternalWait() (Transition, error) {
	c.state.Phase = "external_wait_opened"
	key, _ := ParseWaitKey("external_input")
	effect, err := RequestWait(key, json.RawMessage(`{"kind":"external_input"}`))
	if err != nil {
		return Transition{}, err
	}
	return Continue(0, effect)
}

func (c *childTestExecution) startLeafEffect() (Transition, error) {
	payload, _ := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: strings.TrimPrefix(c.state.Mode, "leaf:")})
	effect, err := NewDispatcherEffect(payload)
	if err != nil {
		return Transition{}, err
	}
	c.state.Phase = "leaf_effect"
	return Continue(0, effect)
}

func (c *childTestExecution) startWaitChildren() (Transition, error) {
	names := []string{"first", "second", "third"}
	switch c.state.Mode {
	case "wait:subtree":
		names = []string{"target"}
	case "wait:subtree_all":
		names = []string{"target", "sibling"}
	}
	effects := make([]Effect, 0, len(names))
	for _, name := range names {
		effect, err := c.childEffect(name, c.waitChildMode(name))
		if err != nil {
			return Transition{}, err
		}
		effects = append(effects, effect)
	}
	return Continue(0, effects...)
}

func (c *childTestExecution) waitChildMode(name string) string {
	switch c.state.Mode {
	case "wait:subtree":
		return "nested_wait"
	case "wait:subtree_all":
		if name == "target" {
			return "nested_wait"
		}
		return "leaf_pause"
	case "wait:paused":
		return "leaf_pause"
	case "wait:failure":
		if name == "first" {
			return "leaf_fail"
		}
		return "leaf"
	default:
		return "leaf:" + name
	}
}

func (c *childTestExecution) childEffect(name, mode string) (Effect, error) {
	childInput, _ := EncodeInput(childTestInput{Mode: mode})
	key, _ := ParseChildKey(name)
	return StartChild(childTestSpec(key, c.reference, childInput))
}

func (c *childTestExecution) startBinaryChildren() (Transition, error) {
	depth, err := strconv.Atoi(strings.TrimPrefix(c.state.Mode, "binary:"))
	if err != nil || depth <= 0 || depth > 16 {
		return Transition{}, errors.New("invalid binary child depth")
	}
	units := uint64(20*(1<<uint(depth-1)) - 10)
	effects := make([]Effect, 0, 2)
	for _, name := range []string{"left", "right"} {
		childInput, _ := EncodeInput(childTestInput{Mode: fmt.Sprintf("binary:%d", depth-1)})
		key, _ := ParseChildKey(name)
		spec := childTestSpec(key, c.reference, childInput)
		spec.Budget, _ = NewBudget(BudgetConfig{Steps: units, Effects: units, Signals: units})
		effect, err := StartChild(spec)
		if err != nil {
			return Transition{}, err
		}
		effects = append(effects, effect)
	}
	return Continue(0, effects...)
}

func (c *childTestExecution) startFanoutChildren() (Transition, error) {
	effects := make([]Effect, 0, 3)
	for _, name := range []string{"first", "second", "third"} {
		mode := "leaf"
		if c.state.Mode == "fanout_blocking" {
			mode = "leaf:" + name
		}
		effect, err := c.childEffect(name, mode)
		if err != nil {
			return Transition{}, err
		}
		effects = append(effects, effect)
	}
	return Continue(0, effects...)
}

func (c *childTestExecution) startSingleChild() (Transition, error) {
	childMode := "leaf"
	recursiveDepth := 0
	if encodedDepth, ok := strings.CutPrefix(c.state.Mode, "recurse:"); ok {
		depth, err := strconv.Atoi(encodedDepth)
		if err != nil || depth <= 0 {
			return Transition{}, errors.New("invalid recursive child depth")
		}
		recursiveDepth = depth
		childMode = fmt.Sprintf("recurse:%d", depth-1)
	}
	if c.state.Mode == "nested_wait" {
		childMode = "external_wait"
	}
	childInput, _ := EncodeInput(childTestInput{Mode: childMode})
	key, _ := ParseChildKey("worker")
	spec := childTestSpec(key, c.reference, childInput)
	c.configureSingleChild(&spec, recursiveDepth)
	effect, err := StartChild(spec)
	if err != nil {
		return Transition{}, err
	}
	if c.state.Mode == "duplicate" {
		return Continue(0, effect, effect)
	}
	return Continue(0, effect)
}

func (c *childTestExecution) configureSingleChild(spec *ChildSpec, recursiveDepth int) {
	if c.state.Mode == "nested_wait" {
		spec.Budget, _ = NewBudget(BudgetConfig{Steps: 5, Effects: 5, Signals: 5})
	}
	if recursiveDepth > 0 {
		units := uint64(recursiveDepth * 50)
		spec.Budget, _ = NewBudget(BudgetConfig{Steps: units, Effects: units, Signals: units})
	}
	switch c.state.Mode {
	case "capability_child":
		capability, _ := ParseCapability("resource.read")
		spec.Capabilities, _ = NewCapabilitySet(capability)
	case "capability_escalation":
		capability, _ := ParseCapability("resource.write")
		spec.Capabilities, _ = NewCapabilitySet(capability)
	case "budget_escalation":
		spec.Budget, _ = NewBudget(BudgetConfig{Steps: 20_000, Effects: 20_000, Signals: 20_000})
	}
}

func (c *childTestExecution) acceptChildStarts(signals []Signal) (Transition, error) {
	if len(signals) == 0 {
		return Transition{}, errors.New("child start results are required")
	}
	output := childTestOutput{}
	for _, signal := range signals {
		result, err := ParseChildStartResult(signal)
		if err != nil {
			return Transition{}, err
		}
		if childID, started := result.ProcessID(); started {
			output.ChildIDs = append(output.ChildIDs, childID.String())
		} else if failure, failed := result.Failure(); failed {
			output.Failures++
			output.FailureCodes = append(output.FailureCodes, failure.Code())
		}
	}
	if len(output.ChildIDs) == 0 || !c.requiresChildWait() {
		c.state.Phase = "done"
		erased, _ := EncodeOutput(output)
		return Complete(uint32(len(signals)), erased)
	}
	return c.openChildWait(signals, output.ChildIDs)
}

func (c *childTestExecution) requiresChildWait() bool {
	mode := c.state.Mode
	return strings.HasPrefix(mode, "wait:") || strings.HasPrefix(mode, "recurse:") ||
		strings.HasPrefix(mode, "binary:") || mode == "nested_wait"
}

func (c *childTestExecution) openChildWait(
	signals []Signal,
	childIDs []string,
) (Transition, error) {
	c.state.ChildIDs = slices.Clone(childIDs)
	children := make([]ProcessID, len(childIDs))
	for index, encoded := range childIDs {
		children[index], _ = ParseProcessID(encoded)
	}
	condition := AllChildren()
	switch c.state.Mode {
	case "wait:any":
		condition = AnyChild()
	case "wait:quorum":
		condition, _ = ChildQuorum(2)
	}
	key, _ := ParseWaitKey("children")
	effect, err := WaitForChildren(ChildWaitSpec{Key: key, Children: children, Condition: condition})
	if err != nil {
		return Transition{}, err
	}
	c.state.Phase = "wait_opened"
	return Continue(uint32(len(signals)), effect)
}

func (c *childTestExecution) acceptChildWait(signals []Signal) (Transition, error) {
	if len(signals) == 0 {
		return Transition{}, errors.New("child wait-opened Signal is required")
	}
	opened, err := ParseChildWaitOpened(signals[0])
	if err != nil {
		return Transition{}, err
	}
	openedSpec := opened.Spec()
	if len(openedSpec.Children) != len(c.state.ChildIDs) {
		return Transition{}, errors.New("child wait acknowledgement changed the requested children")
	}
	if len(openedSpec.Children) > 0 {
		original := openedSpec.Children[0]
		openedSpec.Children[0] = ProcessID{}
		if opened.Spec().Children[0] != original {
			return Transition{}, errors.New("child wait acknowledgement exposed mutable children")
		}
	}
	c.state.WaitID = opened.WaitID().String()
	if len(signals) > 1 {
		return c.completeChildren(signals, uint32(len(signals)))
	}
	c.state.Phase = "waiting"
	if c.state.Mode == "wait:paused_parent" {
		return Pause(1, "parent paused while children run")
	}
	if c.state.Mode == "wait:external_parent" {
		transition, openErr := c.openExternalWait()
		if openErr != nil {
			return Transition{}, openErr
		}
		return Continue(1, transition.Effects()...)
	}
	return Wait(1, opened.WaitID())
}

func (c *childTestExecution) acceptExternalWait(signals []Signal) (Transition, error) {
	if len(signals) != 1 {
		return Transition{}, errors.New("external wait-opened Signal is required")
	}
	waitID, addressed := signals[0].WaitID()
	if !addressed {
		return Transition{}, errors.New("external wait-opened Signal has no WaitID")
	}
	if c.state.Mode == "wait:external_parent" {
		c.state.ExternalWaitID = waitID.String()
		c.state.Phase = "external_children_waiting"
	} else {
		c.state.WaitID = waitID.String()
		c.state.Phase = "external_waiting"
	}
	return Wait(1, waitID)
}

func (c *childTestExecution) completeAfterSignal(
	signals []Signal,
	missingSignalError string,
) (Transition, error) {
	if len(signals) != 1 {
		return Transition{}, errors.New(missingSignalError)
	}
	return c.completeEmpty(1)
}

func (c *childTestExecution) completeEmpty(consumedSignals uint32) (Transition, error) {
	c.state.Phase = "done"
	output, _ := EncodeOutput(childTestOutput{})
	return Complete(consumedSignals, output)
}

func (c *childTestExecution) completeChildren(signals []Signal, consumedSignals uint32) (Transition, error) {
	if len(signals) == 0 {
		return Transition{}, errors.New("children-completed Signal is required")
	}
	completed, err := ParseChildrenCompleted(signals[len(signals)-1])
	if err != nil {
		return Transition{}, err
	}
	if completed.WaitID().String() != c.state.WaitID {
		return Transition{}, errors.New("children completed another wait")
	}
	output := childTestOutput{ChildIDs: slices.Clone(c.state.ChildIDs)}
	for _, outcome := range completed.Outcomes() {
		if outcome.Result().Status() == StatusFailed {
			c.state.Phase = "done"
			failure, _ := NewFailure(FailureKindExecution, "test.child.failed", "a child Process failed")
			return Fail(consumedSignals, failure)
		}
		output.CompletedKeys = append(output.CompletedKeys, outcome.Key().String())
	}
	c.state.Phase = "done"
	erased, _ := EncodeOutput(output)
	return Complete(consumedSignals, erased)
}

func childTestSpec(key ChildKey, deployment DeploymentRef, input Input) ChildSpec {
	budget, _ := NewBudget(BudgetConfig{Steps: 20, Effects: 20, Signals: 40})
	return ChildSpec{
		Key: key, DeploymentRef: deployment, Input: input, Budget: budget,
		Capabilities: CapabilitySet{},
	}
}

func (c *childTestExecution) Snapshot() (ExecutionState, error) {
	payload, err := json.Marshal(c.state)
	if err != nil {
		return ExecutionState{}, err
	}
	return NewExecutionState("test.child", payload)
}

type childTestDispatcher struct{}

func (childTestDispatcher) Dispatch(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
	return Settlement{}, errors.New("child test has no dispatcher Effects")
}

func (childTestDispatcher) ReplayPolicy(Effect) ReplayPolicy { return ReplayPolicyNever }

type contextCheckingChildDispatcher struct {
	key  any
	want any
}

func (c contextCheckingChildDispatcher) Dispatch(
	ctx context.Context,
	request EffectRequest,
	_ DeltaEmitter,
) (Settlement, error) {
	if got := ctx.Value(c.key); got != c.want {
		return Settlement{}, fmt.Errorf("child context value = %v, want %v", got, c.want)
	}
	if ctx.Done() != nil {
		return Settlement{}, errors.New("child context retained request cancellation")
	}
	return NewSettlement(request.ID(), SettlementStatusSucceeded, json.RawMessage(`{}`))
}

func (contextCheckingChildDispatcher) ReplayPolicy(Effect) ReplayPolicy { return ReplayPolicyNever }

type blockingChildDispatcher struct {
	started  chan string
	releases map[string]*childTestRelease
}

func newBlockingChildDispatcher(names ...string) *blockingChildDispatcher {
	dispatcher := &blockingChildDispatcher{
		started: make(chan string, len(names)), releases: make(map[string]*childTestRelease, len(names)),
	}
	for _, name := range names {
		dispatcher.releases[name] = &childTestRelease{done: make(chan struct{})}
	}
	return dispatcher
}

func (b *blockingChildDispatcher) Dispatch(
	_ context.Context,
	request EffectRequest,
	_ DeltaEmitter,
) (Settlement, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(request.Effect().Payload(), &input); err != nil {
		return Settlement{}, err
	}
	release, exists := b.releases[input.Name]
	if !exists {
		return Settlement{}, fmt.Errorf("unknown child %q", input.Name)
	}
	b.started <- input.Name
	<-release.done
	return NewSettlement(request.ID(), SettlementStatusSucceeded, json.RawMessage(`{}`))
}

func (*blockingChildDispatcher) ReplayPolicy(Effect) ReplayPolicy { return ReplayPolicyNever }

func (b *blockingChildDispatcher) Release(name string) {
	if release := b.releases[name]; release != nil {
		release.once.Do(func() { close(release.done) })
	}
}

func (b *blockingChildDispatcher) ReleaseAll() {
	for name := range b.releases {
		b.Release(name)
	}
}

type childTestRelease struct {
	done chan struct{}
	once sync.Once
}

func waitForProcessStatus(t *testing.T, process *Process, want Status) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for {
		snapshot, err := process.Snapshot(ctx)
		if err != nil {
			t.Fatalf("capture Process while waiting for %s: %v", want, err)
		}
		if snapshot.Status() == want {
			return
		}
		runtime.Gosched()
	}
}

func childTestResult(t *testing.T, result Result) childTestOutput {
	t.Helper()
	if result.Status() != StatusCompleted {
		t.Fatalf("status = %s, termination = %#v", result.Status(), result.Termination())
	}
	erased, ok := result.Output()
	if !ok {
		t.Fatal("completed result has no Output")
	}
	decoded, err := erased.Decode[childTestOutput]()
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func mustAwait(t *testing.T, process *Process) Result {
	t.Helper()
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func awaitChildren(t *testing.T, engine *Engine, encodedIDs []string) {
	t.Helper()
	for _, encoded := range encodedIDs {
		childID, err := ParseProcessID(encoded)
		if err != nil {
			t.Fatal(err)
		}
		child, found := engine.Process(childID)
		if !found {
			t.Fatalf("child %s is missing", childID)
		}
		_ = mustAwait(t, child)
	}
}

func directChildIDs(t *testing.T, engine *Engine, parentID ProcessID) []string {
	t.Helper()
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	var ids []string
	for _, controller := range engine.processes {
		actualParent, child := controller.relation.ParentID()
		if child && actualParent == parentID {
			ids = append(ids, controller.processID.String())
		}
	}
	slices.Sort(ids)
	return ids
}
