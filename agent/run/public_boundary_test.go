package run

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"iter"
	"math"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestCompiledReferenceUsesOneDerivedIdentity(t *testing.T) {
	runtime := kernelRuntime(t, Config{})
	program := treeFixtureProgram(t, runtime, 0, false, false, Budget{})
	reference := program.ProgramRef()
	encoded := controlValue(jsonv2.Marshal(reference))
	if err := controlValue(jsonschema.For[ProgramRef]()).Validate(encoded); err != nil {
		t.Fatal(err)
	}
	var decoded ProgramRef
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != reference || reference.Digest() != ComputeDigest(encoded) || reference.Build() != kernelTestBuild || reference.Name() != program.Descriptor().Name() {
		t.Fatal("program reference does not derive its identity from its frozen declaration")
	}
	if reference.String() != reference.Name()+"+"+reference.Digest().String() || (ProgramRef{}).String() != "<invalid-program-ref>" {
		t.Fatal("program reference display lost its validity or digest")
	}
	for _, test := range []struct {
		name   string
		change func(*programIdentityWire)
	}{
		{"name", func(w *programIdentityWire) { w.Name = "test.changed" }},
		{"build", func(w *programIdentityWire) { w.Build = "changed-build" }},
		{"contract", func(w *programIdentityWire) { w.Contract = ComputeDigest([]byte("changed-contract")) }},
		{"configuration", func(w *programIdentityWire) { w.Configuration = ComputeDigest([]byte("changed-configuration")) }},
		{"revision", func(w *programIdentityWire) { w.Revision = "changed-revision" }},
		{"children", func(w *programIdentityWire) { w.Children = ComputeDigest([]byte("changed-children")) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := reference.wire()
			test.change(&wire)
			changed := controlValue(jsonv2.Marshal(wire))
			var candidate ProgramRef
			if err := jsonv2.Unmarshal(changed, &candidate); err != nil {
				t.Fatal(err)
			}
			if candidate.Digest() == reference.Digest() || candidate.Digest() != ComputeDigest(changed) {
				t.Fatal("changed declaration did not change its derived identity")
			}
		})
	}
	for _, invalid := range []string{
		strings.Replace(string(encoded), `"revision":"parent"`, `"revision":" padded "`, 1),
		strings.Replace(string(encoded), `"revision":"parent",`, "", 1),
		strings.Replace(string(encoded), `"build":"`+kernelTestBuild+`"`, `"build":""`, 1),
		strings.Replace(string(encoded), `"name":"`+reference.Name()+`"`, `"name":"Invalid"`, 1),
		strings.TrimSuffix(string(encoded), "}") + `,"digest":"` + reference.Digest().String() + `"}`,
	} {
		decoded = reference
		if err := jsonv2.Unmarshal([]byte(invalid), &decoded); err == nil || decoded != reference {
			t.Fatalf("invalid declaration replaced a valid reference: %s, %v", invalid, err)
		}
	}
}

func TestDescriptorSchemaFailuresStayAtTheContractBoundary(t *testing.T) {
	descriptor := kernelDescriptor(t, "test.descriptor-boundary")
	encoded := controlValue(jsonv2.Marshal(descriptor))
	if err := controlValue(jsonschema.For[Descriptor]()).Validate(encoded); err != nil {
		t.Fatal(err)
	}
	if !descriptor.SignalSchema().Equal(descriptor.InputSchema()) || descriptor.ValidateSignal(controlValue(EncodePayload(1))) != nil {
		t.Fatal("the advertised signal schema differs from its admitted contract")
	}
	for _, test := range []struct {
		name   string
		change func(*descriptorContractWire)
	}{
		{"input", func(w *descriptorContractWire) { w.InputSchema = json.RawMessage(`{"type":"unknown"}`) }},
		{"output", func(w *descriptorContractWire) { w.OutputSchema = json.RawMessage(`{"type":"unknown"}`) }},
		{"signal", func(w *descriptorContractWire) { w.SignalSchema = json.RawMessage(`{"type":"unknown"}`) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := descriptor.contractWire()
			test.change(&wire)
			decoded := descriptor
			if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(wire)), &decoded); !errors.Is(err, ErrInvalidDescriptor) || decoded.Digest() != descriptor.Digest() {
				t.Fatalf("invalid schema changed the accepted contract: %v", err)
			}
		})
	}
	for _, config := range []DescriptorConfig{
		{Name: descriptor.Name(), Description: descriptor.Description(), OutputSchema: descriptor.OutputSchema()},
		{Name: descriptor.Name(), Description: descriptor.Description(), InputSchema: descriptor.InputSchema()},
	} {
		if value, err := NewDescriptor(config); !errors.Is(err, ErrInvalidDescriptor) || value.Valid() {
			t.Fatalf("missing schema admitted: %v", err)
		}
	}
	for _, validate := range []func(Payload) error{(Descriptor{}).ValidateInput, (Descriptor{}).ValidateOutput, (Descriptor{}).ValidateSignal} {
		if err := validate(controlValue(EncodePayload(1))); !errors.Is(err, ErrInvalidDescriptor) {
			t.Fatalf("missing contract accepted a payload: %v", err)
		}
	}
	if _, err := descriptor.EncodeInput(make(chan int)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("unsupported typed input: %v", err)
	}
	if _, err := descriptor.DecodeOutput[int](controlValue(EncodePayload("wrong schema"))); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("output schema mismatch: %v", err)
	}
	if _, err := descriptor.DecodeOutput[string](controlValue(EncodePayload(1))); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("output type mismatch: %v", err)
	}
}

