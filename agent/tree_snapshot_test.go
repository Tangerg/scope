package agent

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
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
	<-root.handle.treeRuntime().done
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
	if err := jsonv2.Unmarshal(tree.JSON(), &fields); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		omit      string
		unknown   bool
		malformed json.RawMessage
	}{
		{name: "missing writer", omit: "incarnation_id"},
		{name: "missing processes", omit: "process_snapshots"},
		{name: "unknown member", unknown: true},
		{name: "malformed JSON", malformed: json.RawMessage(`{"incarnation_id":`)},
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
				data, err = jsonv2.Marshal(candidate)
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
	if err := jsonv2.Unmarshal(tree.JSON(), &fields); err != nil {
		t.Fatal(err)
	}
	if got, want := slices.Sorted(maps.Keys(fields)), []string{"incarnation_id", "process_snapshots", "tree_limits"}; !slices.Equal(got, want) {
		t.Fatalf("tree fields = %v, want %v", got, want)
	}
	if !tree.IncarnationID().Valid() {
		t.Fatal("capture is missing its writer identity")
	}
}

func TestTreeSnapshotEncodedSizeAndJSONOwnership(t *testing.T) {
	if size := (TreeSnapshot{}).EncodedSize(); size != 0 {
		t.Fatalf("zero snapshot size=%d, want 0", size)
	}
	owner := newWaitingSnapshotTree(t, 1)
	root := owner.members.get(owner.rootID)
	root.committedExecutionState = controlValue(EncodeExecutionState("size", "界🙂\n\"\\\x00"))
	tree := controlValue(owner.captureTree())
	data := tree.JSON()
	if tree.EncodedSize() != len(data) {
		t.Fatalf("snapshot size=%d, want encoded byte length %d", tree.EncodedSize(), len(data))
	}
	parsed := controlValue(ParseTreeSnapshot(data))
	if parsed.EncodedSize() != len(data) || parsed.Digest() != tree.Digest() {
		t.Fatal("size or content identity changed across round trip")
	}
	clear(data)
	if bytes.Equal(data, tree.JSON()) || tree.EncodedSize() != len(data) ||
		tree.Digest() != ComputeDigest(tree.JSON()) {
		t.Fatal("caller mutation changed the retained snapshot")
	}
	var size int
	if allocations := testing.AllocsPerRun(100, func() { size = tree.EncodedSize() }); allocations != 0 || size != len(data) {
		t.Fatalf("size lookup allocations=%g, want 0", allocations)
	}
}

func TestTreeEncodingPreservesCanonicalBytes(t *testing.T) {
	for _, count := range []int{1, 3} {
		owner := newWaitingSnapshotTree(t, count)
		root := owner.members.get(owner.rootID)
		root.committedExecutionState = controlValue(ParseExecutionState("encoding", []byte(`{"z":"界🙂\n\"\\\u0000","a":[1,{},[]]}`)))
		tree := controlValue(owner.captureTree())
		wire := controlValue(tree.wire())
		canonical := controlValue(jsonv2.Marshal(encoderDocument(wire), jsonv2.Deterministic(true)))
		if !bytes.Equal(tree.JSON(), canonical) || tree.Digest() != ComputeDigest(canonical) {
			t.Fatal("tree encoding changed canonical content or digest")
		}
		for _, process := range wire.ProcessSnapshots {
			encoded := controlValue(process.MarshalJSON())
			clear(encoded)
			if !process.Valid() || !bytes.Contains(tree.JSON(), process.JSON()) {
				t.Fatal("borrowed Process bytes escaped the tree encoder")
			}
		}
	}
}

