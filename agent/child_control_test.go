package agent

import (
	"encoding/json"
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
	child := controlValue(newProcessID())
	other := controlValue(newProcessID())
	request := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:control")), WaitID{}, []byte(`{"direction":"inspect"}`)))
	for _, operation := range []frameworkEffectOperation{frameworkEffectSignalChild, frameworkEffectCancelChild} {
		var effect Effect
		if operation == frameworkEffectSignalChild {
			effect = controlValue(SignalChild(child, request))
		} else {
			effect = controlValue(CancelChild(child, "stop work"))
		}
		var roundTrip Effect
		if err := json.Unmarshal(controlValue(json.Marshal(effect)), &roundTrip); err != nil || !sameBoundaryEffect(effect, roundTrip) {
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
			payload := controlValue(json.Marshal(result))
			var decoded ChildControlResult
			if err := json.Unmarshal(payload, &decoded); err != nil || !decoded.Matches(effect) || decoded.ChildID() != child {
				t.Fatalf("result codec: %v", err)
			}
			if _, present := decoded.SignalID(); present != (operation == frameworkEffectSignalChild) {
				t.Fatal("incorrect signal identity presence")
			}
			if _, present := decoded.Failure(); present != failed {
				t.Fatal("incorrect failure presence")
			}
			schema := controlValue(SchemaFor[ChildControlResult]())
			if err := schema.ValidateOutput(controlValue(ParseOutput(payload))); err != nil {
				t.Fatal(err)
			}
			signal := controlValue(newSignal(controlValue(ParseSignalID("signal:receipt")), WaitID{}, payload))
			if got, err := ParseChildControlResult(signal); err != nil || !got.Matches(effect) {
				t.Fatal(err)
			}
			id := deriveEffectID(child, 1, 0)
			status := SettlementStatusSucceeded
			if failed {
				status = SettlementStatusFailed
			}
			record := preparedEffect{ID: id, Effect: effect, Phase: effectPhaseSettled, Settlement: new(controlValue(NewSettlement(id, status, payload)))}
			if err := record.validateChildControl(); err != nil {
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
				"addressed": func(record *preparedEffect) { record.WaitID = new(deriveWaitID(id)) },
				"other recipient": func(record *preparedEffect) {
					if operation == frameworkEffectSignalChild {
						record.Effect = controlValue(SignalChild(other, request))
					} else {
						record.Effect = controlValue(CancelChild(other, "stop"))
					}
				},
			} {
				t.Run(string(operation)+"/"+name, func(t *testing.T) {
					altered := record
					mutate(&altered)
					if err := altered.validateChildControl(); err == nil {
						t.Fatal("inconsistent settlement accepted")
					}
				})
			}
		}
	}
	if _, err := SignalChild(ProcessID{}, request); !errors.Is(err, ErrInvalidChildControl) {
		t.Fatal(err)
	}
	if _, err := SignalChild(child, SignalRequest{}); !errors.Is(err, ErrInvalidChildControl) {
		t.Fatal(err)
	}
	if _, err := CancelChild(child, " "); !errors.Is(err, ErrInvalidChildControl) {
		t.Fatal(err)
	}
	if _, err := CancelChild(ProcessID{}, "stop"); !errors.Is(err, ErrInvalidChildControl) {
		t.Fatal(err)
	}
	if _, err := ParseChildControlResult(Signal{}); !errors.Is(err, ErrInvalidSignal) {
		t.Fatal(err)
	}
	if _, err := json.Marshal(ChildControlResult{}); err == nil {
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
		if err := json.Unmarshal([]byte(payload), &result); err == nil {
			t.Fatal("accepted malformed result", payload)
		}
	}
}