func TestSettlementOwnsItsStatusAndDetachesPayload(t *testing.T) {
	for _, status := range []SettlementStatus{SettlementStatusSucceeded, SettlementStatusFailed, SettlementStatusUnknown} {
		t.Run(status.String(), func(t *testing.T) {
			raw := json.RawMessage(` {"value": 1} `)
			var settlement Settlement
			if status == SettlementStatusFailed {
				failure := controlValue(NewFailure(FailureKindExternal, "test.operation.failed", "operation failed"))
				settlement = controlValue(NewFailedSettlement(failure, controlValue(ParsePayload(raw))))
			} else {
				settlement = controlValue(NewSettlement(status, controlValue(ParsePayload(raw))))
			}
			raw[0] = '['
			returned := settlement.Payload().JSON()
			returned[0] = '['
			if !settlement.Valid() || settlement.Status() != status || string(settlement.Payload().JSON()) != `{"value":1}` {
				t.Fatal("settlement admitted a mutable or competing result")
			}
			encoded := controlValue(jsonv2.Marshal(settlement))
			if err := controlValue(jsonschema.For[Settlement]()).Validate(encoded); err != nil {
				t.Fatal(err)
			}
			var decoded Settlement
			if err := jsonv2.Unmarshal(encoded, &decoded); err != nil || decoded.Status() != status || !bytes.Equal(decoded.Payload().JSON(), settlement.Payload().JSON()) {
				t.Fatalf("settlement round trip changed result: %v", err)
			}
		})
	}
	if SettlementStatusInvalid.Valid() || SettlementStatusInvalid.String() != invalidEnumName {
		t.Fatal("absent settlement status appears valid")
	}
	if settlement, err := NewSettlement(SettlementStatusInvalid, controlValue(ParsePayload(json.RawMessage(`null`)))); !errors.Is(err, ErrInvalidSettlement) || settlement.Valid() {
		t.Fatalf("missing status accepted: %v", err)
	}
	if _, err := NewSettlement(SettlementStatusFailed, controlValue(ParsePayload(json.RawMessage(`null`)))); !errors.Is(err, ErrInvalidSettlement) {
		t.Fatalf("untyped failure constructor accepted: %v", err)
	}
	if settlement, err := NewSettlement(SettlementStatusSucceeded, Payload{}); !errors.Is(err, ErrInvalidSettlement) || settlement.Valid() {
		t.Fatalf("absent settlement payload admitted: %v", err)
	}
	diagnostic := controlValue(NewFailure(FailureKindExternal, "test.operation.failed", "operation failed"))
	accepted := controlValue(NewFailedSettlement(diagnostic, Payload{}))
	for _, raw := range []string{
		`{"status":"invalid","payload":null}`,
		`{"status":"failed","payload":null}`,
		`{"status":"failed","failure":{"kind":"external","code":"test.operation.failed","message":""}}`,
		`{"status":"succeeded","payload":null,"failure":{"kind":"external","code":"test.operation.failed","message":"failed"}}`,
		`{"status":"succeeded"}`,
		`{"payload":null}`,
		`{"status":"succeeded","payload":null,"success":true}`,
		`{"status":"failed","status":"succeeded","payload":null}`,
	} {
		candidate := accepted
		if err := candidate.UnmarshalJSON([]byte(raw)); !errors.Is(err, ErrInvalidSettlement) || candidate.Status() != accepted.Status() || !bytes.Equal(candidate.Payload().JSON(), accepted.Payload().JSON()) {
			t.Fatalf("invalid wire replaced the accepted settlement: %s, %v", raw, err)
		}
	}
}

