package agent

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestTreeSnapshotRejectsPendingExternalAnswersOutsideTheirContract(t *testing.T) {
	key := controlValue(ParseWaitKey("approval"))
	schema := controlValue(jsonschema.For[string]())
	for _, test := range []struct {
		name    string
		config  ExternalWaitSpecConfig
		payload func() json.RawMessage
		cause   error
	}{
		{
			name: "answer schema", config: ExternalWaitSpecConfig{Key: key, AnswerSchema: schema, AnswerIsOutput: true},
			payload: func() json.RawMessage { return json.RawMessage(`123`) }, cause: jsonschema.ErrValidation,
		},
		{
			name: "null answer schema", config: ExternalWaitSpecConfig{Key: key, AnswerSchema: schema, AnswerIsOutput: true},
			payload: func() json.RawMessage { return json.RawMessage(`null`) }, cause: jsonschema.ErrValidation,
		},
		{
			name: "answer bytes", config: ExternalWaitSpecConfig{Key: key, AnswerSchema: schema, MaxAnswerBytes: 8, AnswerIsOutput: true},
			payload: func() json.RawMessage { return json.RawMessage(`"approved"`) }, cause: ErrPayloadTooLarge,
		},
		{
			name: "answer output encoding", config: ExternalWaitSpecConfig{Key: key, AnswerIsOutput: true},
			payload: func() json.RawMessage {
				payload := bytes.Repeat([]byte("a"), MaxPayloadBytes)
				payload[0], payload[len(payload)-1] = '"', '"'
				return payload
			}, cause: ErrPayloadTooLarge,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := controlValue(NewExternalWaitSpec(test.config))
			engine, process, _ := startExternalWaitRestoreProcess(t, spec)
			before := controlValue(engine.CaptureTree(t.Context(), process.Relation().ProcessID()))
			waitID, _ := before.ProcessSnapshots()[0].WaitID()
			answer := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:invalid-answer")), waitID, test.payload()))
			if accepted, err := process.DeliverSignals(t.Context(), answer); accepted || !errors.Is(err, ErrSignalRejected) || !errors.Is(err, test.cause) {
				t.Fatalf("live answer admission=%t %v, want rejection with %v", accepted, err, test.cause)
			}
			after := controlValue(engine.CaptureTree(t.Context(), process.Relation().ProcessID()))
			if after.Digest() != before.Digest() {
				t.Fatal("rejected answer changed the acknowledged wait")
			}

			// Keep the captured identities and opening contract; only persisted
			// pending input is malformed. The parser must enforce the same rule.
			wire := controlValue(before.ProcessSnapshots()[0].wire())
			wire.Mailbox.Signals = append(wire.Mailbox.Signals, mailboxRecordWire(controlValue(answer.signal())))
			document := treeSnapshotDocument{
				IncarnationID: before.IncarnationID(), TreeLimits: before.state.TreeLimits,
				ProcessSnapshots: []json.RawMessage{controlValue(jsonv2.Marshal(wire))},
			}
			data := controlValue(jsonv2.Marshal(document))
			if _, err := ParseTreeSnapshot(data); !errors.Is(err, ErrInvalidTreeSnapshot) || !errors.Is(err, ErrSignalRejected) || !errors.Is(err, test.cause) {
				t.Fatalf("pending answer restore=%v, want invalid snapshot with %v", err, test.cause)
			}
		})
	}
}

