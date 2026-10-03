package agent

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"
)

func controlValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func TestChildControlCodecAndExactSettlement(t *testing.T) {
	child := newProcessID()
	request := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:control")), WaitID{}, []byte(`{"direction":"inspect"}`)))
	for _, operation := range []frameworkOperationKind{frameworkOperationSignalChild, frameworkOperationCancelChild} {
		var effect Effect
		if operation == frameworkOperationSignalChild {
			effect = controlValue(NewChildSignalEffect(child, request))
		} else {
			effect = controlValue(NewChildCancelEffect(child, "stop work"))
		}
		var roundTrip Effect
		if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(effect)), &roundTrip); err != nil || !effect.equal(roundTrip) {
			t.Fatalf("effect codec: %v", err)
		}
		for _, failed := range []bool{false, true} {
			result := ChildControlResult{operation: operation}
			if failed {
				result.failure = controlValue(NewFailure(FailureKindContract, "test.rejected", "not admitted"))
			}
			payload := controlValue(jsonv2.Marshal(result))
			var decoded ChildControlResult
			if err := jsonv2.Unmarshal(payload, &decoded); err != nil || decoded != result {
				t.Fatalf("result codec: %v", err)
			}
			if _, present := decoded.Failure(); present != failed {
				t.Fatal("incorrect failure presence")
			}
			schema := controlValue(SchemaFor[ChildControlResult]())
			if err := schema.Validate((controlValue(ParsePayload(payload))).JSON()); err != nil {
				t.Fatal(err)
			}
			signal := controlValue(NewSignal(controlValue(ParseSignalID("signal:engine:receipt")), WaitID{}, payload))
			if got, err := ParseChildControlResult(signal); err != nil || got != result {
				t.Fatal(err)
			}
			id := child.effectID(1, 0)
			control := controlValue(decodeChildControlEffect(effect.Payload()))
			record := preparedEffect{ID: id, Effect: effect, progress: &effectProgress{}}
			if err := record.settleOperation(childControlOperation{request: control}, result.failure); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(record.settlement().Payload(), controlValue(normalizeJSON(payload, MaxPayloadBytes))) {
				t.Fatalf("derived settlement = %s, want %s", record.settlement().Payload(), payload)
			}
			wire := controlValue(record.wire())
			stored := wire.Progress.Settlement
			if stored.Status != SettlementStatusInvalid || stored.Payload != nil || (stored.Failure != nil) != failed {
				t.Fatalf("Framework settlement stored derived facts: %+v", stored)
			}
			restored := controlValue(wire.record(id))
			if !restored.settlement().equal(*record.settlement()) {
				t.Fatal("decoded settlement differs from the one its request determines")
			}
			wire.Progress.Settlement = &preparedSettlementWire{Status: record.settlement().Status(), Payload: payload}
			if _, err := wire.record(id); err == nil {
				t.Fatal("Framework settlement accepted a stored copy of its request")
			}
		}
	}
	if _, err := NewChildSignalEffect(ProcessID{}, request); !errors.Is(err, ErrInvalidChildControl) {
		t.Fatal(err)
	}
	if _, err := NewChildSignalEffect(child, SignalRequest{}); !errors.Is(err, ErrInvalidChildControl) {
		t.Fatal(err)
	}
	if _, err := NewChildCancelEffect(child, " "); !errors.Is(err, ErrInvalidChildControl) {
		t.Fatal(err)
	}
	if _, err := NewChildCancelEffect(ProcessID{}, "stop"); !errors.Is(err, ErrInvalidChildControl) {
		t.Fatal(err)
	}
	if _, err := ParseChildControlResult(Signal{}); !errors.Is(err, ErrInvalidSignal) {
		t.Fatal(err)
	}
	if _, err := jsonv2.Marshal(ChildControlResult{}); err == nil {
		t.Fatal("encoded invalid result")
	}
	var nilResult *ChildControlResult
	if err := nilResult.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, ErrInvalidChildControl) {
		t.Fatal(err)
	}
	for _, payload := range []string{`{}`, `null`, `{"operation":"signal_child","child_id":"bad"}`, `{"operation":"unknown"}`, `{"operation":"cancel_child","extra":true}`} {
		if _, err := decodeChildControlEffect([]byte(payload)); err == nil {
			t.Fatal("accepted malformed effect", payload)
		}
		var result ChildControlResult
		if err := jsonv2.Unmarshal([]byte(payload), &result); err == nil {
			t.Fatal("accepted malformed result", payload)
		}
	}
}