func TestPublicStatusRoundTripsAndRejectsUnknownTransitions(t *testing.T) {
	for _, test := range []struct {
		status   Status
		terminal bool
	}{
		{StatusRunning, false}, {StatusWaiting, false}, {StatusPaused, false}, {StatusDraining, false},
		{StatusCompleted, true}, {StatusFailed, true}, {StatusCanceled, true}, {StatusTimedOut, true}, {StatusKilled, true},
	} {
		t.Run(test.status.String(), func(t *testing.T) {
			encoded := controlValue(jsonv2.Marshal(test.status))
			if schema, err := jsonschema.For[Status](); err != nil {
				t.Error(err)
			} else if validateErr := schema.Validate(encoded); validateErr != nil {
				t.Error(validateErr)
			}
			var decoded Status
			if err := jsonv2.Unmarshal(encoded, &decoded); err != nil || decoded != test.status || !decoded.Valid() || decoded.Terminal() != test.terminal {
				t.Fatalf("lifecycle status changed across wire: %s, %v", encoded, err)
			}
		})
	}
	if _, err := StatusInvalid.MarshalText(); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("absent status encoded: %v", err)
	}
	for _, text := range []string{"", "unknown", "waiting ", "Completed"} {
		status := StatusRunning
		if err := status.UnmarshalText([]byte(text)); !errors.Is(err, ErrInvalidStatus) || status != StatusRunning {
			t.Fatalf("invalid status replaced running: %q, %v", text, err)
		}
		if err := controlValue(jsonschema.For[Status]()).Validate(controlValue(jsonv2.Marshal(text))); err == nil {
			t.Fatalf("derived status schema admitted unknown lifecycle %q", text)
		}
	}
}

func TestControlTerminationKeepsOneTerminalOwner(t *testing.T) {
	for _, test := range []struct {
		cause  TerminationCause
		status Status
	}{
		{TerminationCauseRuntimeKill, StatusKilled},
		{TerminationCauseParentDeadline, StatusTimedOut},
		{TerminationCauseHostDeadline, StatusTimedOut},
		{TerminationCauseParentCancellation, StatusCanceled},
		{TerminationCauseHostCancellation, StatusCanceled},
	} {
		t.Run(test.cause.String(), func(t *testing.T) {
			wire := terminationWire{Cause: test.cause, Reason: "stopped by owner"}
			encoded := controlValue(jsonv2.Marshal(wire))
			if err := controlValue(jsonschema.For[Termination]()).Validate(encoded); err != nil {
				t.Fatal(err)
			}
			var value Termination
			if err := jsonv2.Unmarshal(encoded, &value); err != nil || !value.Valid() || value.Status() != test.status || value.Cause() != test.cause || value.Reason() != wire.Reason {
				t.Fatalf("control termination changed: %v", err)
			}
			if _, present := value.Output(); present {
				t.Fatal("control termination fabricated an output")
			}
			if _, present := value.Failure(); present {
				t.Fatal("control termination fabricated a failure")
			}
			if !bytes.Equal(controlValue(jsonv2.Marshal(value)), encoded) {
				t.Fatal("control termination wire gained derived facts")
			}
		})
	}
	var complete Termination
	if err := jsonv2.Unmarshal([]byte(`{"output":null}`), &complete); err != nil || complete.Cause() != TerminationCauseCompletion || complete.Status() != StatusCompleted {
		t.Fatalf("explicit null must remain a completed output: %v", err)
	}
	if output, present := complete.Output(); !present || string(output.JSON()) != "null" {
		t.Fatal("explicit null output became absent")
	}
	for _, raw := range []string{
		`{"cause":"completion","output":null}`,
		`{"reason":"completed","output":null}`,
		`{"cause":"execution_failure","reason":"missing failure"}`,
		`{"cause":"host_cancellation","reason":"stopped","output":null}`,
		`{"cause":"unknown","reason":"unknown"}`,
		`{"output":null,"unresolved_effect_ids":[]}`,
	} {
		value := complete
		if err := jsonv2.Unmarshal([]byte(raw), &value); !errors.Is(err, errInvalidTermination) || !value.equal(complete) {
			t.Fatalf("competing terminal owner accepted: %s, %v", raw, err)
		}
	}
	if TerminationCauseInvalid.Valid() || TerminationCauseInvalid.String() != invalidEnumName {
		t.Fatal("invalid termination cause appears valid")
	}
}

