package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

type capacityDefinition struct {
	*engineTestDefinition
	effects []Effect
}

func (c *capacityDefinition) Start(input Payload) (Execution, error) {
	execution, err := c.engineTestDefinition.Start(input)
	if err != nil {
		return nil, err
	}
	return &capacityExecution{engineTestExecution: execution.(*engineTestExecution), effects: c.effects}, nil
}

func (c *capacityDefinition) Restore(ctx context.Context, state ExecutionState) (Execution, error) {
	execution, err := c.engineTestDefinition.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return &capacityExecution{engineTestExecution: execution.(*engineTestExecution), effects: c.effects}, nil
}

type capacityExecution struct {
	*engineTestExecution
	effects []Effect
}

func (c *capacityExecution) Step(_ context.Context, signals []Signal) (Transition, error) {
	if c.state.Phase == "ready" {
		c.state.Phase = "effect"
		return Continue(0, c.effects...)
	}
	if len(signals) == 0 {
		return Transition{}, errors.New("expected settlement")
	}
	message, err := jsonwire.Decode[engineTestMessage](signals[len(signals)-1].Payload())
	if err != nil {
		return Transition{}, err
	}
	c.state.Phase = "done"
	output, err := EncodePayload(engineTestOutput{Value: message.Value})
	if err != nil {
		return Transition{}, err
	}
	return Complete(uint32(len(signals)), output)
}

func TestOversizedStepRejectedBeforeDispatcherPermission(t *testing.T) {
	payload := json.RawMessage(`"` + strings.Repeat("x", 45<<14) + `"`)
	effect := controlValue(NewDispatcherEffect(payload))
	for _, durable := range []bool{false, true} {
		t.Run(fmt.Sprint(durable), func(t *testing.T) {
			store := &recordingTreeDurability{}
			config := EngineConfig{Limits: Limits{MaxSnapshotBytes: NewQuota(128 << 14)}, TreeLimits: TreeLimits{MaxSnapshotBytes: NewQuota(512 << 14)}}
			if durable {
				config.TreeDurability = store
			}
			engine := controlValue(NewEngine(config))
			defer mustCloseEngine(t, engine)
			dispatcher := &engineTestDispatcher{}
			definition := &capacityDefinition{engineTestDefinition: newEngineTestDefinition(t, "engine.effect", "effect"), effects: []Effect{effect, effect, effect}}
			deployment := engineTestDeployment(t, definition, dispatcher)
			process := controlValue(engine.Start(t.Context(), deployment, controlValue(EncodePayload(engineTestInput{Value: "bounded"}))))
			result, err := process.Await(t.Context())
			if err != nil {
				t.Fatalf("pure candidate caused runtime fault: %v", err)
			}
			failure, failed := result.Termination().Failure()
			if !failed || failure.Code() != failureCodeEngineLimitSnapshot || dispatcher.calls.Load() != 0 || result.Usage().PreparedEffects != 0 {
				t.Fatalf("admission result=%+v, calls=%d, usage=%+v", failure, dispatcher.calls.Load(), result.Usage())
			}
			if len(store.effectBoundaries()) != 0 {
				t.Fatal("oversize candidate acquired durable dispatch permission")
			}
			snapshot := inspectProcessSnapshot(t, process)
			if snapshot.state.Prepared != nil || snapshot.state.CommittedSteps != 0 {
				t.Fatal("rejection installed candidate state")
			}
		})
	}
}