func TestSignalRequestWireSchemaAndOpeningIdentity(t *testing.T) {
	id := controlValue(ParseSignalID("signal:request"))
	wait := newProcessID().effectID(1, 0).waitID()
	request := controlValue(NewSignalRequest(id, wait, []byte(`"request"`)))
	payload := controlValue(jsonv2.Marshal(request))
	var decoded SignalRequest
	if err := jsonv2.Unmarshal(payload, &decoded); err != nil || !decoded.Valid() || decoded.ID() != id || string(decoded.Payload()) != `"request"` {
		t.Fatal(err)
	}
	if got, addressed := decoded.WaitID(); !addressed || got != wait {
		t.Fatal("wait identity lost")
	}
	if err := controlValue(SchemaFor[SignalRequest]()).Validate((controlValue(ParsePayload(payload))).JSON()); err != nil {
		t.Fatal(err)
	}
	if _, err := jsonv2.Marshal(SignalRequest{}); err == nil {
		t.Fatal("encoded invalid request")
	}
	var nilRequest *SignalRequest
	if err := nilRequest.UnmarshalJSON(payload); !errors.Is(err, ErrInvalidSignalRequest) {
		t.Fatal(err)
	}
	if err := jsonv2.Unmarshal([]byte(`{"id":"signal:1","payload":1,"extra":1}`), &decoded); err == nil {
		t.Fatal("accepted unknown member")
	}
	mailbox := newSignalMailbox()
	signal := controlValue(NewSignal(controlValue(ParseSignalID("signal:engine:opening")), wait, request.Payload()))
	if err := mailbox.openWait(controlValue(ParseWaitKey("answer")), signal); err != nil {
		t.Fatal(err)
	}
	if accepted, err := mailbox.enqueue(StatusWaiting, signal, signalSourceExternal); accepted || !errors.Is(err, ErrSignalRejected) {
		t.Fatalf("opening masqueraded as an answer: %t %v", accepted, err)
	}
}