func TestEffectPolicyAndWaitSchemasRemainOwned(t *testing.T) {
	capability := controlValue(ParseCapability("test.call"))
	for _, replay := range []ReplayPolicy{ReplayPolicyNever, ReplayPolicySameIdentity} {
		policy := EffectPolicy{Replay: replay, RequiredCapabilities: controlValue(NewCapabilitySet(capability))}
		encoded := controlValue(jsonv2.Marshal(policy))
		if schema, err := jsonschema.For[EffectPolicy](); err != nil {
			t.Error(err)
		} else if validateErr := schema.Validate(encoded); validateErr != nil {
			t.Error(validateErr)
		}
		var decoded EffectPolicy
		if err := jsonv2.Unmarshal(encoded, &decoded); err != nil || !decoded.Valid() || decoded.Replay.String() != string(replay) || !decoded.RequiredCapabilities.Contains(capability) {
			t.Fatalf("effect policy lost declared replay or authority: %v", err)
		}
		decoded.RequiredCapabilities.Values()[0] = Capability{}
		if !policy.RequiredCapabilities.Contains(capability) || !decoded.RequiredCapabilities.Contains(capability) {
			t.Fatal("returned capabilities rewrote effect authority")
		}
	}
	if ReplayPolicyInvalid.Valid() || ReplayPolicyInvalid.String() != invalidEnumName {
		t.Fatal("missing replay policy appears valid")
	}
	for _, raw := range []string{
		`{"replay":"retry","required_capabilities":[]}`,
		`{"replay":"never"}`,
		`{"replay":"never","required_capabilities":null}`,
		`{"replay":"never","required_capabilities":["test.call","test.call"]}`,
	} {
		policy := EffectPolicy{Replay: ReplayPolicySameIdentity}
		if err := jsonv2.Unmarshal([]byte(raw), &policy); err == nil || policy.Replay != ReplayPolicySameIdentity {
			t.Fatalf("invalid policy replaced existing declaration: %s, %v", raw, err)
		}
	}
	wait := kernelWait()
	wait.MaxAnswerBytes = len(`123`)
	encoded := controlValue(jsonv2.Marshal(wait))
	if schema, err := jsonschema.For[Wait](); err != nil {
		t.Error(err)
	} else if validateErr := schema.Validate(encoded); validateErr != nil {
		t.Error(validateErr)
	}
	var restored Wait
	if err := jsonv2.Unmarshal(encoded, &restored); err != nil || !restored.Valid() || restored.Key != wait.Key || !restored.AnswerSchema.Equal(wait.AnswerSchema) {
		t.Fatalf("wait lost its answer contract: %v", err)
	}
	if err := restored.Admits(controlValue(EncodePayload(123))); err != nil {
		t.Fatalf("answer at exact byte limit rejected: %v", err)
	}
	for _, answer := range []Payload{{}, controlValue(EncodePayload(1234)), controlValue(EncodePayload("x"))} {
		if err := restored.Admits(answer); !errors.Is(err, ErrSignalRejected) {
			t.Fatalf("answer violated size or schema: %v", err)
		}
	}
	untyped := Wait{Key: wait.Key}
	if err := untyped.Admits(controlValue(EncodePayload(true))); err != nil {
		t.Fatalf("optional answer schema invented a restriction: %v", err)
	}
}

func TestLogAndBatchClonesDetachEveryMutableContainer(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "pending"
		if completed {
			name = "completed"
		}
		t.Run(name, func(t *testing.T) {
			original := publicBoundaryLog(t, completed)
			before := controlValue(jsonv2.Marshal(original))
			cloned := controlValue(original.Clone())
			publicBoundaryMutateRecords(cloned.Entries)
			cloned.Entries[0] = Entry{}
			if !bytes.Equal(before, controlValue(jsonv2.Marshal(original))) || original.Validate() != nil {
				t.Fatal("Log.Clone exposed mutable committed facts")
			}
			batch := Batch{Root: original.Root, Records: make([]Record, len(original.Entries))}
			for index, entry := range original.Entries {
				batch.Records[index] = entry.Record
			}
			batchBefore := controlValue(jsonv2.Marshal(batch))
			batchClone := controlValue(batch.Clone())
			publicBoundaryMutateRecords(batchClone.Entries())
			batchClone.Records[0] = Activated{Epoch: 5}
			if !bytes.Equal(batchBefore, controlValue(jsonv2.Marshal(batch))) || !bytes.Equal(before, controlValue(jsonv2.Marshal(original))) {
				t.Fatal("Batch.Clone exposed caller-owned facts")
			}
		})
	}
	if clone, err := (Log{}).Clone(); !errors.Is(err, ErrInvalidLog) || len(clone.Entries) != 0 {
		t.Fatalf("invalid log cloned into evidence: %+v, %v", clone, err)
	}
	if clone, err := (Batch{}).Clone(); !errors.Is(err, ErrInvalidLog) || len(clone.Records) != 0 {
		t.Fatalf("invalid batch cloned into evidence: %+v, %v", clone, err)
	}
}

func TestInspectionCannotRewriteLogFailureEvidence(t *testing.T) {
	log := publicBoundaryLog(t, false)
	before := controlValue(jsonv2.Marshal(log))
	view := controlValue(log.Inspect())
	process, found := view.Process(log.Root)
	if !found || len(process.PendingEffects) != 1 || process.PendingEffects[0].Failure == nil {
		t.Fatal("unknown effect diagnostic is absent from inspection")
	}
	*process.PendingEffects[0].Failure = controlValue(NewFailure(FailureKindExternal, "test.overwritten", "rewritten by observer"))
	process.Waits[0] = Wait{}
	process.PendingEffects[0].Policy.Replay = ReplayPolicySameIdentity
	if !bytes.Equal(before, controlValue(jsonv2.Marshal(log))) {
		t.Fatal("inspection exposed a writable pointer into committed log evidence")
	}
}