func TestExternalWaitAnswerRestoresPendingAndConsumedThroughEngine(t *testing.T) {
	payload := json.RawMessage(`"approved"`)
	spec := controlValue(NewExternalWaitSpec(ExternalWaitSpecConfig{
		Key: controlValue(ParseWaitKey("approval")), AnswerSchema: controlValue(jsonschema.For[string]()),
		MaxAnswerBytes: len(payload), AnswerIsOutput: true,
	}))
	engine, process, deployment := startExternalWaitRestoreProcess(t, spec)
	if err := process.Pause(t.Context(), "retain the pending answer"); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, process, StatusPaused)
	before := inspectProcessSnapshot(t, process)
	waitID, _ := before.WaitID()
	answer := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:answer")), waitID, payload))
	invalid := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:invalid-answer")), waitID, []byte(`123`)))
	if accepted, err := process.DeliverSignals(t.Context(), answer, invalid); accepted || !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("mixed answer batch=%t %v, want atomic rejection", accepted, err)
	}
	if after := inspectProcessSnapshot(t, process); !bytes.Equal(before.JSON(), after.JSON()) {
		t.Fatal("rejected batch changed the wait or retained input")
	}
	if accepted, err := process.DeliverSignals(t.Context(), answer); !accepted || err != nil {
		t.Fatalf("valid answer admission=%t %v", accepted, err)
	}
	pending := controlValue(engine.CaptureTree(t.Context(), process.Relation().ProcessID()))

	restore := func(snapshot TreeSnapshot) (*Engine, *Process) {
		t.Helper()
		parsed := controlValue(ParseTreeSnapshot(snapshot.JSON()))
		if parsed.Digest() != snapshot.Digest() {
			t.Fatal("external answer round trip changed the acknowledged cut")
		}
		restoredEngine := controlValue(NewEngine(EngineConfig{TreeCommitter: newSnapshotTestCommitter(parsed)}))
		t.Cleanup(func() { mustCloseEngine(t, restoredEngine) })
		if err := restoredEngine.ValidateRestorableTree(t.Context(), deployment, parsed); err != nil {
			t.Fatal(err)
		}
		restored := controlValue(restoredEngine.RestoreTree(t.Context(), deployment, parsed))
		t.Cleanup(func() { finishExternalWaitRestoreProcess(t, restored) })
		return restoredEngine, restored
	}

	restoredEngine, restored := restore(pending)
	retained := inspectProcessSnapshot(t, restored)
	if retained.Status() != StatusPaused {
		t.Fatalf("restored status=%s, want Paused", retained.Status())
	}
	if _, waiting := retained.WaitID(); waiting {
		t.Fatal("pending accepted answer did not satisfy the restored wait")
	}
	for _, candidate := range []*Process{process, restored} {
		if accepted, err := candidate.DeliverSignals(t.Context(), answer); accepted || err != nil {
			t.Fatalf("duplicate answer admission=%t %v", accepted, err)
		}
		conflict := controlValue(NewSignalRequest(answer.ID(), waitID, invalid.Payload()))
		if accepted, err := candidate.DeliverSignals(t.Context(), conflict); accepted || !errors.Is(err, ErrSignalConflict) {
			t.Fatalf("conflicting answer admission=%t %v, want identity conflict", accepted, err)
		}
	}
	if err := restored.Resume(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := restored.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	result := controlValue(restored.Await(t.Context()))
	output, present := result.Termination().Output()
	if !present {
		t.Fatal("restored pending answer did not complete")
	}
	delivered := controlValue(output.Decode[Signal]())
	if delivered.ID() != answer.ID() || !bytes.Equal(delivered.Payload(), payload) {
		t.Fatal("restored pending answer changed its delivered identity or payload")
	}
	completed := controlValue(restoredEngine.CaptureTree(t.Context(), restored.Relation().ProcessID()))
	receipts := completed.ProcessSnapshots()[0].SignalReceipts()
	if len(receipts) != 2 || !receipts[1].Consumed() || !receipts[1].Matches(answer) {
		t.Fatal("consumed answer did not retain its exact admission receipt")
	}
	if _, pending := receipts[1].PendingSignal(); pending {
		t.Fatal("consumed answer retained a payload")
	}
	_, finished := restore(completed)
	if err := finished.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	retainedResult := controlValue(finished.Await(t.Context()))
	retainedOutput, retainedCompletion := retainedResult.Termination().Output()
	if !retainedCompletion || !bytes.Equal(retainedOutput.JSON(), output.JSON()) || retainedResult.Usage() != result.Usage() {
		t.Fatal("restoring consumed digest history changed the completed result")
	}
}

func startExternalWaitRestoreProcess(t *testing.T, spec ExternalWaitSpec) (*Engine, *Process, Deployment) {
	t.Helper()
	definition := &externalWaitRestoreDefinition{
		descriptor: controlValue(NewDescriptor(DescriptorConfig{
			Name: externalWaitRestoreKind, Description: "Return the accepted external answer.",
			InputSchema: controlValue(jsonschema.Parse([]byte("true"))), OutputSchema: controlValue(jsonschema.For[Signal]()),
			SignalSchema: controlValue(jsonschema.Parse([]byte("true"))),
		})),
		wait: spec,
	}
	deployment := controlValue(NewDeployment(DeploymentConfig{
		Definition: definition, ImplementationDigest: ComputeDigest([]byte(externalWaitRestoreKind)),
		ConfigurationDigest: ComputeDigest(controlValue(NewWaitEffect(spec)).Payload()),
	}))
	engine := controlValue(NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}))
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	process := controlValue(engine.Start(t.Context(), deployment, controlValue(ParsePayload([]byte(`{}`)))))
	t.Cleanup(func() { finishExternalWaitRestoreProcess(t, process) })
	waitForStatus(t, process, StatusWaiting)
	return engine, process, deployment
}

