package agent

import (
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
	other := newProcessID()
	request := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:control")), WaitID{}, []byte(`{"direction":"inspect"}`)))
	for _, operation := range []frameworkEffectOperation{frameworkEffectSignalChild, frameworkEffectCancelChild} {
		var effect Effect
		if operation == frameworkEffectSignalChild {
			effect = controlValue(NewChildSignalEffect(child, request))
		} else {
			effect = controlValue(NewChildCancelEffect(child, "stop work"))
		}
		var roundTrip Effect
		if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(effect)), &roundTrip); err != nil || !effect.equal(roundTrip) {
			t.Fatalf("effect codec: %v", err)
		}
		for _, failed := range []bool{false, true} {
			result := ChildControlResult{childID: child, operation: operation}
			if failed {
				result.failure = controlValue(NewFailure(FailureKindContract, "test.rejected", "not admitted"))
			}
			if operation == frameworkEffectSignalChild {
				result.signalID = request.ID()
			}
			payload := controlValue(jsonv2.Marshal(result))
			var decoded ChildControlResult
			if err := jsonv2.Unmarshal(payload, &decoded); err != nil || !decoded.Matches(effect) || decoded.ChildID() != child {
				t.Fatalf("result codec: %v", err)
			}
			if _, present := decoded.SignalID(); present != (operation == frameworkEffectSignalChild) {
				t.Fatal("incorrect signal identity presence")
			}
			if _, present := decoded.Failure(); present != failed {
				t.Fatal("incorrect failure presence")
			}
			schema := controlValue(SchemaFor[ChildControlResult]())
			if err := schema.Validate((controlValue(ParsePayload(payload))).JSON()); err != nil {
				t.Fatal(err)
			}
			signal := controlValue(NewSignal(controlValue(ParseSignalID("signal:engine:receipt")), WaitID{}, payload))
			if got, err := ParseChildControlResult(signal); err != nil || !got.Matches(effect) {
				t.Fatal(err)
			}
			id := child.effectID(1, 0)
			status := SettlementStatusSucceeded
			if failed {
				status = SettlementStatusFailed
			}
			record := preparedEffect{ID: id, Effect: effect, Phase: effectPhaseSettled, Settlement: new(controlValue(NewSettlement(id, status, payload)))}
			if err := record.validateFramework(); err != nil {
				t.Fatal(err)
			}
			for name, mutate := range map[string]func(*preparedEffect){
				"unknown": func(record *preparedEffect) {
					record.Settlement = new(controlValue(NewSettlement(id, SettlementStatusUnknown, []byte(`null`))))
				},
				"wrong status": func(record *preparedEffect) {
					wrong := SettlementStatusFailed
					if failed {
						wrong = SettlementStatusSucceeded
					}
					record.Settlement = new(controlValue(NewSettlement(id, wrong, payload)))
				},
				"addressed": func(record *preparedEffect) { record.WaitID = new(id.waitID()) },
				"other recipient": func(record *preparedEffect) {
					if operation == frameworkEffectSignalChild {
						record.Effect = controlValue(NewChildSignalEffect(other, request))
					} else {
						record.Effect = controlValue(NewChildCancelEffect(other, "stop"))
					}
				},
			} {
				t.Run(string(operation)+"/"+name, func(t *testing.T) {
					altered := record
					mutate(&altered)
					if err := altered.validateFramework(); err == nil {
						t.Fatal("inconsistent settlement accepted")
					}
				})
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
	if err := mailbox.openWait(controlValue(ParseWaitKey("answer")), signal, WaitKindExternal); err != nil {
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
			child.handle.childRequestDigest = ComputeDigest([]byte("control fixture"))
			child.limits.Budget = Budget{Steps: NewQuota(100), Effects: NewQuota(100), Signals: NewQuota(100)}
			parent.allocatedResources, _ = parent.limits.Budget.allocation(child.limits.Budget)
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
			if failure := prepareTestStep(parent, stepJobResult{transition: transition, candidate: parent.execution, candidateState: parent.committedExecutionState}); failure != nil {
				t.Fatal(failure.cause)
			}
			if err := parent.prepared.Effects[0].begin(); err != nil {
				t.Fatal(err)
			}
			record := &parent.prepared.Effects[0]
			runtime.controlChild(parent, 0, record, effectAttempt{id: newEffectAttemptID(), startedAt: time.Now()})
			if runtime.fault != nil {
				t.Fatal(runtime.fault)
			}
			if runtime.commit != nil {
				runtime.applyTreeCommitCompletion(<-runtime.commitDone)
			}
			if !record.definitelySettled() {
				t.Fatal("control not settled")
			}
			result := controlValue(decodeChildControlResult(record.Settlement.Payload()))
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
			if !duplicate.Matches(effect) || child.usage().AcceptedSignals != 1 {
				t.Fatal("deduplication changed accounting")
			}
			child.status = StatusPaused
			second := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:paused")), WaitID{}, []byte(`"queued"`)))
			paused := runtime.applyChildControl(child, controlValue(decodeChildControlEffect(controlValue(NewChildSignalEffect(recipient, second)).Payload())))
			if _, failed := paused.Failure(); failed || child.status != StatusPaused {
				t.Fatal("signal resumed paused child")
			}
			child.status = StatusCompleted
			third := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:terminal")), WaitID{}, []byte(`"late"`)))
			rejected := runtime.applyChildControl(child, controlValue(decodeChildControlEffect(controlValue(NewChildSignalEffect(recipient, third)).Payload())))
			if failure, failed := rejected.Failure(); !failed || failure.Code() != failureCodeEngineChildSignalRejected {
				t.Fatal("terminal input admitted")
			}
			cancel := controlValue(NewChildCancelEffect(recipient, "stop"))
			result = runtime.applyChildControl(child, controlValue(decodeChildControlEffect(cancel.Payload())))
			if _, failed := result.Failure(); failed || child.status != StatusCompleted {
				t.Fatal("terminal cancellation changed result")
			}
			child.status = StatusRunning
			runtime.applyChildControl(child, controlValue(decodeChildControlEffect(cancel.Payload())))
			if child.pendingControl.cancellation.owner != cancellationOwnerParent {
				t.Fatal("missing parent cancellation intent")
			}
		})
	}
}