func TestChildControlAdmissionUsesDirectOwnershipAndMailbox(t *testing.T) {
	for _, target := range []string{"direct", "self", "foreign", "missing"} {
		t.Run(target, func(t *testing.T) {
			runtime, parent := newChildCompletionTestProcess(t)
			_, child := newChildCompletionTestProcess(t)
			key := controlValue(ParseChildKey("worker"))
			child.handle.relation = childProcessRelation(child.handle.processID, parent.handle.relation, key)
			child.handle.budget = Budget{Steps: NewQuota(100), Effects: NewQuota(100), Signals: NewQuota(100)}
			runtime.addProcess(child)
			recipient := child.handle.processID
			switch target {
			case "self":
				recipient = parent.handle.processID
			case "foreign":
				recipient = newProcessID()
			case "missing":
				recipient = newProcessID()
			}
			request := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:control")), WaitID{}, []byte(`"steer"`)))
			effect := controlValue(NewChildSignalEffect(recipient, request))
			transition := controlValue(Continue(0, effect))
			if failure := prepareTestStep(parent, runtime.treeLimits, stepJobResult{transition: transition, candidate: parent.execution, candidateState: parent.committedExecutionState}); failure != nil {
				t.Fatal(failure.cause)
			}
			if err := parent.prepared.Effects[0].begin(); err != nil {
				t.Fatal(err)
			}
			record := &parent.prepared.Effects[0]
			runtime.controlChild(parent, 0, record, controlValue(decodeChildControlEffect(record.Effect.Payload())), effectAttempt{id: newEffectAttemptID(), startedAt: time.Now()})
			if runtime.fault != nil {
				t.Fatal(runtime.fault)
			}
			if runtime.writer.committing() {
				runtime.applyTreeCommitCompletion(<-runtime.writer.done)
			}
			if !record.definitelySettled() {
				t.Fatal("control not settled")
			}
			result := controlValue(decodeChildControlResult(record.settlement().Payload()))
			failure, failed := result.Failure()
			if target != "direct" {
				if !failed || failure.Code() != failureCodeEngineChildControlNotOwned || child.mailbox.pendingCount() != 0 {
					t.Fatalf("authority failure=%+v", result)
				}
				return
			}
			if failed || child.mailbox.pendingCount() != 1 || child.usage().AcceptedSignals != 1 {
				t.Fatalf("delivery=%+v usage=%+v", result, child.usage())
			}
			wire := controlValue(decodeChildControlEffect(effect.Payload()))
			duplicate := runtime.applyChildControl(child, wire)
			if !duplicate.Valid() || child.usage().AcceptedSignals != 1 {
				t.Fatal("deduplication changed accounting")
			}
			child.pause = pause{reason: "await operator"}
			second := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:paused")), WaitID{}, []byte(`"queued"`)))
			paused := runtime.applyChildControl(child, controlValue(decodeChildControlEffect(controlValue(NewChildSignalEffect(recipient, second)).Payload())))
			if _, failed := paused.Failure(); failed || child.status() != StatusPaused {
				t.Fatal("signal resumed paused child")
			}
			cancel := controlValue(NewChildCancelEffect(recipient, "stop"))
			runtime.applyChildControl(child, controlValue(decodeChildControlEffect(cancel.Payload())))
			if child.pendingControl.cancellation.owner != cancellationOwnerParent {
				t.Fatal("missing parent cancellation intent")
			}
			child.installTermination(controlValue((terminationInputs{outcome: completedOutcome()}).resolve()),
				controlValue(EncodePayload(childTestOutput{})), child.handle.startedAt)
			third := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:terminal")), WaitID{}, []byte(`"late"`)))
			rejected := runtime.applyChildControl(child, controlValue(decodeChildControlEffect(controlValue(NewChildSignalEffect(recipient, third)).Payload())))
			if failure, failed := rejected.Failure(); !failed || failure.Code() != failureCodeEngineChildSignalRejected {
				t.Fatal("terminal input admitted")
			}
			result = runtime.applyChildControl(child, controlValue(decodeChildControlEffect(cancel.Payload())))
			if _, failed := result.Failure(); failed || child.status() != StatusCompleted {
				t.Fatal("terminal cancellation changed result")
			}
		})
	}
}