func finishExternalWaitRestoreProcess(t *testing.T, process *Process) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	if err := process.Kill(ctx, "test complete"); err != nil && !errors.Is(err, ErrProcessFinished) {
		t.Error(err)
	}
	if err := process.Join(ctx); err != nil {
		t.Error(err)
	}
}

const externalWaitRestoreKind = "test.external_wait_restore"

type externalWaitRestoreDefinition struct {
	descriptor Descriptor
	wait       ExternalWaitSpec
}

func (e *externalWaitRestoreDefinition) Descriptor() Descriptor { return e.descriptor }

func (*externalWaitRestoreDefinition) ChildDeployments() []Deployment { return nil }

func (e *externalWaitRestoreDefinition) Start(Payload) (Execution, error) {
	return &externalWaitRestoreExecution{wait: e.wait, state: externalWaitRestoreState{Phase: externalWaitRestoreReady}}, nil
}

func (e *externalWaitRestoreDefinition) Restore(_ context.Context, state ExecutionState) (Execution, error) {
	value, err := state.Decode[externalWaitRestoreState](externalWaitRestoreKind)
	if err != nil {
		return nil, err
	}
	if !value.valid() {
		return nil, ErrInvalidExecutionState
	}
	return &externalWaitRestoreExecution{wait: e.wait, state: value}, nil
}

type externalWaitRestorePhase string

const (
	externalWaitRestoreReady    externalWaitRestorePhase = "ready"
	externalWaitRestoreOpening  externalWaitRestorePhase = "opening"
	externalWaitRestoreWaiting  externalWaitRestorePhase = "waiting"
	externalWaitRestoreComplete externalWaitRestorePhase = "complete"
)

type externalWaitRestoreState struct {
	Phase  externalWaitRestorePhase `json:"phase"`
	WaitID WaitID                   `json:"wait_id,omitzero"`
}

func (e externalWaitRestoreState) valid() bool {
	switch e.Phase {
	case externalWaitRestoreReady, externalWaitRestoreOpening:
		return !e.WaitID.Valid()
	case externalWaitRestoreWaiting, externalWaitRestoreComplete:
		return e.WaitID.Valid()
	default:
		return false
	}
}

type externalWaitRestoreExecution struct {
	wait  ExternalWaitSpec
	state externalWaitRestoreState
}

func (e *externalWaitRestoreExecution) Step(_ context.Context, signals []Signal) (Transition, error) {
	switch e.state.Phase {
	case externalWaitRestoreReady:
		effect, err := NewWaitEffect(e.wait)
		if err != nil {
			return Transition{}, err
		}
		e.state.Phase = externalWaitRestoreOpening
		return Continue(0, effect)
	case externalWaitRestoreOpening:
		if len(signals) != 1 {
			return Transition{}, errors.New("external wait requires its opening")
		}
		waitID, err := ParseWaitOpened(signals[0])
		if err != nil {
			return Transition{}, err
		}
		e.state = externalWaitRestoreState{Phase: externalWaitRestoreWaiting, WaitID: waitID}
		return Wait(1, waitID)
	case externalWaitRestoreWaiting:
		if len(signals) != 1 {
			return Transition{}, errors.New("external wait requires one answer")
		}
		if waitID, addressed := signals[0].WaitID(); !addressed || waitID != e.state.WaitID {
			return Transition{}, errors.New("external wait answer has the wrong identity")
		}
		output, err := EncodePayload(signals[0])
		if err != nil {
			return Transition{}, err
		}
		e.state.Phase = externalWaitRestoreComplete
		return Complete(1, output)
	default:
		return Transition{}, errors.New("completed external wait cannot advance")
	}
}

func (e *externalWaitRestoreExecution) Snapshot() (ExecutionState, error) {
	return EncodeExecutionState(externalWaitRestoreKind, e.state)
}