func TestSignalRequestWireSchemaAndOpeningIdentity(t *testing.T) {
	id := controlValue(ParseSignalID("signal:opening"))
	wait := deriveWaitID(deriveEffectID(controlValue(newProcessID()), 1, 0))
	request := controlValue(NewSignalRequest(id, wait, []byte(`"request"`)))
	payload := controlValue(json.Marshal(request))
	var decoded SignalRequest
	if err := json.Unmarshal(payload, &decoded); err != nil || !decoded.Valid() || decoded.ID() != id || string(decoded.Payload()) != `"request"` {
		t.Fatal(err)
	}
	if got, addressed := decoded.WaitID(); !addressed || got != wait {
		t.Fatal("wait identity lost")
	}
	if err := controlValue(SchemaFor[SignalRequest]()).ValidateInput(controlValue(ParseInput(payload))); err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(SignalRequest{}); err == nil {
		t.Fatal("encoded invalid request")
	}
	var nilRequest *SignalRequest
	if err := nilRequest.UnmarshalJSON(payload); !errors.Is(err, ErrInvalidSignalRequest) {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"id":"signal:1","payload":1,"extra":1}`), &decoded); err == nil {
		t.Fatal("accepted unknown member")
	}
	mailbox := newSignalMailbox()
	signal := controlValue(request.signal())
	if err := mailbox.openWait(controlValue(ParseWaitKey("answer")), signal, true); err != nil {
		t.Fatal(err)
	}
	if accepted, err := mailbox.enqueue(StatusWaiting, signal, signalSourceExternal); accepted || !errors.Is(err, ErrSignalConflict) {
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
			runtime.processes[child.handle.processID] = child
			recipient := child.handle.processID
			switch target {
			case "self":
				recipient = parent.handle.processID
			case "foreign":
				child.handle.relation = rootProcessRelation(recipient)
			case "missing":
				recipient = controlValue(newProcessID())
			}
			request := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:control")), WaitID{}, []byte(`"steer"`)))
			effect := controlValue(SignalChild(recipient, request))
			id := deriveEffectID(parent.handle.processID, 1, 0)
			parent.prepared = &preparedStep{StepSequence: 1, Effects: preparedEffects{{ID: id, Effect: effect, Phase: effectPhasePending}}}
			record := &parent.prepared.Effects[0]
			runtime.controlChild(parent, 0, record, time.Now())
			if !record.definitelySettled() {
				t.Fatal("control not settled")
			}
			result := controlValue(decodeChildControlResult(record.Settlement.Payload()))
			failure, failed := result.Failure()
			if target != "direct" {
				if !failed || failure.Code() != childControlNotOwnedCode || child.mailbox.pendingCount() != 0 {
					t.Fatalf("authority failure=%+v", result)
				}
				return
			}
			if failed || child.mailbox.pendingCount() != 1 || child.usage.AcceptedSignals != 1 {
				t.Fatalf("delivery=%+v usage=%+v", result, child.usage)
			}
			wire := controlValue(decodeChildControlEffect(effect.Payload()))
			duplicate := runtime.applyChildControl(child, wire)
			if !duplicate.Matches(effect) || child.usage.AcceptedSignals != 1 {
				t.Fatal("deduplication changed accounting")
			}
			child.status = StatusPaused
			second := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:paused")), WaitID{}, []byte(`"queued"`)))
			paused := runtime.applyChildControl(child, controlValue(decodeChildControlEffect(controlValue(SignalChild(recipient, second)).Payload())))
			if _, failed := paused.Failure(); failed || child.status != StatusPaused {
				t.Fatal("signal resumed paused child")
			}
			child.status = StatusCompleted
			third := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:terminal")), WaitID{}, []byte(`"late"`)))
			rejected := runtime.applyChildControl(child, controlValue(decodeChildControlEffect(controlValue(SignalChild(recipient, third)).Payload())))
			if failure, failed := rejected.Failure(); !failed || failure.Code() != childSignalRejectedCode {
				t.Fatal("terminal input admitted")
			}
			cancel := controlValue(CancelChild(recipient, "stop"))
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
	parentID := controlValue(newProcessID())
	childID := controlValue(newProcessID())
	request := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:cut")), WaitID{}, []byte(`"instruction"`)))
	effect := controlValue(SignalChild(childID, request))
	result := ChildControlResult{childID: childID, operation: frameworkEffectSignalChild, signalID: request.ID()}
	id := deriveEffectID(parentID, 1, 0)
	record := preparedEffect{ID: id, Effect: effect, Phase: effectPhaseSettled,
		Settlement: new(controlValue(NewSettlement(id, SettlementStatusSucceeded, controlValue(json.Marshal(result)))))}
	receipt := newSignalRecord(controlValue(request.signal()), false).snapshot()
	receipt.ArrivalSequence = 1
	child := processSnapshotWire{ProcessID: childID, Status: StatusRunning,
		Relation: processRelationWire{ParentID: &parentID}, Mailbox: mailboxWire{Signals: []signalRecordWire{receipt}}}
	for name, mutate := range map[string]func(*processSnapshotWire){
		"missing receipt": func(child *processSnapshotWire) { child.Mailbox.Signals = nil },
		"wrong payload": func(child *processSnapshotWire) {
			child.Mailbox.Signals[0].PayloadDigest = ComputeDigest([]byte(`"other"`))
		},
		"opening receipt": func(child *processSnapshotWire) { child.Mailbox.Signals[0].OpensWait = true },
		"wrong wait":      func(child *processSnapshotWire) { child.Mailbox.Signals[0].WaitID = new(deriveWaitID(id)) },
		"wrong parent":    func(child *processSnapshotWire) { child.Relation.ParentID = new(controlValue(newProcessID())) },
		"not a child":     func(child *processSnapshotWire) { child.Relation.ParentID = nil },
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
	cancel := controlValue(CancelChild(childID, "stop"))
	canceled := ChildControlResult{childID: childID, operation: frameworkEffectCancelChild}
	record.Effect = cancel
	record.Settlement = new(controlValue(NewSettlement(id, SettlementStatusSucceeded, controlValue(json.Marshal(canceled)))))
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
	input := controlValue(EncodeInput(descriptor))
	if err := controlValue(SchemaFor[Descriptor]()).ValidateInput(input); err != nil {
		t.Fatal(err)
	}
	decoded := controlValue(input.Decode[Descriptor]())
	if decoded.Digest() != descriptor.Digest() {
		t.Fatal("descriptor identity changed")
	}
}