func publicBoundaryLog(t *testing.T, completed bool) Log {
	t.Helper()
	log := integrityInitialLog(t)
	started := log.Entries[0].Record.(Started)
	var state ExecutionState
	if completed {
		termination := Termination{output: started.Input}
		log = integrityAppend(log,
			Stepped{Process: log.Root, Step: 1, Transition: controlValue(Complete(0, started.Input))},
			Exited{Process: log.Root, Termination: termination, At: started.At},
		)
	} else {
		effect := controlValue(NewDispatcherEffect(controlValue(ParsePayload(json.RawMessage(`{"operation":"inspect"}`)))))
		wait := kernelWait()
		remaining := wait
		remaining.Key = controlValue(ParseWaitKey("confirmation"))
		intent := EffectIntent{ID: EffectID{Process: log.Root, Step: 1}, Effect: effect, Policy: EffectPolicy{Replay: ReplayPolicyNever}, At: started.At}
		failure := controlValue(NewFailure(FailureKindExternal, "test.dispatch.failed", "external outcome is unknown"))
		log = integrityAppend(log,
			Stepped{Process: log.Root, Step: 1, Transition: controlValue(Yield(0, []Effect{effect}, wait, remaining))},
			intent,
			EffectSettled{ID: intent.ID, Outcome: controlValue(NewSettlement(SettlementStatusUnknown, controlValue(ParsePayload(json.RawMessage(`null`))))), Failure: &failure, At: started.At},
			SignalAdmitted{Process: log.Root, Signal: Signal{ID: controlValue(ParseSignalID("answer")), Wait: wait.Key, Payload: controlValue(EncodePayload(1))}, At: started.At},
		)
		state = controlValue(ParseExecutionState("fixture", started.Input.JSON()))
	}
	projected := controlValue(projectLog(log))
	checkpoint := Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{projected.processes[log.Root].materialize(state)}}
	log = integrityAppend(log, checkpoint)
	if err := log.Validate(); err != nil {
		t.Fatal(err)
	}
	return log
}

func publicBoundaryMutateRecords(entries []Entry) {
	for _, entry := range entries {
		switch record := entry.Record.(type) {
		case EffectSettled:
			if record.Failure != nil {
				*record.Failure = Failure{}
			}
		case Checkpoint:
			for index := range record.Processes {
				process := &record.Processes[index]
				for index := range process.Inbox {
					process.Inbox[index] = nil
				}
				clear(process.Receipts)
				clear(process.Waits)
				for index := range process.Pending {
					pending := &process.Pending[index]
					if pending.Outcome != nil {
						*pending.Outcome = Settlement{}
					}
					if pending.Failure != nil {
						*pending.Failure = Failure{}
					}
					*pending = PendingCheckpoint{}
				}
				if process.Terminal != nil {
					*process.Terminal = Termination{}
				}
				if process.Exited != nil {
					*process.Exited = Exited{}
				}
				*process = ProcessCheckpoint{}
			}
		}
	}
}

func TestBoundedDeltasDoNotChangeCommittedResults(t *testing.T) {
	deltas := make(chan Delta, 1)
	store := NewMemoryStore()
	runtime := kernelRuntime(t, Config{Store: store, Deltas: deltas})
	definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.delta-boundary"), step: func(_ context.Context, turn Turn) (Transition, error) {
		if turn.Step == 1 {
			return Yield(0, []Effect{controlValue(NewDispatcherEffect(controlValue(ParsePayload(json.RawMessage(`0`)))))})
		}
		return Complete(uint32(len(turn.Messages)), controlValue(EncodePayload(7)))
	}}
	requests := make(chan EffectRequest, 1)
	dispatcher := boundaryDispatcher{policy: EffectPolicy{Replay: ReplayPolicyNever}, dispatch: func(_ context.Context, request EffectRequest, emit DeltaEmitter) (Settlement, error) {
		requests <- request
		emit(nil)
		emit(json.RawMessage(`{`))
		payload := json.RawMessage(`{"text":"first"}`)
		emit(payload)
		payload[0] = '['
		emit(json.RawMessage(`"dropped by full channel"`))
		emit(json.RawMessage(`"` + strings.Repeat("x", maxDeltaBytes) + `"`))
		return NewSettlement(SettlementStatusSucceeded, controlValue(ParsePayload(json.RawMessage(`7`))))
	}}
	program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Dispatcher: dispatcher, Revision: "one"}))
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	handle := controlValue(runtime.Start(ctx, program, StartRequest[int]{ID: hostStartID()}))
	result := controlValue(handle.Await(ctx))
	if result.Termination().Status() != StatusCompleted || result.Usage() != (Usage{CommittedSteps: 2, PreparedEffects: 1, AcceptedMessages: 1}) {
		t.Fatalf("lossy increments changed durable outcome: %+v", result)
	}
	delta := <-deltas
	request := <-requests
	if !delta.Valid() || delta.ProcessID() != handle.ID() || delta.EffectID() != request.ID() || delta.Epoch() != request.Epoch() || delta.EffectSequence() != 3 || delta.EmittedAt().IsZero() || string(delta.Payload()) != `{"text":"first"}` {
		t.Fatalf("streamed increment lost its owner or immutable payload: %+v", delta)
	}
	delta.Payload()[0] = '['
	if string(delta.Payload()) != `{"text":"first"}` || (Delta{}).Valid() || len(deltas) != 0 {
		t.Fatal("delta escaped ownership or the bounded channel queued extra increments")
	}
	emitter := deltaEmitter{request: request, channel: deltas, sequence: math.MaxUint64 - 1}
	emitter.emit(json.RawMessage(`"last"`))
	last := <-deltas
	emitter.emit(json.RawMessage(`"cannot wrap"`))
	if last.EffectSequence() != math.MaxUint64 || len(deltas) != 0 {
		t.Fatal("exhausted delta sequence reused a logical coordinate")
	}
	log, found, err := store.Load(ctx, handle.ID())
	if err != nil || !found || log.Validate() != nil {
		t.Fatalf("full delta channel blocked committed history: %t, %v", found, err)
	}
}