func TestTreeSnapshotCarriesOneTypedIncarnationIdentity(t *testing.T) {
	tree := completedTreeSnapshot(t)
	wire, err := tree.wire()
	if err != nil {
		t.Fatal(err)
	}
	incarnationID, err := parseTreeIncarnationID(
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
		encoded, err := jsonv2.Marshal(parsed)
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
	t.Run("structured tree owns its Process snapshots", func(t *testing.T) {
		wire, decodeErr := tree.wire()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		rebuilt, snapshotErr := newTreeSnapshot(wire)
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		wire.ProcessSnapshots[0] = ProcessSnapshot{}
		returned, wireErr := rebuilt.wire()
		if wireErr != nil {
			t.Fatal(wireErr)
		}
		returned.ProcessSnapshots[0] = ProcessSnapshot{}
		retained, retainedErr := rebuilt.wire()
		if retainedErr != nil {
			t.Fatal(retainedErr)
		}
		encoded, encodeErr := jsonv2.Marshal(encoderDocument(retained), jsonv2.Deterministic(true))
		if encodeErr != nil || !bytes.Equal(encoded, tree.JSON()) {
			t.Fatalf("wire mutation changed retained Process snapshots: %v", encodeErr)
		}
	})
	for _, boundary := range []ChildWaitBoundary{"", "unknown"} {
		t.Run("child wait rejects changed boundary "+string(boundary), func(t *testing.T) {
			encoded := treeJSONWithProcess(t, tree, root.ID(), func(wire *processSnapshotWire) {
				for _, record := range wire.Mailbox.Signals {
					if record.Opens != nil && record.Opens.Spec != nil {
						record.Opens.Spec.Boundary = boundary
					}
				}
			})
			if _, parseErr := ParseTreeSnapshot(encoded); !errors.Is(parseErr, ErrInvalidTreeSnapshot) {
				t.Fatalf("changed child wait boundary error=%v", parseErr)
			}
		})
	}
	t.Run("child wait belongs to its parent", func(t *testing.T) {
		candidate, decodeErr := tree.wire()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		rootWaits := candidate.processSnapshot(root.ID()).openChildWaits
		if len(rootWaits) == 0 {
			t.Fatal("fixture root has no open child wait")
		}
		foreignID := controlValue(ParseWaitID("wait:foreign"))
		for index, snapshot := range candidate.ProcessSnapshots {
			if snapshot.ProcessID() == root.ID() {
				continue
			}
			child, childErr := snapshot.wire()
			if childErr != nil {
				t.Fatal(childErr)
			}
			mailbox, restoreErr := restoreSignalMailbox(child.Mailbox)
			if restoreErr != nil {
				t.Fatal(restoreErr)
			}
			openTestChildWait(t, &mailbox, foreignID, rootWaits[0].spec)
			child.Mailbox = mailbox.wire()
			child.PauseReason = ""
			child.CurrentWaitID = &foreignID
			changed, snapshotErr := newProcessSnapshot(child)
			if snapshotErr != nil {
				t.Fatalf("individual Process facts should be valid: %v", snapshotErr)
			}
			candidate.ProcessSnapshots[index] = changed
			break
		}
		encoded, encodeErr := jsonv2.Marshal(candidate)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if _, parseErr := ParseTreeSnapshot(encoded); !errors.Is(parseErr, ErrInvalidTreeSnapshot) {
			t.Fatalf("foreign child wait error=%v", parseErr)
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
	if result := mustAwait(t, root); result.Termination().Status() != StatusKilled {
		t.Fatalf("root status = %s", result.Termination().Status())
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
				if effect.settlement() == nil || effect.settlement().Status() == SettlementStatusUnknown {
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
	if restoredResult := mustAwait(t, restored); restoredResult.Termination().Status() != StatusCompleted {
		t.Fatalf("restored status = %s", restoredResult.Termination().Status())
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
	if restoredResult := mustAwait(t, restored); restoredResult.Termination().Status() != StatusCompleted {
		t.Fatalf("restored root status = %s", restoredResult.Termination().Status())
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
	if len(tree.ProcessSnapshots()) != 2 || !tree.state.processSnapshot(wantChildID).Valid() {
		t.Fatalf("child outcome tree does not contain both Processes: %#v", tree.ProcessSnapshots())
	}
	parentWire, err := tree.state.processSnapshot(root.ID()).wire()
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
	wire.Finish.Termination.output = invalidOutput
	forged, err := newProcessSnapshot(wire)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := newTreeSnapshot(treeSnapshotWire{TreeLimits: DefaultTreeLimits(), IncarnationID: newTreeIncarnationID(),
		ProcessSnapshots: []ProcessSnapshot{forged},
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
	var runtimes []*treeRuntime
	for rootID := range engine.processes {
		if runtime := engine.rootRuntime(rootID); runtime != nil {
			runtimes = append(runtimes, runtime)
		}
	}
	engine.mu.RUnlock()
	for _, runtime := range runtimes {
		select {
		case <-runtime.done:
		case <-time.After(treeRuntimeProgressTimeout):
			t.Fatal("tree runtime did not finish after all Processes settled")
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
	if result, awaitErr := root.Await(context.Background()); awaitErr != nil || result.Termination().Status() != StatusCompleted {
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
	restoration := &treeRestoration{wire: treeSnapshotWire{TreeLimits: runtime.treeLimits, IncarnationID: newTreeIncarnationID()}}
	for _, process := range runtime.members.ordered() {
		restoration.wire.ProcessSnapshots = append(restoration.wire.ProcessSnapshots, controlValue(process.capture()))
	}
	conflict := restoration.wire.ProcessSnapshots[1]
	if err := engine.reserveProcessStart(rootProcessRelation(conflict.ProcessID())); err != nil {
		t.Fatal(err)
	}
	if err := engine.reserveRestoredTree(restoration); !errors.Is(err, ErrProcessAlreadyExists) {
		t.Fatalf("conflicting reservation = %v", err)
	}
	engine.discardProcessStart(rootProcessRelation(conflict.ProcessID()))
	checkStarts := func(want error) {
		t.Helper()
		for _, process := range restoration.wire.ProcessSnapshots {
			id := process.ProcessID()
			err := engine.reserveProcessStart(rootProcessRelation(id))
			if !errors.Is(err, want) {
				t.Fatalf("admission for %s = %v, want %v", id, err, want)
			}
			if err == nil {
				engine.discardProcessStart(rootProcessRelation(id))
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
	foreignID, _ := ParseProcessID("foreign")
	foreign.Relation = rootProcessRelation(foreignID)
	foreignSnapshot, err := newProcessSnapshot(foreign)
	if err != nil {
		t.Fatal(err)
	}
	orphan := base.clone()
	orphanID, _ := ParseProcessID("orphan")
	parentID, _ := ParseProcessID("absent")
	key, _ := ParseChildKey("orphan")
	orphan.Relation = childProcessRelation(orphanID, ProcessRelation{processID: parentID, rootID: tree.RootID()}, key)
	orphanSnapshot, err := newProcessSnapshot(orphan)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		snapshots []ProcessSnapshot
		detail    string
	}{
		{[]ProcessSnapshot{tree.ProcessSnapshots()[0], foreignSnapshot, orphanSnapshot}, "Process belongs to another tree"},
		{[]ProcessSnapshot{tree.ProcessSnapshots()[0], orphanSnapshot, foreignSnapshot}, "Process belongs to another tree"},
	} {
		data, err := (treeSnapshotWire{TreeLimits: DefaultTreeLimits(), IncarnationID: newTreeIncarnationID(), ProcessSnapshots: test.snapshots}).encode()
		if err != nil {
			t.Fatal(err)
		}
		for _, capture := range []func() (TreeSnapshot, error){
			func() (TreeSnapshot, error) { return ParseTreeSnapshot(data) },
			func() (TreeSnapshot, error) {
				return newTreeSnapshot(treeSnapshotWire{TreeLimits: DefaultTreeLimits(), IncarnationID: newTreeIncarnationID(), ProcessSnapshots: test.snapshots})
			},
		} {
			_, err := capture()
			if !errors.Is(err, ErrInvalidTreeSnapshot) || err.Error() != ErrInvalidTreeSnapshot.Error()+": "+test.detail {
				t.Fatalf("nondeterministic relation diagnostic: %v", err)
			}
		}
	}
}

func TestTreeSnapshotEffectRequestPreservesFrozenEvidence(t *testing.T) {
	pending, deployment, id := durablePendingTreeSnapshot(t)
	parsed, err := ParseTreeSnapshot(pending.JSON())
	if err != nil {
		t.Fatal(err)
	}
	request, found := parsed.EffectRequest(parsed.RootID(), id)
	if !found || !request.Valid() || request.ID() != id ||
		request.ProcessID() != parsed.RootID() || request.DeploymentRef() != deployment.DeploymentRef() ||
		request.StepSequence() != 1 || request.BatchIndex() != 0 ||
		request.Relation() != parsed.ProcessSnapshots()[0].Relation() {
		t.Fatalf("frozen request identity changed: %+v", request)
	}
	if writer, present := request.TreeIncarnationID(); !present || writer != parsed.IncarnationID() {
		t.Fatal("frozen request writer changed")
	}
	frozen := request.Effect().Payload()
	mutated := request.Effect().Payload()
	mutated[0] = '!'
	again, found := parsed.EffectRequest(parsed.RootID(), id)
	if !found || !bytes.Equal(again.Effect().Payload(), frozen) {
		t.Fatal("caller mutated frozen Effect evidence")
	}
	for _, capture := range []TreeSnapshot{{}, completedTreeSnapshot(t), parsed} {
		if missing, found := capture.EffectRequest(capture.RootID(), EffectID{}); found || missing.Valid() {
			t.Fatal("snapshot manufactured an absent Effect")
		}
		if missing, found := capture.EffectRequest(ProcessID{}, id); found || missing.Valid() {
			t.Fatal("snapshot manufactured an absent Process")
		}
	}
}

// The spliced encoding is the canonical form every digest depends on, so it
// must stay byte-identical to marshaling the wire through the encoder.
func TestTreeSnapshotEncodingMatchesTheEncoder(t *testing.T) {
	fixtures := map[string]TreeSnapshot{
		"single waiting":  controlValue(newWaitingSnapshotTree(t, 1).captureTree()),
		"waiting tree":    controlValue(newWaitingSnapshotTree(t, 40).captureTree()),
		"drained subtree": drainedSnapshotFixture(t, 8),
		"deep subtree":    deepDrainedSnapshotFixture(t),
		"retained waits":  retainedWaitsSnapshotFixture(t, 6),
	}
	for name, snapshot := range fixtures {
		want := controlValue(jsonv2.Marshal(encoderDocument(snapshot.state), jsonv2.Deterministic(true)))
		if got := controlValue(snapshot.state.encode()); !bytes.Equal(got, want) {
			t.Fatalf("%s: spliced encoding differs from the encoder:\n got %s\nwant %s", name, got, want)
		}
		if !bytes.Equal(snapshot.JSON(), want) {
			t.Fatalf("%s: captured snapshot bytes differ from the encoder", name)
		}
	}
	if len(fixtures["retained waits"].ProcessSnapshots()[0].openChildWaits) == 0 {
		t.Fatal("fixture set does not exercise open child waits")
	}
}

// treeJSONWithProcess re-encodes tree with one Process wire mutated and not
// revalidated, so parsing exercises the tree's own contract checks.
func treeJSONWithProcess(t testing.TB, tree TreeSnapshot, processID ProcessID, mutate func(*processSnapshotWire)) []byte {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(tree.JSON(), &fields); err != nil {
		t.Fatal(err)
	}
	var processes []json.RawMessage
	if err := jsonv2.Unmarshal(fields["process_snapshots"], &processes); err != nil {
		t.Fatal(err)
	}
	for index, snapshot := range tree.ProcessSnapshots() {
		if snapshot.ProcessID() != processID {
			continue
		}
		wire := controlValue(snapshot.wire())
		mutate(&wire)
		processes[index] = controlValue(jsonv2.Marshal(wire))
	}
	fields["process_snapshots"] = controlValue(jsonv2.Marshal(processes))
	return controlValue(jsonv2.Marshal(fields))
}

// treeJSONWithDocument re-encodes tree with one persisted Process document
// mutated, so parsing exercises how the tree decodes it.
func treeJSONWithDocument(t testing.TB, tree TreeSnapshot, processID ProcessID, mutate func(*processSnapshotDocument)) []byte {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(tree.JSON(), &fields); err != nil {
		t.Fatal(err)
	}
	var processes []json.RawMessage
	if err := jsonv2.Unmarshal(fields["process_snapshots"], &processes); err != nil {
		t.Fatal(err)
	}
	for index, snapshot := range tree.ProcessSnapshots() {
		if snapshot.ProcessID() != processID {
			continue
		}
		document := controlValue(decodeProcessSnapshotDocument(processes[index]))
		mutate(&document)
		processes[index] = controlValue(jsonv2.Marshal(document))
	}
	fields["process_snapshots"] = controlValue(jsonv2.Marshal(processes))
	return controlValue(jsonv2.Marshal(fields))
}

// encoderDocument is the tree document the encoder would produce from wire,
// the reference the spliced encoding must match byte for byte.
func encoderDocument(wire treeSnapshotWire) treeSnapshotDocument {
	document := treeSnapshotDocument{IncarnationID: wire.IncarnationID, TreeLimits: wire.TreeLimits, ProcessSnapshots: []json.RawMessage{}}
	for _, snapshot := range wire.ProcessSnapshots {
		document.ProcessSnapshots = append(document.ProcessSnapshots, snapshot.data)
	}
	return document
}
