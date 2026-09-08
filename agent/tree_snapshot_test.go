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
	if got, want := slices.Sorted(maps.Keys(fields)), []string{"process_snapshots", "root_id"}; !slices.Equal(got, want) {
		t.Fatalf("ephemeral tree fields = %v, want %v", got, want)
	}
	if _, durable := tree.IncarnationID(); durable {
		t.Fatal("ephemeral capture unexpectedly contains a TreeIncarnationID")
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
	wire.IncarnationID = &incarnationID
	durable, err := newTreeSnapshot(wire)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := durable.IncarnationID()
	if !ok || got != incarnationID {
		t.Fatalf("IncarnationID = %s, %t, want %s, true", got, ok, incarnationID)
	}
	parsed, err := ParseTreeSnapshot(durable.JSON())
	if err != nil {
		t.Fatal(err)
	}
	if parsedID, parsedOK := parsed.IncarnationID(); !parsedOK || parsedID != incarnationID {
		t.Fatalf("parsed IncarnationID = %s, %t", parsedID, parsedOK)
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
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "wait:paused"})
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
			opened := mustMailboxSignal(t, "signal:foreign-wait", registration.WaitID, json.RawMessage(`{}`))
			if openErr := mailbox.openWait(registration.Spec.Key, opened, false); openErr != nil {
				t.Fatal(openErr)
			}
			child.Mailbox = mailbox.snapshot()
			child.Usage.AcceptedSignals = mailbox.arrivalSequence()
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

	restoredEngine, err := NewEngine(EngineConfig{})
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
	if err := restoredEngine.Close(); err != nil {
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
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalTreeSnapshotClosesUnconsumedChildWait(t *testing.T) {
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
	if err := engine.Close(); err != nil {
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
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "wait:all"})
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
	restoredEngine, _ := NewEngine(EngineConfig{})
	restored, err := restoredEngine.RestoreTree(context.Background(), deployment, result.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if restoredResult := mustAwait(t, restored); restoredResult.Status() != StatusCompleted {
		t.Fatalf("restored status = %s", restoredResult.Status())
	}
	if err := restoredEngine.Close(); err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, root)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTreeRestoreResolvesEveryExactDeployment(t *testing.T) {
	childDeployment := newChildTestDeployment(t)
	parentDeployment := newCrossParentDeployment(t, childDeployment.DeploymentRef())
	resolver := deploymentMapResolver{childDeployment.DeploymentRef(): childDeployment}
	engine, _ := NewEngine(EngineConfig{DeploymentResolver: resolver})
	input, _ := EncodeInput(struct{}{})
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

	withoutResolver, _ := NewEngine(EngineConfig{})
	if _, restoreTreeErr := withoutResolver.RestoreTree(context.Background(), parentDeployment, tree); !errors.Is(restoreTreeErr, ErrInvalidTreeSnapshot) {
		t.Fatalf("missing resolver error = %v", restoreTreeErr)
	}
	if closeErr := withoutResolver.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	restoredEngine, _ := NewEngine(EngineConfig{DeploymentResolver: resolver})
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
	if err := restoredEngine.Close(); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDurableChildOutcomeCommitsWholeProspectiveTree(t *testing.T) {
	durability := &recordingTreeDurability{}
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{TreeDurability: durability})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "parent"})
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
	for _, checkpoint := range durability.treeCheckpoints() {
		if checkpoint.Kind() == TreeCheckpointChild {
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
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTreeRestoreValidatesTerminalOutputAgainstExactDeployment(t *testing.T) {
	deployment := newChildTestDeployment(t)
	engine, _ := NewEngine(EngineConfig{})
	input, _ := EncodeInput(childTestInput{Mode: "leaf"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, root)
	snapshot := inspectProcessSnapshot(t, root)
	wire, _ := snapshot.wire()
	invalidOutput, _ := EncodeOutput(struct {
		Unexpected bool `json:"unexpected"`
	}{Unexpected: true})
	wire.Output = &invalidOutput
	forged, err := newProcessSnapshot(wire)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := newTreeSnapshot(treeSnapshotWire{
		RootID: forged.ProcessID(), ProcessSnapshots: []ProcessSnapshot{forged},
	})
	if err != nil {
		t.Fatal(err)
	}
	restoredEngine, _ := NewEngine(EngineConfig{})
	if _, err := restoredEngine.RestoreTree(context.Background(), deployment, tree); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("schema mismatch error = %v", err)
	}
	if err := restoredEngine.Close(); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
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
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(engineTestInput{Value: "tree"})
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
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	return tree
}