type publicObservationStore struct {
	store Store
	load  func(context.Context, ProcessID) (Log, bool, error)
	read  func(context.Context, ProcessID, LSN) iter.Seq2[Entry, error]
}

func (p *publicObservationStore) Append(ctx context.Context, batch Batch) error {
	return p.store.Append(ctx, batch)
}

func (p *publicObservationStore) Read(ctx context.Context, root ProcessID, after LSN) iter.Seq2[Entry, error] {
	if p.read != nil {
		return p.read(ctx, root, after)
	}
	return p.store.Read(ctx, root, after)
}

func (p *publicObservationStore) Load(ctx context.Context, root ProcessID) (Log, bool, error) {
	if p.load != nil {
		return p.load(ctx, root)
	}
	return p.store.Load(ctx, root)
}

func TestDurableObserversRejectInvalidStoreEvidence(t *testing.T) {
	store := &publicObservationStore{store: NewMemoryStore()}
	runtime := kernelRuntime(t, Config{Store: store})
	program := treeFixtureProgram(t, runtime, 0, false, false, Budget{})
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	handle := controlValue(runtime.Start(ctx, program, StartRequest[int]{ID: hostStartID()}))
	if _, err := handle.Await(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	log, found, err := store.Load(ctx, handle.ID())
	if err != nil || !found {
		t.Fatalf("completed history is missing: found=%t, %v", found, err)
	}
	foreign := controlValue(ParseProcessID("different-root"))
	misplaced := controlValue(log.Clone())
	misplaced.Entries[0].Record = Started{Process: foreign, Program: log.Entries[0].Record.(Started).Program, Input: log.Entries[0].Record.(Started).Input, Limits: log.Entries[0].Record.(Started).Limits, At: log.Entries[0].Record.(Started).At}
	corrupt := controlValue(log.Clone())
	corrupt.Entries[1].LSN++
	sourceFailure := errors.New("observation storage unavailable")
	failing := func(context.Context, ProcessID, LSN) iter.Seq2[Entry, error] {
		return func(yield func(Entry, error) bool) { yield(Entry{}, sourceFailure) }
	}
	serve := func(history Log) func(context.Context, ProcessID, LSN) iter.Seq2[Entry, error] {
		return func(context.Context, ProcessID, LSN) iter.Seq2[Entry, error] {
			return func(yield func(Entry, error) bool) {
				for _, entry := range history.Entries {
					if !yield(entry, nil) {
						return
					}
				}
			}
		}
	}
	empty := serve(Log{})
	for _, test := range []struct {
		name       string
		load       func(context.Context, ProcessID) (Log, bool, error)
		read       func(context.Context, ProcessID, LSN) iter.Seq2[Entry, error]
		after      LSN
		observeErr error
		inspectErr error
	}{
		{"source failure", func(context.Context, ProcessID) (Log, bool, error) { return Log{}, false, sourceFailure }, failing, 0, sourceFailure, sourceFailure},
		{"missing tree", func(context.Context, ProcessID) (Log, bool, error) { return Log{}, false, nil }, empty, 0, ErrTreeNotFound, ErrProcessNotFound},
		{"foreign tree", nil, serve(misplaced), 0, ErrInvalidLog, nil},
		{"corrupt history", func(context.Context, ProcessID) (Log, bool, error) { return corrupt, true, nil }, serve(corrupt), 0, ErrInvalidLog, ErrInvalidLog},
		{"future cursor", nil, nil, log.Head() + 1, ErrCommitConflict, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			store.load, store.read = test.load, test.read
			reported := 0
			for entry, observeErr := range handle.Observe(ctx, test.after) {
				reported++
				if entry.LSN != 0 || entry.Epoch != 0 || entry.Record != nil || !errors.Is(observeErr, test.observeErr) {
					t.Fatalf("invalid source published a committed observation: %+v, %v", entry, observeErr)
				}
			}
			if reported != 1 {
				t.Fatalf("source failure reported %d times, want once", reported)
			}
			if _, inspectErr := handle.Inspect(ctx); !errors.Is(inspectErr, test.inspectErr) {
				t.Fatalf("inspection concealed source failure: %v", inspectErr)
			}
		})
	}
	store.read = nil
	store.load = nil
	var cursor LSN
	for entry, observeErr := range handle.Observe(ctx, 0) {
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		cursor = entry.LSN
		break
	}
	for entry, observeErr := range handle.Observe(ctx, cursor) {
		if observeErr != nil || entry.LSN != cursor+1 {
			t.Fatalf("resumed cursor repeated or omitted evidence: lsn=%d cursor=%d, %v", entry.LSN, cursor, observeErr)
		}
		cursor = entry.LSN
	}
	if cursor != log.Head() {
		t.Fatal("observation ended before the committed terminal head")
	}
	for _, observeErr := range handle.Observe(ctx, cursor) {
		t.Fatalf("terminal cursor produced another event: %v", observeErr)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	reported := 0
	for entry, observeErr := range handle.Observe(canceled, 0) {
		reported++
		if entry.Record != nil || !errors.Is(observeErr, context.Canceled) {
			t.Fatalf("canceled observation published evidence: %+v, %v", entry, observeErr)
		}
	}
	if reported != 1 {
		t.Fatal("canceled observation did not report its cancellation")
	}
	current := controlValue(handle.Inspect(ctx))
	if current.Head != log.Head() {
		t.Fatal("read operations advanced committed progress")
	}
}

func TestColdHandlesProjectOnlyCommittedTerminalResults(t *testing.T) {
	store := &publicObservationStore{store: NewMemoryStore()}
	writer := kernelRuntime(t, Config{Store: store})
	program := treeFixtureProgram(t, writer, 0, false, false, Budget{})
	request := StartRequest[int]{ID: hostStartID()}
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	active := controlValue(writer.Start(ctx, program, request))
	want := controlValue(active.Await(ctx))
	if err := writer.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reader := kernelRuntime(t, Config{Store: store})
	cold := controlValue(reader.Start(ctx, treeFixtureProgram(t, reader, 0, false, false, Budget{}), request))
	if cold.tree != nil || cold.Epoch() != active.Epoch() || cold.ProgramRef() != active.ProgramRef() {
		t.Fatal("duplicate start acquired execution authority or changed its binding")
	}
	log, found, err := store.Load(ctx, cold.ID())
	if err != nil || !found {
		t.Fatalf("completed history unavailable: found=%t, %v", found, err)
	}
	corrupt := controlValue(log.Clone())
	corrupt.Entries[1].LSN++
	beforeExit := controlValue(log.Clone())
	for index, entry := range beforeExit.Entries {
		if _, ok := entry.Record.(Exited); ok {
			beforeExit.Entries = beforeExit.Entries[:index]
			break
		}
	}
	if err := beforeExit.Validate(); err != nil {
		t.Fatal(err)
	}
	loadFailure := errors.New("terminal observation storage unavailable")
	for _, test := range []struct {
		name  string
		log   Log
		found bool
		cause error
		want  error
	}{
		{"store failure", Log{}, false, loadFailure, loadFailure},
		{"missing tree", Log{}, false, nil, ErrTreeNotFound},
		{"corrupt history", corrupt, true, nil, ErrInvalidLog},
	} {
		t.Run(test.name, func(t *testing.T) {
			store.load = func(context.Context, ProcessID) (Log, bool, error) { return test.log, test.found, test.cause }
			result, awaitErr := cold.Await(ctx)
			if result.Valid() || !errors.Is(awaitErr, test.want) {
				t.Fatalf("invalid evidence produced a terminal result: valid=%t, %v", result.Valid(), awaitErr)
			}
		})
	}
	for _, test := range []struct {
		name  string
		log   Log
		found bool
		cause error
		want  error
	}{
		{"store fails after terminal observation", Log{}, false, loadFailure, loadFailure},
		{"tree disappears after terminal observation", Log{}, false, nil, ErrTreeNotFound},
		{"history is corrupt after terminal observation", corrupt, true, nil, ErrInvalidLog},
		{"history regresses before terminal observation", beforeExit, true, nil, ErrInvalidLog},
	} {
		t.Run(test.name, func(t *testing.T) {
			loads := 0
			store.load = func(context.Context, ProcessID) (Log, bool, error) {
				loads++
				if loads == 1 {
					return log, true, nil
				}
				return test.log, test.found, test.cause
			}
			result, awaitErr := cold.Await(ctx)
			if awaitErr != nil {
				if result.Valid() || !errors.Is(awaitErr, test.want) {
					t.Fatalf("read failure concealed its source: valid=%t, %v", result.Valid(), awaitErr)
				}
				return
			}
			if !result.Valid() || controlValue(result.Output()) != controlValue(want.Output()) || result.ProcessID() != want.ProcessID() || !result.StartedAt().Equal(want.StartedAt()) || !result.FinishedAt().Equal(want.FinishedAt()) || result.Usage() != want.Usage() {
				t.Fatal("successful Await did not preserve its committed terminal evidence")
			}
		})
	}
	store.load = nil
	canceled, stop := context.WithCancel(ctx)
	stop()
	if result, err := cold.Await(canceled); result.Valid() || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cold read ignored its caller: valid=%t, %v", result.Valid(), err)
	}
	result := controlValue(cold.Await(ctx))
	if value, err := Result[string](result).Output(); err == nil || value != "" {
		t.Fatalf("typed output silently accepted an incompatible wire value: %q, %v", value, err)
	}
	if _, err := (Result[int]{}).Output(); err == nil {
		t.Fatal("absent result manufactured an output")
	}
}