func TestControlSnapshotRequiresRecipientSideEvidence(t *testing.T) {
	parentID := newProcessID()
	childID := newProcessID()
	request := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:cut")), WaitID{}, []byte(`"instruction"`)))
	effect := controlValue(NewChildSignalEffect(childID, request))
	result := ChildControlResult{operation: frameworkOperationSignalChild}
	id := parentID.effectID(1, 0)
	record := preparedEffect{ID: id, Effect: effect, progress: &effectProgress{settlement: new(controlValue(NewSettlement(id, SettlementStatusSucceeded, controlValue(jsonv2.Marshal(result)))))}}
	receipt := newSignalRecord(controlValue(request.signal()), false).wire()
	childKey := controlValue(ParseChildKey("recipient"))
	child := processSnapshotWire{ProcessID: childID,
		Relation: childProcessRelation(childID, rootProcessRelation(parentID), childKey), Mailbox: mailboxWire{Signals: []signalRecordWire{receipt}}}
	for name, mutate := range map[string]func(*processSnapshotWire){
		"missing receipt": func(child *processSnapshotWire) { child.Mailbox.Signals = nil },
		"wrong payload": func(child *processSnapshotWire) {
			child.Mailbox.Signals[0].Payload = []byte(`"other"`)
		},
		"wrong wait": func(child *processSnapshotWire) { child.Mailbox.Signals[0].WaitID = new(id.waitID()) },
		"wrong parent": func(child *processSnapshotWire) {
			child.Relation = childProcessRelation(childID, rootProcessRelation(newProcessID()), childKey)
		},
		"not a child": func(child *processSnapshotWire) { child.Relation = rootProcessRelation(childID) },
	} {
		t.Run(name, func(t *testing.T) {
			altered := child
			altered.Mailbox.Signals = []signalRecordWire{receipt}
			mutate(&altered)
			validation := treeSnapshotValidation{processes: map[ProcessID]processSnapshotWire{childID: altered}}
			if err := controlValue(decodeFrameworkOperation(record.Effect.Payload())).validateTree(&validation, parentID, record); err == nil {
				t.Fatal("one-sided control accepted")
			}
		})
	}
	validation := treeSnapshotValidation{processes: map[ProcessID]processSnapshotWire{childID: child}}
	if err := controlValue(decodeFrameworkOperation(record.Effect.Payload())).validateTree(&validation, parentID, record); err != nil {
		t.Fatal(err)
	}
	child.Mailbox.Signals[0].PayloadDigest = new(ComputeDigest(child.Mailbox.Signals[0].Payload))
	child.Mailbox.Signals[0].Payload = nil
	child.Mailbox.SignalCursor = 1
	validation.processes[childID] = child
	if err := controlValue(decodeFrameworkOperation(record.Effect.Payload())).validateTree(&validation, parentID, record); err != nil {
		t.Fatal("consumed receipt lost proof", err)
	}
	cancel := controlValue(NewChildCancelEffect(childID, "stop"))
	canceled := ChildControlResult{operation: frameworkOperationCancelChild}
	record.Effect = cancel
	record.progress.settlement = new(controlValue(NewSettlement(id, SettlementStatusSucceeded, controlValue(jsonv2.Marshal(canceled)))))
	if err := controlValue(decodeFrameworkOperation(record.Effect.Payload())).validateTree(&validation, parentID, record); err == nil {
		t.Fatal("cancellation without intent accepted")
	}
	child.PendingControl.CancellationOwner = cancellationOwnerParent
	validation.processes[childID] = child
	if err := controlValue(decodeFrameworkOperation(record.Effect.Payload())).validateTree(&validation, parentID, record); err != nil {
		t.Fatal(err)
	}
	child.PendingControl.CancellationOwner = ""
	child.Finish = &processFinish{Termination: Termination{cause: TerminationCauseCompletion}}
	validation.processes[childID] = child
	if err := controlValue(decodeFrameworkOperation(record.Effect.Payload())).validateTree(&validation, parentID, record); err != nil {
		t.Fatal("terminal cancellation rejected", err)
	}
}

func TestDescriptorParticipatesInTypedWireSchemas(t *testing.T) {
	descriptor := newChildTestDeployment(t).Descriptor()
	input := controlValue(EncodePayload(descriptor))
	if err := controlValue(SchemaFor[Descriptor]()).Validate(input.JSON()); err != nil {
		t.Fatal(err)
	}
	decoded := controlValue(input.Decode[Descriptor]())
	if decoded.Digest() != descriptor.Digest() {
		t.Fatal("descriptor identity changed")
	}
}

func TestSignalChildRejectsEngineSignalIdentity(t *testing.T) {
	childID := newProcessID()
	waitID := childID.effectID(1, 0).waitID()
	internal := controlValue(NewSignal(waitID.childWaitSignalID(), waitID, []byte(`"done"`)))
	if _, err := NewChildSignalEffect(childID, SignalRequest(internal)); !errors.Is(err, ErrInvalidChildControl) {
		t.Fatalf("child control accepted Engine identity: %v", err)
	}
}
