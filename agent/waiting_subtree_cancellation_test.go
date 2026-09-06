package agent

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestPreparedWaitingSubtreeCancellationAppliesExactTreeState(t *testing.T) {
	engine, root, target, descendant := startWaitingSubtree(t)

	prepared, err := engine.PrepareWaitingSubtreeCancellation(
		context.Background(), root.ID(), target.ID(), "caller discarded the waiting branch",
	)
	if err != nil {
		t.Fatal(err)
	}
	if prepared == nil || !prepared.ResultingSnapshot().Valid() {
		t.Fatal("prepared cancellation is invalid")
	}
	if target.Status() != StatusWaiting || descendant.Status() != StatusWaiting || root.Status() != StatusWaiting {
		t.Fatal("preparing cancellation changed the live tree")
	}
	canceled := prepared.CanceledProcessIDs()
	if len(canceled) != 2 || canceled[0] != target.ID() || canceled[1] != descendant.ID() {
		t.Fatalf("canceled Process IDs = %v", canceled)
	}
	paused := prepared.PausedProcessIDs()
	if len(paused) != 1 || paused[0] != root.ID() {
		t.Fatalf("paused Process IDs = %v", paused)
	}
	canceled[0] = ProcessID{}
	paused[0] = ProcessID{}
	if prepared.CanceledProcessIDs()[0] != target.ID() || prepared.PausedProcessIDs()[0] != root.ID() {
		t.Fatal("prepared exposed mutable Process ID storage")
	}

	assertPreparedWaitingSubtree(t, prepared, root.ID(), target.ID(), descendant.ID())
	if applyErr := prepared.Apply(); applyErr != nil {
		t.Fatal(applyErr)
	}
	if applyErr := prepared.Apply(); !errors.Is(applyErr, ErrPreparedWaitingSubtreeCancellationResolved) {
		t.Fatalf("second Apply error = %v, want resolved", applyErr)
	}
	if discardErr := prepared.Discard(); !errors.Is(discardErr, ErrPreparedWaitingSubtreeCancellationResolved) {
		t.Fatalf("Discard after Apply error = %v, want resolved", discardErr)
	}
	targetResult := mustAwait(t, target)
	if targetResult.Status() != StatusCanceled ||
		targetResult.Termination().Cause() != TerminationCauseHostCancellation {
		t.Fatalf("target termination = %#v", targetResult.Termination())
	}
	descendantResult := mustAwait(t, descendant)
	if descendantResult.Status() != StatusCanceled ||
		descendantResult.Termination().Cause() != TerminationCauseParentCancellation {
		t.Fatalf("descendant termination = %#v", descendantResult.Termination())
	}
	if root.Status() != StatusPaused {
		t.Fatalf("root status = %s, want paused", root.Status())
	}
	live, err := engine.CaptureTree(context.Background(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(live.JSON(), prepared.ResultingSnapshot().JSON()) {
		t.Fatal("live tree does not equal the prepared resulting snapshot")
	}
	assertRootHasCanceledChildSignal(t, live, root.ID(), target.ID())

	if err := root.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if result := mustAwait(t, root); result.Status() != StatusCompleted {
		t.Fatalf("resumed root status = %s", result.Status())
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDurablePreparedWaitingSubtreeCancellationAdvancesFromAcknowledgedHead(t *testing.T) {
	durability := &recordingTreeDurability{}
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{TreeDurability: durability})
	if err != nil {
		t.Fatal(err)
	}
	root, target, descendant := startWaitingSubtreeInEngine(t, engine, deployment)
	prepared, err := engine.PrepareWaitingSubtreeCancellation(
		context.Background(), root.ID(), target.ID(), "durable branch cancellation",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.SourceTreeDigest().Valid() ||
		prepared.SourceTreeDigest() == prepared.ResultingSnapshot().Digest() {
		t.Fatalf(
			"source=%s resulting=%s",
			prepared.SourceTreeDigest(), prepared.ResultingSnapshot().Digest(),
		)
	}
	checkpoints := durability.treeCheckpoints()
	if len(checkpoints) == 0 ||
		checkpoints[len(checkpoints)-1].TreeSnapshot().Digest() != prepared.SourceTreeDigest() {
		t.Fatalf("prepared source was not the acknowledged checkpoint head")
	}
	sourceIncarnation, sourceDurable := prepared.source.snapshot.IncarnationID()
	resultIncarnation, resultDurable := prepared.ResultingSnapshot().IncarnationID()
	if !sourceDurable || !resultDurable || sourceIncarnation != resultIncarnation {
		t.Fatalf(
			"source incarnation=%s/%t result=%s/%t",
			sourceIncarnation, sourceDurable, resultIncarnation, resultDurable,
		)
	}
	if err := prepared.Apply(); err != nil {
		t.Fatal(err)
	}
	if root.Status() != StatusPaused {
		t.Fatalf("root status=%s", root.Status())
	}
	if result := mustAwait(t, target); result.Status() != StatusCanceled {
		t.Fatalf("target status=%s", result.Status())
	}
	if result := mustAwait(t, descendant); result.Status() != StatusCanceled {
		t.Fatalf("descendant status=%s", result.Status())
	}
	if err := root.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, root)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedWaitingSubtreeCancellationRejectsNilAndZeroValues(t *testing.T) {
	var nilPrepared *PreparedWaitingSubtreeCancellation
	if err := nilPrepared.Apply(); !errors.Is(err, ErrInvalidPreparedWaitingSubtreeCancellation) {
		t.Fatalf("nil Apply error = %v", err)
	}
	if err := nilPrepared.Discard(); !errors.Is(err, ErrInvalidPreparedWaitingSubtreeCancellation) {
		t.Fatalf("nil Discard error = %v", err)
	}
	zero := &PreparedWaitingSubtreeCancellation{}
	if err := zero.Discard(); !errors.Is(err, ErrInvalidPreparedWaitingSubtreeCancellation) {
		t.Fatalf("zero Discard error = %v", err)
	}
	if err := zero.Apply(); !errors.Is(err, ErrInvalidPreparedWaitingSubtreeCancellation) {
		t.Fatalf("zero Apply error = %v", err)
	}
}

func TestPreparedWaitingSubtreeCancellationResolvesExactlyOnceConcurrently(t *testing.T) {
	engine, root, target, descendant := startWaitingSubtree(t)
	prepared, err := engine.PrepareWaitingSubtreeCancellation(
		context.Background(), root.ID(), target.ID(), "resolve concurrently",
	)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- prepared.Apply()
	}()
	go func() {
		<-start
		results <- prepared.Discard()
	}()
	close(start)
	first, second := <-results, <-results
	if first != nil && second != nil {
		t.Fatalf("both resolutions failed: %v; %v", first, second)
	}
	if first == nil && second == nil {
		t.Fatal("Apply and Discard both resolved the same prepared cancellation")
	}
	resolvedErr := first
	if resolvedErr == nil {
		resolvedErr = second
	}
	if !errors.Is(resolvedErr, ErrPreparedWaitingSubtreeCancellationResolved) {
		t.Fatalf("losing resolution error = %v, want resolved", resolvedErr)
	}
	if err := root.Kill(context.Background(), "test cleanup"); err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, root)
	_ = mustAwait(t, target)
	_ = mustAwait(t, descendant)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedWaitingSubtreeCancellationCopyCannotDuplicateAuthority(t *testing.T) {
	engine, root, target, descendant := startWaitingSubtree(t)
	prepared, err := engine.PrepareWaitingSubtreeCancellation(
		context.Background(), root.ID(), target.ID(), "resolve copied capability concurrently",
	)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := *prepared
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- prepared.Apply()
	}()
	go func() {
		<-start
		results <- duplicate.Discard()
	}()
	close(start)
	first, second := <-results, <-results
	if first != nil && second != nil {
		t.Fatalf("both copied-capability resolutions failed: %v; %v", first, second)
	}
	if first == nil && second == nil {
		t.Fatal("a copied prepared value duplicated the one-shot authority")
	}
	resolvedErr := first
	if resolvedErr == nil {
		resolvedErr = second
	}
	if !errors.Is(resolvedErr, ErrPreparedWaitingSubtreeCancellationResolved) {
		t.Fatalf("losing copied-capability resolution error = %v, want resolved", resolvedErr)
	}
	if err := root.Kill(context.Background(), "test cleanup"); err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, root)
	_ = mustAwait(t, target)
	_ = mustAwait(t, descendant)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedWaitingSubtreeCancellationFreezesSourceUntilDiscard(t *testing.T) {
	synctest.Test(t, testPreparedWaitingSubtreeCancellationFreezesSourceUntilDiscard)
}

func testPreparedWaitingSubtreeCancellationFreezesSourceUntilDiscard(t *testing.T) {
	engine, root, target, descendant := startWaitingSubtree(t)
	prepared, err := engine.PrepareWaitingSubtreeCancellation(
		context.Background(), root.ID(), target.ID(), "discard frozen branch",
	)
	if err != nil {
		t.Fatal(err)
	}
	waitID, waiting := descendant.WaitID()
	if !waiting {
		t.Fatal("descendant has no external WaitID")
	}
	signalID, _ := ParseSignalID("signal:complete-frozen-wait")
	response, _ := NewSignalRequest(signalID, waitID, []byte(`{"answer":true}`))
	type deliveryResult struct {
		accepted bool
		err      error
	}
	delivered := make(chan deliveryResult, 1)
	go func() {
		accepted, deliveryErr := descendant.DeliverSignals(context.Background(), response)
		delivered <- deliveryResult{accepted: accepted, err: deliveryErr}
	}()
	synctest.Wait()
	select {
	case result := <-delivered:
		t.Fatalf("DeliverSignals crossed the prepared boundary: %#v", result)
	default:
	}
	if root.Status() != StatusWaiting || target.Status() != StatusWaiting ||
		descendant.Status() != StatusWaiting {
		t.Fatal("source tree changed while cancellation was prepared")
	}
	if err := prepared.Discard(); err != nil {
		t.Fatal(err)
	}
	result := <-delivered
	if result.err != nil || !result.accepted {
		t.Fatalf("DeliverSignals accepted = %t, error = %v", result.accepted, result.err)
	}
	if err := prepared.Discard(); !errors.Is(err, ErrPreparedWaitingSubtreeCancellationResolved) {
		t.Fatalf("second Discard error = %v, want resolved", err)
	}
	_ = mustAwait(t, root)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedWaitingSubtreeCancellationPreservesUnsatisfiedChildWait(t *testing.T) {
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "wait:subtree_all"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForProcessStatus(t, root, StatusWaiting)

	children := processesByChildKey(t, engine, root.ID())
	target := children["target"]
	sibling := children["sibling"]
	if target == nil || sibling == nil {
		t.Fatal("direct child keys do not contain target and sibling")
	}
	waitForProcessStatus(t, target, StatusWaiting)
	waitForProcessStatus(t, sibling, StatusPaused)
	descendants := directChildIDs(t, engine, target.ID())
	if len(descendants) != 1 {
		t.Fatalf("target descendant count = %d, want 1", len(descendants))
	}
	descendantID, _ := ParseProcessID(descendants[0])
	descendant, _ := engine.Process(descendantID)
	waitForProcessStatus(t, descendant, StatusWaiting)

	prepared, err := engine.PrepareWaitingSubtreeCancellation(
		context.Background(), root.ID(), target.ID(), "discard one waiting branch",
	)
	if err != nil {
		t.Fatal(err)
	}
	if paused := prepared.PausedProcessIDs(); len(paused) != 0 {
		t.Fatalf("paused Process IDs = %v, want none before wait-all is satisfied", paused)
	}
	resultingRoot := snapshotByID(prepared.ResultingSnapshot().ProcessSnapshots(), root.ID())
	if resultingRoot.Status() != StatusWaiting {
		t.Fatalf("resulting root status = %s, want waiting", resultingRoot.Status())
	}
	if err := prepared.Apply(); err != nil {
		t.Fatal(err)
	}
	if root.Status() != StatusWaiting {
		t.Fatalf("live root status = %s, want waiting", root.Status())
	}
	if targetResult := mustAwait(t, target); targetResult.Status() != StatusCanceled {
		t.Fatalf("target status = %s", targetResult.Status())
	}

	if err := sibling.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	rootResult := mustAwait(t, root)
	rootOutput := childTestResult(t, rootResult)
	if len(rootOutput.CompletedKeys) != 2 || rootOutput.CompletedKeys[0] != "target" ||
		rootOutput.CompletedKeys[1] != "sibling" {
		t.Fatalf("completed keys = %v, want [target sibling]", rootOutput.CompletedKeys)
	}
	if siblingResult := mustAwait(t, sibling); siblingResult.Status() != StatusCompleted {
		t.Fatalf("sibling status = %s", siblingResult.Status())
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedWaitingSubtreeCancellationApplyCannotBeRequestCanceled(t *testing.T) {
	engine, root, target, descendant := startWaitingSubtree(t)
	prepared, err := engine.PrepareWaitingSubtreeCancellation(
		context.Background(), root.ID(), target.ID(), "discard canceled apply",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := prepared.Discard(); !errors.Is(err, ErrPreparedWaitingSubtreeCancellationResolved) {
		t.Fatalf("Discard after successful retry error = %v, want resolved", err)
	}
	if result := mustAwait(t, target); result.Status() != StatusCanceled {
		t.Fatalf("target status = %s, want canceled", result.Status())
	}
	_ = mustAwait(t, descendant)
	if err := root.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, root)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedWaitingSubtreeCancellationDoesNotFreezeOtherRootTrees(t *testing.T) {
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	root, target, descendant := startWaitingSubtreeInEngine(t, engine, deployment)
	prepared, err := engine.PrepareWaitingSubtreeCancellation(
		context.Background(), root.ID(), target.ID(), "freeze only one root",
	)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "leaf"})
	otherRoot, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, otherRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := engine.CaptureTree(ctx, otherRoot.ID()); err != nil {
		t.Fatalf("capturing an independent root: %v", err)
	}
	if err := prepared.Discard(); err != nil {
		t.Fatal(err)
	}
	if err := root.Kill(context.Background(), "test cleanup"); err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, root)
	_ = mustAwait(t, target)
	_ = mustAwait(t, descendant)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedWaitingSubtreeCancellationResultRestoresWithoutPrivateStateMutation(t *testing.T) {
	engine, root, target, descendant := startWaitingSubtree(t)
	prepared, err := engine.PrepareWaitingSubtreeCancellation(
		context.Background(), root.ID(), target.ID(), "restore canceled branch",
	)
	if err != nil {
		t.Fatal(err)
	}

	deployment := newChildTestDeployment(t)
	restoredEngine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	restoredRoot, err := restoredEngine.RestoreTree(
		context.Background(), deployment, prepared.ResultingSnapshot(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if restoredRoot.Status() != StatusPaused {
		t.Fatalf("restored root status = %s", restoredRoot.Status())
	}
	restoredTarget, found := restoredEngine.Process(target.ID())
	if !found || restoredTarget.Status() != StatusCanceled {
		t.Fatalf("restored target found = %t, status = %s", found, restoredTarget.Status())
	}
	restoredDescendant, found := restoredEngine.Process(descendant.ID())
	if !found || restoredDescendant.Status() != StatusCanceled {
		t.Fatalf("restored descendant found = %t, status = %s", found, restoredDescendant.Status())
	}
	if err := restoredRoot.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if result := mustAwait(t, restoredRoot); result.Status() != StatusCompleted {
		t.Fatalf("restored root result = %s", result.Status())
	}
	if err := restoredEngine.Close(); err != nil {
		t.Fatal(err)
	}

	if err := prepared.Apply(); err != nil {
		t.Fatal(err)
	}
	if err := root.Kill(context.Background(), "test cleanup"); err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, root)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedWaitingSubtreeCancellationRequiresNonRootWaitingTarget(t *testing.T) {
	engine, root, target, _ := startWaitingSubtree(t)
	if _, err := engine.PrepareWaitingSubtreeCancellation(
		context.Background(), root.ID(), root.ID(), "root is not a valid target",
	); !errors.Is(err, ErrWaitingSubtreeCancellationUnavailable) {
		t.Fatalf("root target error = %v", err)
	}
	if root.Status() != StatusWaiting || target.Status() != StatusWaiting {
		t.Fatal("rejected root target changed the live tree")
	}
	prepared, err := engine.PrepareWaitingSubtreeCancellation(
		context.Background(), root.ID(), target.ID(), "test cleanup",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Apply(); err != nil {
		t.Fatal(err)
	}
	if err := root.Kill(context.Background(), "test cleanup"); err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, root)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	pausedEngine, pausedRoot, pausedChild := startPausedChildTree(t)
	if _, err := pausedEngine.PrepareWaitingSubtreeCancellation(
		context.Background(), pausedRoot.ID(), pausedChild.ID(), "paused is not waiting",
	); !errors.Is(err, ErrWaitingSubtreeCancellationUnavailable) {
		t.Fatalf("paused target error = %v", err)
	}
	if pausedChild.Status() != StatusPaused {
		t.Fatal("rejected paused target changed the live tree")
	}
	for _, encoded := range directChildIDs(t, pausedEngine, pausedRoot.ID()) {
		id, _ := ParseProcessID(encoded)
		child, _ := pausedEngine.Process(id)
		if err := child.Resume(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	_ = mustAwait(t, pausedRoot)
	if err := pausedEngine.Close(); err != nil {
		t.Fatal(err)
	}
}

func startWaitingSubtree(t *testing.T) (*Engine, *Process, *Process, *Process) {
	t.Helper()
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	root, target, descendant := startWaitingSubtreeInEngine(t, engine, deployment)
	return engine, root, target, descendant
}

func startWaitingSubtreeInEngine(
	t *testing.T,
	engine *Engine,
	deployment Deployment,
) (*Process, *Process, *Process) {
	t.Helper()
	input, _ := EncodeInput(childTestInput{Mode: "wait:subtree"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForProcessStatus(t, root, StatusWaiting)
	targets := directChildIDs(t, engine, root.ID())
	if len(targets) != 1 {
		t.Fatalf("target count = %d, want 1", len(targets))
	}
	targetID, _ := ParseProcessID(targets[0])
	target, _ := engine.Process(targetID)
	waitForProcessStatus(t, target, StatusWaiting)
	descendants := directChildIDs(t, engine, target.ID())
	if len(descendants) != 1 {
		t.Fatalf("descendant count = %d, want 1", len(descendants))
	}
	descendantID, _ := ParseProcessID(descendants[0])
	descendant, _ := engine.Process(descendantID)
	waitForProcessStatus(t, descendant, StatusWaiting)
	return root, target, descendant
}

func startPausedChildTree(t *testing.T) (*Engine, *Process, *Process) {
	t.Helper()
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "wait:paused"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForProcessStatus(t, root, StatusWaiting)
	children := directChildIDs(t, engine, root.ID())
	if len(children) == 0 {
		t.Fatal("paused child tree has no child")
	}
	childID, _ := ParseProcessID(children[0])
	child, _ := engine.Process(childID)
	waitForProcessStatus(t, child, StatusPaused)
	return engine, root, child
}

func processesByChildKey(t *testing.T, engine *Engine, parentID ProcessID) map[string]*Process {
	t.Helper()
	processes := make(map[string]*Process)
	for _, encoded := range directChildIDs(t, engine, parentID) {
		processID, err := ParseProcessID(encoded)
		if err != nil {
			t.Fatal(err)
		}
		process, found := engine.Process(processID)
		if !found {
			t.Fatalf("child Process %s is missing", processID)
		}
		key, child := process.Relation().ChildKey()
		if !child {
			t.Fatalf("Process %s has no child key", processID)
		}
		processes[key.String()] = process
	}
	return processes
}

func assertPreparedWaitingSubtree(
	t *testing.T,
	prepared *PreparedWaitingSubtreeCancellation,
	rootID ProcessID,
	targetID ProcessID,
	descendantID ProcessID,
) {
	t.Helper()
	statuses := make(map[ProcessID]Status)
	for _, snapshot := range prepared.ResultingSnapshot().ProcessSnapshots() {
		statuses[snapshot.ProcessID()] = snapshot.Status()
		if snapshot.ProcessID() == descendantID {
			wire, err := snapshot.wire()
			if err != nil {
				t.Fatal(err)
			}
			for _, wait := range wire.Mailbox.Waits {
				if !wait.Closed {
					t.Fatal("canceled descendant retained an open external wait")
				}
			}
		}
	}
	if statuses[rootID] != StatusPaused || statuses[targetID] != StatusCanceled ||
		statuses[descendantID] != StatusCanceled {
		t.Fatalf("prepared statuses = %v", statuses)
	}
}

func assertRootHasCanceledChildSignal(
	t *testing.T,
	tree TreeSnapshot,
	rootID ProcessID,
	targetID ProcessID,
) {
	t.Helper()
	root := snapshotByID(tree.ProcessSnapshots(), rootID)
	wire, err := root.wire()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range wire.Mailbox.Signals[wire.Mailbox.SignalCursor:] {
		completed, err := ParseChildrenCompleted(record.Signal)
		if err != nil {
			continue
		}
		for _, outcome := range completed.Outcomes() {
			if outcome.Result().ProcessID() == targetID && outcome.Result().Status() == StatusCanceled {
				return
			}
		}
	}
	t.Fatal("root mailbox has no canceled child outcome")
}