func TestOversizedUnknownResolutionPreservesHeadAndAllowsSmallerResult(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(fmt.Sprint(durable), func(t *testing.T) {
			store := &recordingTreeDurability{}
			config := EngineConfig{Limits: Limits{MaxSnapshotBytes: NewQuota(128 << 14)}, TreeLimits: TreeLimits{MaxSnapshotBytes: NewQuota(512 << 14)}}
			if durable {
				config.TreeDurability = store
			}
			engine := controlValue(NewEngine(config))
			defer mustCloseEngine(t, engine)
			dispatcher := &failingEngineTestDispatcher{}
			effect := controlValue(NewDispatcherEffect(json.RawMessage(`{}`)))
			definition := &capacityDefinition{engineTestDefinition: newEngineTestDefinition(t, "engine.effect", "effect"), effects: []Effect{effect}}
			process := controlValue(engine.Start(t.Context(), engineTestDeployment(t, definition, dispatcher), controlValue(EncodePayload(engineTestInput{Value: "bounded"}))))
			defer func() {
				_ = process.Kill(context.WithoutCancel(t.Context()), "cleanup")
				_ = process.Join(context.WithoutCancel(t.Context()))
			}()
			unknown := waitForUnknownSettlement(t, process).UnknownEffectIDs()[0]
			payload := json.RawMessage(`"` + strings.Repeat("x", 40<<14) + `"`)
			var requests []SignalRequest
			for index := range 2 {
				requests = append(requests, controlValue(NewSignalRequest(controlValue(ParseSignalID(fmt.Sprintf("signal:padding-%d", index))), WaitID{}, payload)))
			}
			if accepted, err := process.DeliverSignals(t.Context(), requests...); err != nil || !accepted {
				t.Fatalf("padding admission = %t, %v", accepted, err)
			}
			before := controlValue(engine.InspectTree(t.Context(), process.ID()))
			oversize := controlValue(NewSettlement(unknown, SettlementStatusSucceeded, json.RawMessage(`"`+strings.Repeat("x", 50<<14)+`"`)))
			if err := process.ResolveUnknownEffect(t.Context(), oversize); !errors.Is(err, ErrResourceLimitExceeded) {
				t.Fatalf("oversize resolution = %v", err)
			}
			after := controlValue(engine.InspectTree(t.Context(), process.ID()))
			if before.HeadDigest != after.HeadDigest || !bytes.Equal(before.Processes[0].Snapshot.data, after.Processes[0].Snapshot.data) {
				t.Fatal("rejected resolution changed tree head")
			}
			if got := after.Processes[0].Snapshot.UnknownEffectIDs(); len(got) != 1 || got[0] != unknown {
				t.Fatal("rejection lost Unknown evidence")
			}
			small := controlValue(NewSettlement(unknown, SettlementStatusSucceeded, json.RawMessage(`{"kind":"result","value":"resolved"}`)))
			if err := process.ResolveUnknownEffect(t.Context(), small); err != nil {
				t.Fatalf("small resolution after rejection: %v", err)
			}
			result, err := process.Await(t.Context())
			if err != nil || result.Status() != StatusCompleted {
				t.Fatalf("resolved result=%s, %v", result.Status(), err)
			}
			output, _ := result.Output()
			if value := controlValue(output.Decode[engineTestOutput]()); value.Value != "resolved" || dispatcher.calls.Load() != 1 {
				t.Fatalf("result=%+v, calls=%d", value, dispatcher.calls.Load())
			}
		})
	}
}

func TestPreparedSnapshotHasOneEffectRepresentation(t *testing.T) {
	snapshot := preparedEngineTestSnapshot(t)
	wire := controlValue(snapshot.wire())
	effect := controlValue(NewDispatcherEffect(json.RawMessage(`{"marker":"canonical-effect-content"}`)))
	wire.Prepared.Effects[0].Effect = effect
	encoded := controlValue(newProcessSnapshot(wire)).JSON()
	if bytes.Count(encoded, []byte("canonical-effect-content")) != 1 {
		t.Fatal("prepared snapshot duplicates Effect content")
	}
	wire.Prepared.Intent = controlValue(Continue(0, effect))
	if _, err := newProcessSnapshot(wire); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("duplicate representation accepted: %v", err)
	}
	obsolete := bytes.Replace(encoded, []byte(`"intent":`), []byte(`"transition":`), 1)
	if _, err := ParseProcessSnapshot(obsolete); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("obsolete schema accepted: %v", err)
	}
}

func TestTreeCapacityRejectsIndividuallyRepresentableProcesses(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 5)
	for _, process := range runtime.processes {
		process.limits.MaxSnapshotBytes = NewQuota(128 << 14)
		process.treeLimits.MaxSnapshotBytes = NewQuota(512 << 14)
		process.handle.treeLimits = process.treeLimits
	}
	effect := controlValue(NewDispatcherEffect(json.RawMessage(`"` + strings.Repeat("x", 53<<14) + `"`)))
	for _, process := range runtime.processes {
		process.status, process.currentWaitID = StatusRunning, WaitID{}
		process.counters.PreparedEffects = 2
		process.prepared = &preparedStep{StepSequence: process.committedSteps + 1, CommittedExecutionStateDigest: controlValue(process.committedExecutionState.digest()), SignalCursor: process.mailbox.committedSignalCursor(), Intent: controlValue(Continue(0)), CandidateState: process.committedExecutionState, Effects: preparedEffects{
			{ID: process.handle.processID.effectID(1, 0), Effect: effect, Phase: effectPhasePlanned},
			{ID: process.handle.processID.effectID(1, 1), Effect: effect, Phase: effectPhasePlanned},
		}}
		if _, err := process.snapshotAdmissionSize(); err != nil {
			t.Fatalf("individual process exceeds capacity: %v", err)
		}
	}
	if err := runtime.validateSnapshotCapacity(runtime.processes[runtime.rootID]); !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("aggregate tree capacity = %v", err)
	}
}

