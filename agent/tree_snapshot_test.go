package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

func TestCaptureTreeRejectsAlreadyCanceledContext(t *testing.T) {
	engine, _ := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
	input, _ := EncodePayload(childTestInput{Mode: "leaf"})
	root, err := engine.Start(t.Context(), newChildTestDeployment(t), input)
	if err != nil {
		t.Fatal(err)
	}
	_ = awaitResult(t, root)
	<-root.handle.runtime.Load().done
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if snapshot, err := engine.CaptureTree(ctx, root.ID()); !errors.Is(err, context.Canceled) || snapshot.Valid() {
		t.Errorf("CaptureTree returned valid=%v, error=%v for canceled context", snapshot.Valid(), err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestParseTreeSnapshotRejectsInvalidWire(t *testing.T) {
	tree := completedTreeSnapshot(t)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(tree.JSON(), &fields); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		omit      string
		unknown   bool
		malformed json.RawMessage
	}{
		{name: "missing root", omit: "root_id"},
		{name: "missing writer", omit: "incarnation_id"},
		{name: "missing processes", omit: "process_snapshots"},
		{name: "unknown member", unknown: true},
		{name: "malformed JSON", malformed: json.RawMessage(`{"root_id":`)},
		{name: "null", malformed: json.RawMessage(`null`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := maps.Clone(fields)
			delete(candidate, test.omit)
			if test.unknown {
				candidate["unexpected"] = json.RawMessage(`1`)
			}
			data := test.malformed
			if data == nil {
				var err error
				data, err = json.Marshal(candidate)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ParseTreeSnapshot(data); !errors.Is(err, ErrInvalidTreeSnapshot) {
				t.Fatalf("ParseTreeSnapshot() error = %v, want ErrInvalidTreeSnapshot", err)
			}
		})
	}
}

func TestTreeSnapshotDigestIsCanonicalAndStable(t *testing.T) {
	tree := completedTreeSnapshot(t)
	parsed, err := ParseTreeSnapshot(tree.JSON())
	if err != nil {
		t.Fatal(err)
	}
	if tree.Digest() != parsed.Digest() || tree.Digest() != ComputeDigest(tree.JSON()) {
		t.Fatalf("digest changed across canonical round trip: %s != %s", tree.Digest(), parsed.Digest())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(tree.JSON(), &fields); err != nil {
		t.Fatal(err)
	}
	if got, want := slices.Sorted(maps.Keys(fields)), []string{"incarnation_id", "process_snapshots", "root_id"}; !slices.Equal(got, want) {
		t.Fatalf("tree fields = %v, want %v", got, want)
	}
	if !tree.IncarnationID().Valid() {
		t.Fatal("capture is missing its writer identity")
	}
}

func TestTreeSnapshotCarriesOneTypedIncarnationIdentity(t *testing.T) {
	tree := completedTreeSnapshot(t)
	wire, err := tree.wire()
	if err != nil {
		t.Fatal(err)
	}
	incarnationID, err := ParseTreeIncarnationID(
		treeIncarnationIDPrefix + "0123456789abcdef0123456789abcdef",
	)
	if err != nil {
		t.Fatal(err)
	}
	wire.IncarnationID = incarnationID
	durable, err := newTreeSnapshot(wire)
	if err != nil {
		t.Fatal(err)
	}
	wire.IncarnationID = TreeIncarnationID{}
	got := durable.IncarnationID()
	if got != incarnationID {
		t.Fatalf("IncarnationID = %s, want %s", got, incarnationID)
	}
	parsed, err := ParseTreeSnapshot(durable.JSON())
	if err != nil {
		t.Fatal(err)
	}
	if parsedID := parsed.IncarnationID(); parsedID != incarnationID {
		t.Fatalf("parsed IncarnationID = %s", parsedID)
	}
}

func FuzzTreeSnapshotJSONRoundTrip(f *testing.F) {
	tree := completedTreeSnapshot(f)
	f.Add([]byte(tree.JSON()))
	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := ParseTreeSnapshot(data)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}
		reparsed, err := ParseTreeSnapshot(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(parsed.JSON(), reparsed.JSON()) {
			t.Fatal("TreeSnapshot changed across a strict JSON round trip")
		}
	})
}

func TestEngineCapturesAndRestoresCompleteWaitingTree(t *testing.T) {
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(childTestInput{Mode: "wait:paused"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, root, StatusWaiting)
	childIDs := directChildIDs(t, engine, root.ID())
	if len(childIDs) != 3 {
		t.Fatalf("child count = %d, want 3", len(childIDs))
	}
	for _, encoded := range childIDs {
		id, _ := ParseProcessID(encoded)
		child, _ := engine.Process(id)
		waitForStatus(t, child, StatusPaused)
	}
	tree, err := engine.CaptureTree(context.Background(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if tree.RootID() != root.ID() || len(tree.ProcessSnapshots()) != 4 {
		t.Fatalf("tree root = %s, Process count = %d", tree.RootID(), len(tree.ProcessSnapshots()))
	}
	parsed, err := ParseTreeSnapshot(tree.JSON())
	if err != nil || parsed.RootID() != tree.RootID() || len(parsed.ProcessSnapshots()) != 4 {
		t.Fatalf("parsed tree = %#v, error = %v", parsed, err)
	}
	t.Run("structured tree owns child wait members", func(t *testing.T) {
		wire, decodeErr := tree.wire()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		rebuilt, snapshotErr := newTreeSnapshot(wire)
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		wire.ChildWaits[0].Spec.Children[0] = ProcessID{}
		wire.ProcessSnapshots[0] = ProcessSnapshot{}
		returned, wireErr := rebuilt.wire()
		if wireErr != nil {
			t.Fatal(wireErr)
		}
		returned.ChildWaits[0].Spec.Children[0] = ProcessID{}
		retained, retainedErr := rebuilt.wire()
		if retainedErr != nil {
			t.Fatal(retainedErr)
		}
		encoded, encodeErr := json.Marshal(retained)
		if encodeErr != nil || !bytes.Equal(encoded, tree.JSON()) {
			t.Fatalf("wire mutation changed retained child wait membership: %v", encodeErr)
		}
	})
	for _, boundary := range []ChildWaitBoundary{"", "unknown", ChildWaitBoundaryDrained} {
		t.Run("child wait rejects changed boundary "+string(boundary), func(t *testing.T) {
			candidate, decodeErr := tree.wire()
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			candidate.ChildWaits[0].Spec.Boundary = boundary
			encoded, encodeErr := json.Marshal(candidate)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			if _, parseErr := ParseTreeSnapshot(encoded); !errors.Is(parseErr, ErrInvalidTreeSnapshot) {
				t.Fatalf("changed child wait boundary error=%v", parseErr)
			}
		})
	}
	t.Run("child wait registration belongs to its parent", func(t *testing.T) {
		candidate, decodeErr := tree.wire()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		registration := candidate.ChildWaits[0]
		for index, snapshot := range candidate.ProcessSnapshots {
			if snapshot.ProcessID() == root.ID() {
				continue
			}
			child, childErr := snapshot.wire()
			if childErr != nil {
				t.Fatal(childErr)
			}
			mailbox, restoreErr := restoreSignalMailbox(child.Mailbox, child.Status)
			if restoreErr != nil {
				t.Fatal(restoreErr)
			}
			opened := mustMailboxSignal(t, "signal:engine:foreign-wait", registration.WaitID, json.RawMessage(`{}`))
			if openErr := mailbox.openWait(registration.Spec.Key, opened, WaitKindChildren); openErr != nil {
				t.Fatal(openErr)
			}
			child.Mailbox = mailbox.wire()
			child.Status = StatusWaiting
			child.PauseReason = ""
			child.CurrentWaitID = &registration.WaitID
			changed, snapshotErr := newProcessSnapshot(child)
			if snapshotErr != nil {
				t.Fatalf("individual Process facts should be valid: %v", snapshotErr)
			}
			candidate.ProcessSnapshots[index] = changed
			break
		}
		encoded, encodeErr := json.Marshal(candidate)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if _, parseErr := ParseTreeSnapshot(encoded); !errors.Is(parseErr, ErrInvalidTreeSnapshot) {
			t.Fatalf("foreign child wait registration error=%v", parseErr)
		}
	})
	wire, err := tree.wire()
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(wire.ProcessSnapshots)
	inputOrder := make([]ProcessID, len(wire.ProcessSnapshots))
	for index, snapshot := range wire.ProcessSnapshots {
		inputOrder[index] = snapshot.ProcessID()
	}
	rebuilt, err := newTreeSnapshot(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rebuilt.JSON(), tree.JSON()) {
		t.Fatal("private tree construction and strict parsing produced different canonical snapshots")
	}
	for index, snapshot := range wire.ProcessSnapshots {
		if snapshot.ProcessID() != inputOrder[index] {
			t.Fatal("private tree construction mutated the caller-owned Process order")
		}
	}

	restoredEngine, err := NewEngine(EngineConfig{TreeCommitter: newSnapshotTestCommitter(parsed)})
	if err != nil {
		t.Fatal(err)
	}
	restoredRoot, err := restoredEngine.RestoreTree(context.Background(), deployment, parsed)
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range childIDs {
		id, _ := ParseProcessID(encoded)
		child, found := restoredEngine.Process(id)
		if !found {
			t.Fatalf("restored child %s is missing", id)
		}
		if err := child.Resume(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	restoredResult := mustAwait(t, restoredRoot)
	restoredOutput := childTestResult(t, restoredResult)
	if len(restoredOutput.CompletedKeys) != 3 {
		t.Fatalf("restored output = %#v", restoredOutput)
	}
	if len(directChildIDs(t, restoredEngine, restoredRoot.ID())) != 3 {
		t.Fatal("tree restore duplicated or lost a child")
	}
	assertNoChildWaitRegistrations(t, restoredEngine)
	if err := restoredEngine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}

	for _, encoded := range childIDs {
		id, _ := ParseProcessID(encoded)
		child, _ := engine.Process(id)
		if err := child.Resume(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	_ = mustAwait(t, root)
	assertNoChildWaitRegistrations(t, engine)
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalTreeSnapshotClosesUnconsumedChildWait(t *testing.T) {
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(childTestInput{Mode: "wait:paused"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, root, StatusWaiting)
	if killErr := root.Kill(context.Background(), "capture terminal tree"); killErr != nil {
		t.Fatal(killErr)
	}
	if result := mustAwait(t, root); result.Status() != StatusKilled {
		t.Fatalf("root status = %s", result.Status())
	}
	tree, err := engine.CaptureTree(context.Background(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTreeSnapshot(tree.JSON()); err != nil {
		t.Fatalf("terminal TreeSnapshot is not self-consistent: %v", err)
	}
	assertNoChildWaitRegistrations(t, engine)
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestTreeCaptureWaitsForInflightChildEffectsToSettle(t *testing.T) {
	synctest.Test(t, testTreeCaptureWaitsForInflightChildEffectsToSettle)
}

func testTreeCaptureWaitsForInflightChildEffectsToSettle(t *testing.T) {
	dispatcher := newBlockingChildDispatcher("first", "second", "third")
	t.Cleanup(dispatcher.ReleaseAll)
	deployment := newChildTestDeploymentWithDispatcher(t, dispatcher)
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(childTestInput{Mode: "wait:all"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		<-dispatcher.started
	}
	waitForStatus(t, root, StatusWaiting)
	type captureResult struct {
		snapshot TreeSnapshot
		err      error
	}
	captured := make(chan captureResult, 1)
	go func() {
		snapshot, captureTreeErr := engine.CaptureTree(context.Background(), root.ID())
		captured <- captureResult{snapshot: snapshot, err: captureTreeErr}
	}()
	synctest.Wait()
	select {
	case result := <-captured:
		t.Fatalf("CaptureTree crossed unsettled Effects: %#v", result)
	default:
	}
	dispatcher.ReleaseAll()
	result := <-captured
	if result.err != nil {
		t.Fatal(result.err)
	}
	for _, snapshot := range result.snapshot.ProcessSnapshots() {
		wire, wireErr := snapshot.wire()
		if wireErr != nil {
			t.Fatal(wireErr)
		}
		if wire.Prepared != nil {
			for _, effect := range wire.Prepared.Effects {
				if effect.Settlement == nil || effect.Settlement.Status() == SettlementStatusUnknown {
					t.Fatalf("Process %s captured an unsettled Effect", snapshot.ProcessID())
				}
			}
		}
	}
	restoredEngine, _ := NewEngine(EngineConfig{TreeCommitter: newSnapshotTestCommitter(result.snapshot)})
	restored, err := restoredEngine.RestoreTree(context.Background(), deployment, result.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if restoredResult := mustAwait(t, restored); restoredResult.Status() != StatusCompleted {
		t.Fatalf("restored status = %s", restoredResult.Status())
	}
	if err := restoredEngine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, root)
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestTreeRestoreResolvesEveryExactDeployment(t *testing.T) {
	childDeployment := newChildTestDeployment(t)
	parentDeployment := newCrossParentDeployment(t, childDeployment.DeploymentRef())
	resolver := deploymentMapResolver{childDeployment.DeploymentRef(): childDeployment}
	engine, _ := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter(), DeploymentResolver: resolver})
	input, _ := EncodePayload(struct{}{})
	root, err := engine.Start(context.Background(), parentDeployment, input)
	if err != nil {
		t.Fatal(err)
	}
	result := mustAwait(t, root)
	output := childTestResult(t, result)
	awaitChildren(t, engine, output.ChildIDs)
	tree, err := engine.CaptureTree(context.Background(), root.ID())
	if err != nil {
		t.Fatal(err)
	}

	withoutResolver, _ := NewEngine(EngineConfig{TreeCommitter: newSnapshotTestCommitter(tree)})
	if _, restoreTreeErr := withoutResolver.RestoreTree(context.Background(), parentDeployment, tree); !errors.Is(restoreTreeErr, ErrInvalidTreeSnapshot) {
		t.Fatalf("missing resolver error = %v", restoreTreeErr)
	}
	if closeErr := withoutResolver.Close(context.WithoutCancel(t.Context())); closeErr != nil {
		t.Fatal(closeErr)
	}
	restoredEngine, _ := NewEngine(EngineConfig{TreeCommitter: newSnapshotTestCommitter(tree), DeploymentResolver: resolver})
	restored, err := restoredEngine.RestoreTree(context.Background(), parentDeployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	if restoredResult := mustAwait(t, restored); restoredResult.Status() != StatusCompleted {
		t.Fatalf("restored root status = %s", restoredResult.Status())
	}
	childID, _ := ParseProcessID(output.ChildIDs[0])
	restoredChild, found := restoredEngine.Process(childID)
	if !found || restoredChild.DeploymentRef() != childDeployment.DeploymentRef() {
		t.Fatal("cross-Strategy child binding was not restored exactly")
	}
	_ = mustAwait(t, restoredChild)
	if err := restoredEngine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestDurableChildOutcomeCommitsWholeProspectiveTree(t *testing.T) {
	committer := &recordingTreeCommitter{}
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(childTestInput{Mode: "parent"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	result := mustAwait(t, root)
	output := childTestResult(t, result)
	if len(output.ChildIDs) != 1 {
		t.Fatalf("child output = %#v", output)
	}
	wantChildID, err := ParseProcessID(output.ChildIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	var childCheckpoint TreeCheckpoint
	for _, checkpoint := range committer.treeCheckpoints() {
		if checkpoint.Kind() == TreeCheckpointKindChildStart {
			childCheckpoint = checkpoint
			break
		}
	}
	tree := childCheckpoint.TreeSnapshot()
	if !childCheckpoint.Valid() || !childCheckpoint.PreviousTreeDigest().Valid() {
		t.Fatalf("child checkpoint lacks durable tree facts: %#v", childCheckpoint)
	}
	if len(tree.ProcessSnapshots()) != 2 || !snapshotByID(tree.ProcessSnapshots(), wantChildID).Valid() {
		t.Fatalf("child outcome tree does not contain both Processes: %#v", tree.ProcessSnapshots())
	}
	parentWire, err := snapshotByID(tree.ProcessSnapshots(), root.ID()).wire()
	if err != nil || parentWire.Prepared == nil ||
		!parentWire.Prepared.Effects[0].definitelySettled() {
		t.Fatalf("parent child-start settlement is not atomic with child: %v", err)
	}
	child, found := engine.Process(wantChildID)
	if !found {
		t.Fatal("committed child was not published")
	}
	_ = mustAwait(t, child)
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestTreeRestoreValidatesTerminalOutputAgainstExactDeployment(t *testing.T) {
	deployment := newChildTestDeployment(t)
	engine, _ := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
	input, _ := EncodePayload(childTestInput{Mode: "leaf"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, root)
	snapshot := inspectProcessSnapshot(t, root)
	wire, _ := snapshot.wire()
	invalidOutput, _ := EncodePayload(struct {
		Unexpected bool `json:"unexpected"`
	}{Unexpected: true})
	wire.Output = &invalidOutput
	forged, err := newProcessSnapshot(wire)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := newTreeSnapshot(treeSnapshotWire{IncarnationID: newTreeIncarnationID(),
		RootID: forged.ProcessID(), ProcessSnapshots: []ProcessSnapshot{forged},
	})
	if err != nil {
		t.Fatal(err)
	}
	restoredEngine, _ := NewEngine(EngineConfig{TreeCommitter: newSnapshotTestCommitter(tree)})
	if _, err := restoredEngine.RestoreTree(context.Background(), deployment, tree); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("schema mismatch error = %v", err)
	}
	if err := restoredEngine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func assertNoChildWaitRegistrations(t *testing.T, engine *Engine) {
	t.Helper()
	engine.mu.RLock()
	runtimes := make([]*treeRuntime, 0, len(engine.trees))
	for _, runtime := range engine.trees {
		runtimes = append(runtimes, runtime)
	}
	engine.mu.RUnlock()
	for _, runtime := range runtimes {
		select {
		case <-runtime.done:
		case <-time.After(treeRuntimeProgressTimeout):
			t.Fatal("tree runtime did not finish after all Processes settled")
		}
		if len(runtime.childWaits) != 0 {
			t.Fatalf(
				"active child wait registrations = %d, want 0",
				len(runtime.childWaits),
			)
		}
	}
}

func completedTreeSnapshot(t testing.TB) TreeSnapshot {
	t.Helper()
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(engineTestInput{Value: "tree"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	if result, awaitErr := root.Await(context.Background()); awaitErr != nil || result.Status() != StatusCompleted {
		t.Fatalf("root result = %#v, error = %v", result, awaitErr)
	}
	tree, err := engine.CaptureTree(context.Background(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestRestoreReservationAdmissionIsAtomicAndReleasesEveryIdentity(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 3)
	engine := runtime.engine
	restoration := &treeRestoration{wire: treeSnapshotWire{IncarnationID: newTreeIncarnationID(), RootID: runtime.rootID}}
	for _, process := range orderedProcesses(runtime.processes) {
		restoration.wire.ProcessSnapshots = append(restoration.wire.ProcessSnapshots, controlValue(process.capture()))
	}
	conflict := restoration.wire.ProcessSnapshots[1]
	if err := engine.reserveProcessStart(rootProcessRelation(conflict.ProcessID()), conflict.DeploymentRef(), engine.treeLimits, Digest{}); err != nil {
		t.Fatal(err)
	}
	if err := engine.reserveRestoredTree(restoration); !errors.Is(err, ErrProcessAlreadyExists) {
		t.Fatalf("conflicting reservation = %v", err)
	}
	engine.discardProcessStart(conflict.ProcessID())
	checkStarts := func(want error) {
		t.Helper()
		for _, process := range restoration.wire.ProcessSnapshots {
			id := process.ProcessID()
			err := engine.reserveProcessStart(rootProcessRelation(id), process.DeploymentRef(), engine.treeLimits, Digest{})
			if !errors.Is(err, want) {
				t.Fatalf("admission for %s = %v, want %v", id, err, want)
			}
			if err == nil {
				engine.discardProcessStart(id)
			}
		}
	}
	checkStarts(nil)
	if err := engine.reserveRestoredTree(restoration); err != nil {
		t.Fatal(err)
	}
	checkStarts(ErrProcessAlreadyExists)
	engine.discardRestoredTree(restoration)
	checkStarts(nil)
	if err := engine.reserveRestoredTree(restoration); err != nil {
		t.Fatalf("reservation could not be reused: %v", err)
	}
	engine.discardRestoredTree(restoration)
}

func TestTreeSnapshotReportsFirstRelationErrorInCanonicalOrder(t *testing.T) {
	tree := completedTreeSnapshot(t)
	base, err := tree.ProcessSnapshots()[0].wire()
	if err != nil {
		t.Fatal(err)
	}
	foreign := base.clone()
	foreign.ProcessID, _ = ParseProcessID("foreign")
	foreign.Relation = rootProcessRelation(foreign.ProcessID).wire()
	foreignSnapshot, err := newProcessSnapshot(foreign)
	if err != nil {
		t.Fatal(err)
	}
	orphan := base.clone()
	orphan.ProcessID, _ = ParseProcessID("orphan")
	parentID, _ := ParseProcessID("absent")
	key, _ := ParseChildKey("orphan")
	orphan.Relation = processRelationWire{ParentID: &parentID, RootID: tree.RootID(), ChildKey: &key, Depth: 1}
	digest := ComputeDigest([]byte("orphan request"))
	orphan.ChildRequestDigest = &digest
	orphanSnapshot, err := newProcessSnapshot(orphan)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		snapshots []ProcessSnapshot
		detail    string
	}{
		{[]ProcessSnapshot{tree.ProcessSnapshots()[0], foreignSnapshot, orphanSnapshot}, "Process belongs to another tree contract"},
		{[]ProcessSnapshot{tree.ProcessSnapshots()[0], orphanSnapshot, foreignSnapshot}, "Process belongs to another tree contract"},
	} {
		data, err := json.Marshal(treeSnapshotWire{IncarnationID: newTreeIncarnationID(), RootID: tree.RootID(), ProcessSnapshots: test.snapshots})
		if err != nil {
			t.Fatal(err)
		}
		for _, capture := range []func() (TreeSnapshot, error){
			func() (TreeSnapshot, error) { return ParseTreeSnapshot(data) },
			func() (TreeSnapshot, error) {
				return newTreeSnapshot(treeSnapshotWire{IncarnationID: newTreeIncarnationID(), RootID: tree.RootID(), ProcessSnapshots: test.snapshots})
			},
		} {
			_, err := capture()
			if !errors.Is(err, ErrInvalidTreeSnapshot) || err.Error() != ErrInvalidTreeSnapshot.Error()+": "+test.detail {
				t.Fatalf("nondeterministic relation diagnostic: %v", err)
			}
		}
	}
}