func TestControlSnapshotRequiresRecipientSideEvidence(t *testing.T) {
	parentID := newProcessID()
	childID := newProcessID()
	request := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:cut")), WaitID{}, []byte(`"instruction"`)))
	effect := controlValue(NewChildSignalEffect(childID, request))
	result := ChildControlResult{childID: childID, operation: frameworkEffectSignalChild, signalID: request.ID()}
	id := parentID.effectID(1, 0)
	record := preparedEffect{ID: id, Effect: effect, Phase: effectPhaseSettled,
		Settlement: new(controlValue(NewSettlement(id, SettlementStatusSucceeded, controlValue(jsonv2.Marshal(result)))))}
	receipt := newSignalRecord(controlValue(request.signal()), false).wire()
	receipt.ArrivalSequence = 1
	child := processSnapshotWire{ProcessID: childID, Status: StatusRunning,
		Relation: processRelationWire{ParentID: &parentID}, Mailbox: mailboxWire{Signals: []signalRecordWire{receipt}}}
	for name, mutate := range map[string]func(*processSnapshotWire){
		"missing receipt": func(child *processSnapshotWire) { child.Mailbox.Signals = nil },
		"wrong payload": func(child *processSnapshotWire) {
			child.Mailbox.Signals[0].PayloadDigest = ComputeDigest([]byte(`"other"`))
		},
		"opening receipt": func(child *processSnapshotWire) {
			child.Mailbox.Signals[0].OpensWait = true
			child.Mailbox.Signals[0].Source = signalSourceSettlement
		},
		"wrong wait":   func(child *processSnapshotWire) { child.Mailbox.Signals[0].WaitID = new(id.waitID()) },
		"wrong parent": func(child *processSnapshotWire) { child.Relation.ParentID = new(newProcessID()) },
		"not a child":  func(child *processSnapshotWire) { child.Relation.ParentID = nil },
	} {
		t.Run(name, func(t *testing.T) {
			altered := child
			altered.Mailbox.Signals = []signalRecordWire{receipt}
			mutate(&altered)
			validation := treeSnapshotValidation{processes: map[ProcessID]processSnapshotWire{childID: altered}}
			if err := validation.validateChildControl(parentID, record); err == nil {
				t.Fatal("one-sided control accepted")
			}
		})
	}
	validation := treeSnapshotValidation{processes: map[ProcessID]processSnapshotWire{childID: child}}
	if err := validation.validateChildControl(parentID, record); err != nil {
		t.Fatal(err)
	}
	child.Mailbox.Signals[0].Payload = nil
	child.Mailbox.SignalCursor = 1
	validation.processes[childID] = child
	if err := validation.validateChildControl(parentID, record); err != nil {
		t.Fatal("consumed receipt lost proof", err)
	}
	cancel := controlValue(NewChildCancelEffect(childID, "stop"))
	canceled := ChildControlResult{childID: childID, operation: frameworkEffectCancelChild}
	record.Effect = cancel
	record.Settlement = new(controlValue(NewSettlement(id, SettlementStatusSucceeded, controlValue(jsonv2.Marshal(canceled)))))
	if err := validation.validateChildControl(parentID, record); err == nil {
		t.Fatal("cancellation without intent accepted")
	}
	child.PendingControl.CancellationOwner = cancellationOwnerParent
	validation.processes[childID] = child
	if err := validation.validateChildControl(parentID, record); err != nil {
		t.Fatal(err)
	}
	child.PendingControl.CancellationOwner = ""
	child.Status = StatusCompleted
	validation.processes[childID] = child
	if err := validation.validateChildControl(parentID, record); err != nil {
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