func TestKnownWaitSettlementsAreAdmittedBeforeEarlierDispatcher(t *testing.T) {
	process := admissionTestProcess(t, 0)
	process.limits.MaxSnapshotBytes = NewQuota(128 << 14)
	payload := json.RawMessage(`"` + strings.Repeat("x", 33<<14) + `"`)
	first := controlValue(NewWaitEffect(controlValue(ParseWaitKey("first")), payload))
	second := controlValue(NewWaitEffect(controlValue(ParseWaitKey("second")), payload))
	dispatch := controlValue(NewDispatcherEffect(json.RawMessage(`{}`)))
	before := controlValue(process.capture())
	transition := controlValue(Continue(0, dispatch, first, second))
	failure := prepareTestStep(process, stepJobResult{transition: transition, candidate: process.execution, candidateState: process.committedExecutionState})
	if failure == nil || !errors.Is(failure.cause, ErrResourceLimitExceeded) || process.prepared != nil {
		t.Fatalf("known settlement capacity was not reserved: %+v", failure)
	}
	after := controlValue(process.capture())
	if !bytes.Equal(before.data, after.data) {
		t.Fatal("rejected local wait preparation changed the Process")
	}
}

func TestChildInitializationCannotExceedTreeSnapshotCapacity(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 5)
	for _, process := range runtime.processes {
		process.limits.MaxSnapshotBytes = NewQuota(128 << 14)
		process.treeLimits.MaxSnapshotBytes = NewQuota(512 << 14)
		process.handle.treeLimits = process.treeLimits
	}
	root := runtime.processes[runtime.rootID]
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, &engineTestDispatcher{})
	state := controlValue(NewExecutionState("engine.effect", controlValue(json.Marshal(engineTestState{Phase: "ready", Value: strings.Repeat("x", 44<<14)}))))
	execution := controlValue(definition.Restore(t.Context(), state))
	limits := TreeLimits{MaxSnapshotBytes: NewQuota(512 << 14), MaxDepth: 1, MaxChildren: NewQuota(5), MaxActiveChildren: 5, MaxTreeProcesses: NewQuota(6)}
	for _, process := range runtime.processes {
		process.deployment = deployment
		process.handle.deploymentRef = deployment.DeploymentRef()
		process.treeLimits, process.handle.treeLimits = limits, limits
		process.status, process.currentWaitID = StatusRunning, WaitID{}
		process.committedExecutionState = state
		process.execution = execution
		process.prepared = &preparedStep{
			StepSequence: process.committedSteps + 1, CommittedExecutionStateDigest: controlValue(state.digest()),
			CandidateState: state, SignalCursor: process.mailbox.committedSignalCursor(), Intent: controlValue(Continue(0)),
		}
	}
	effectID := root.handle.processID.effectID(1, 0)
	spec := ChildSpec{
		Key: controlValue(ParseChildKey("large-child")), DeploymentRef: deployment.DeploymentRef(),
		Input: controlValue(EncodePayload(engineTestInput{Value: strings.Repeat("x", 22<<14)})), Budget: Budget{Steps: NewQuota(2), Effects: NewQuota(2), Signals: NewQuota(2)},
	}
	root.prepared.Effects = preparedEffects{{ID: effectID, Effect: controlValue(NewChildStartEffect(spec)), Phase: effectPhasePending}}
	root.counters.PreparedEffects = 1
	if err := runtime.validateSnapshotCapacity(); err != nil {
		t.Fatalf("parent and existing tree must fit before initialization: %v", err)
	}
	if err := runtime.engine.reserveProcessStart(root.handle.relation, deployment.DeploymentRef(), limits, Digest{}); err != nil {
		t.Fatal(err)
	}
	runtime.engine.publishProcessStart(root.handle)
	t.Cleanup(func() { delete(runtime.engine.processes, root.handle.processID) })
	preparation := runtime.prepareChildStart(root, effectID, spec)
	if preparation.plan == nil {
		t.Fatalf("child preparation failed: %+v", preparation.result)
	}
	result := preparation.plan.execute(t.Context())
	if !result.started() {
		t.Fatalf("child initialization failed: %+v", result.result)
	}
	reserved := root.allocatedResources
	pending := &pendingChildStartPublication{parentID: root.handle.processID, effectID: effectID, plan: preparation.plan, result: result}
	if err := runtime.applyChildStart(pending); err != nil {
		t.Fatalf("capacity rejection became a runtime fault: %v", err)
	}
	runtime.discardChildStart(preparation.plan)
	failure, failed := pending.result.result.Failure()
	if !failed || failure.Code() != failureCodeEngineChildTreeLimit || pending.result.started() || len(runtime.processes) != 5 {
		t.Fatalf("oversize child was installed: failure=%+v, members=%d", failure, len(runtime.processes))
	}
	if root.allocatedResources != reserved || root.provisionalChildBudget != nil || root.prepared.Effects[0].Settlement.Status() != SettlementStatusFailed {
		t.Fatal("rejection retained child resources or lost the failed start fact")
	}
	assertNoPendingProcessStarts(t, runtime.engine)
	if err := runtime.validateSnapshotCapacity(); err != nil {
		t.Fatalf("rejection left an unrepresentable tree: %v", err)
	}
}