func TestCanceledHandleCommandsCannotPublishAdmission(t *testing.T) {
	store := NewMemoryStore()
	runtime := kernelRuntime(t, Config{Store: store})
	program := lifecycleEchoProgram(t, runtime, "test.public.cancel")
	request := StartRequest[int]{ID: hostStartID()}
	ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
	defer cancel()
	active := controlValue(runtime.Start(ctx, program, request))
	waitForKernelStep(t, ctx, active, 1)
	reader := kernelRuntime(t, Config{Store: store})
	cold := controlValue(reader.Start(ctx, lifecycleEchoProgram(t, reader, "test.public.cancel"), request))
	before := controlValue(active.Inspect(ctx)).Head
	canceled, stop := context.WithCancel(ctx)
	stop()
	signal := Signal{ID: controlValue(ParseSignalID("canceled-input")), Payload: controlValue(EncodePayload(42))}
	outcome := controlValue(NewSettlement(SettlementStatusSucceeded, controlValue(ParsePayload([]byte(`42`)))))
	for _, handle := range []*Handle[int]{active, cold} {
		if accepted, err := handle.DeliverSignals(canceled, signal); accepted || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled delivery claimed admission: accepted=%t, %v", accepted, err)
		}
		effect := EffectID{Process: handle.ID(), Step: 1, Index: 0}
		for _, operation := range []struct {
			name string
			run  func(context.Context) error
		}{
			{"pause", func(ctx context.Context) error { return handle.Pause(ctx, "caller pause") }},
			{"resume", handle.Resume},
			{"cancel", func(ctx context.Context) error { return handle.Cancel(ctx, "caller cancel") }},
			{"kill", func(ctx context.Context) error { return handle.Kill(ctx, "caller kill") }},
			{"answer", func(ctx context.Context) error { return handle.Answer(ctx, kernelWait().Key, 42) }},
			{"resolve", func(ctx context.Context) error { return handle.ResolveUnknownEffect(ctx, effect, outcome) }},
			{"replay", func(ctx context.Context) error { return handle.ReplayUnknownEffect(ctx, effect) }},
		} {
			if err := operation.run(canceled); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled %s concealed its caller's cancellation: %v", operation.name, err)
			}
			if handle == cold {
				if err := operation.run(ctx); !errors.Is(err, ErrProcessNotActive) {
					t.Fatalf("cold handle gained %s authority: %v", operation.name, err)
				}
			}
		}
	}
	if after := controlValue(active.Inspect(ctx)).Head; after != before {
		t.Fatalf("rejected commands advanced committed history from %d to %d", before, after)
	}
	if err := active.Answer(ctx, kernelWait().Key, 42); err != nil {
		t.Fatal(err)
	}
	result := controlValue(active.Await(ctx))
	if output, err := result.Output(); err != nil || output != 42 {
		t.Fatalf("rejected commands consumed the real answer: %d, %v", output, err)
	}
}

func TestFailedSettlementRequiresDiagnosticAndKeepsDomainEvidenceSeparate(t *testing.T) {
	if _, err := NewFailedSettlement(Failure{}, Payload{}); !errors.Is(err, ErrInvalidFailure) {
		t.Fatal(err)
	}
	for _, status := range []SettlementStatus{SettlementStatusSucceeded, SettlementStatusUnknown} {
		settlement := controlValue(NewSettlement(status, controlValue(ParsePayload([]byte(`{"failure":"domain data"}`)))))
		if failure, present := settlement.Failure(); present || failure.Valid() {
			t.Fatal("domain payload was mistaken for a definite-failure diagnostic")
		}
	}
}