func TestRejectedChildStartReleasesReservationAtCompletion(t *testing.T) {
	for _, mode := range []string{"ephemeral", "committed", "commit_failed", "capture_failed", "checkpoint_failed"} {
		t.Run(mode, func(t *testing.T) {
			runtime := newWaitingSnapshotTree(t, 1)
			root := runtime.processes[runtime.rootID]
			limits := TreeLimits{MaxSnapshotBytes: NewQuota(512 << 14), MaxDepth: 1, MaxChildren: NewQuota(1), MaxActiveChildren: 1, MaxTreeProcesses: NewQuota(2)}
			root.treeLimits, root.handle.treeLimits = limits, limits
			effectID := root.handle.processID.effectID(1, 0)
			spec := ChildSpec{
				Key: controlValue(ParseChildKey("rejected")), DeploymentRef: root.deployment.DeploymentRef(),
				Input: controlValue(EncodePayload(childTestInput{Mode: "leaf"})), Budget: Budget{Steps: NewQuota(2), Effects: NewQuota(2), Signals: NewQuota(2)},
			}
			root.prepared = &preparedStep{
				StepSequence: 1, CommittedExecutionStateDigest: controlValue(root.committedExecutionState.digest()),
				CandidateState: root.committedExecutionState, Intent: controlValue(Continue(0)),
				Effects: preparedEffects{{ID: effectID, Effect: controlValue(NewChildStartEffect(spec)), Phase: effectPhasePending}},
			}
			root.counters.PreparedEffects = 1
			if err := runtime.engine.reserveProcessStart(root.handle.relation, root.deployment.DeploymentRef(), limits, Digest{}); err != nil {
				t.Fatal(err)
			}
			runtime.engine.publishProcessStart(root.handle)
			t.Cleanup(func() { delete(runtime.engine.processes, root.handle.processID) })
			if mode != "ephemeral" {
				runtime.engine.durability = &recordingTreeDurability{}
				runtime.incarnation = newTreeIncarnationID()
				runtime.head = controlValue(runtime.captureTree())
			}
			preparation := runtime.prepareChildStart(root, effectID, spec)
			if preparation.plan == nil {
				t.Fatalf("preparation failed: %+v", preparation.result)
			}
			if mode == "capture_failed" {
				root.committedExecutionState = ExecutionState{}
			}
			if mode == "checkpoint_failed" {
				runtime.commit = &treeCommit{kind: treeCommitCheckpoint}
			}
			result := childStartJobResult{result: failedChildStart(spec, FailureKindExternal, failureCodeEngineChildAdmissionRejected, errors.New("admission refused"))}
			runtime.applyChildStartCompletion(root, &processJob{childStart: preparation.plan, effectID: effectID}, result)
			if root.provisionalChildBudget != nil || root.allocatedResources != (resourceAmounts{}) || len(runtime.processes) != 1 {
				t.Fatal("rejection retained child resources")
			}
			assertNoPendingProcessStarts(t, runtime.engine)
			if mode == "committed" || mode == "commit_failed" {
				completion := <-runtime.commitDone
				if completion.commit.child != nil {
					t.Fatal("rejected start transferred child reservation ownership")
				}
				if mode == "commit_failed" {
					completion.err = errors.New("checkpoint refused")
				}
				runtime.applyTreeCommitCompletion(completion)
			}
			if wantFault := mode == "commit_failed" || mode == "capture_failed" || mode == "checkpoint_failed"; (runtime.fault != nil) != wantFault {
				t.Fatalf("runtime fault = %v, want failure %t", runtime.fault, wantFault)
			}
			if root.prepared.Effects[0].Settlement.Status() != SettlementStatusFailed {
				t.Fatal("rejection lost its failed settlement")
			}
		})
	}
}
