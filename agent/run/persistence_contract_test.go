package run

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"testing"
)

func TestDurableRecordCodecsPreserveRuntimeHistories(t *testing.T) {
	for _, scenario := range []string{"child reaped", "answered wait", "execution rejected"} {
		t.Run(scenario, func(t *testing.T) {
			store := NewMemoryStore()
			runtime := kernelRuntime(t, Config{Store: store, CheckpointInterval: 1})
			var program Program[int, int]
			switch scenario {
			case "child reaped":
				program = treeFixtureProgram(t, runtime, 1, false, false, Budget{})
			case "answered wait":
				program = lifecycleEchoProgram(t, runtime, "test.wire_wait")
			case "execution rejected":
				definition := &boundaryDefinition{descriptor: kernelDescriptor(t, "test.wire_rejection"), step: func(context.Context, Turn) (Transition, error) { return Transition{}, nil }}
				program = controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: definition, Revision: "one"}))
			}
			handle := controlValue(runtime.Start(t.Context(), program, StartRequest[int]{ID: foregroundStartID()}))
			ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer cancel()
			if scenario == "answered wait" {
				waitForKernelStep(t, ctx, handle, 1)
				if err := handle.Pause(ctx, "hold before answer"); err != nil {
					t.Fatal(err)
				}
				if err := handle.Answer(ctx, kernelWait().Key, 42); err != nil {
					t.Fatal(err)
				}
				if err := handle.Resume(ctx); err != nil {
					t.Fatal(err)
				}
			}
			result := controlValue(handle.Await(ctx))
			log, _, err := store.Load(ctx, handle.ID())
			if err != nil {
				t.Fatal(err)
			}
			encoded := controlValue(jsonv2.Marshal(log))
			var restored Log
			if err := jsonv2.Unmarshal(encoded, &restored); err != nil || restored.Validate() != nil || !bytes.Equal(encoded, controlValue(jsonv2.Marshal(restored))) {
				t.Fatalf("runtime history changed at the storage wire boundary: %v", err)
			}
			inspection := controlValue(restored.Inspect())
			process, found := inspection.Process(handle.ID())
			if !found || !process.Termination.equal(result.Termination()) || process.Usage != result.Usage() || !process.StartedAt.Equal(result.StartedAt()) || !process.FinishedAt.Equal(result.FinishedAt()) {
				t.Fatal("wire restoration changed terminal evidence")
			}
			if scenario == "answered wait" {
				if answered, err := inspection.Answered(handle.ID(), kernelWait().Key, controlValue(EncodePayload(42))); !answered || err != nil {
					t.Fatalf("wire restoration lost the answer receipt: %t %v", answered, err)
				}
			}
		})
	}
}

func TestLogEnvelopeRejectsSplicedOrIncompleteHistory(t *testing.T) {
	valid := integrityInitialLog(t)
	otherRoot := foregroundStartID().Root()
	for _, test := range []struct {
		name   string
		mutate func(*Log)
	}{
		{"absent root", func(log *Log) { log.Root = ProcessID{} }},
		{"child as root", func(log *Log) { log.Root = controlValue(log.Root.Child(controlValue(ParseChildKey("child")))) }},
		{"empty history", func(log *Log) { log.Entries = nil }},
		{"unactivated start", func(log *Log) { log.Entries = log.Entries[:1] }},
		{"foreign root declaration", func(log *Log) {
			value := log.Entries[0].Record.(Started)
			value.Process = otherRoot
			log.Entries[0].Record = value
		}},
		{"missing started record", func(log *Log) { log.Entries[0].Record = Activated{Epoch: 1} }},
		{"first epoch skipped", func(log *Log) { log.Entries[1].Record = Activated{Epoch: 2}; log.Entries[1].Epoch = 2 }},
		{"first activation replaced", func(log *Log) {
			log.Entries[1].Record = Reaped{Process: controlValue(log.Root.Child(controlValue(ParseChildKey("child"))))}
		}},
		{"LSN gap", func(log *Log) { log.Entries[1].LSN++ }},
		{"entry from another epoch", func(log *Log) { log.Entries[1].Epoch++ }},
		{"absent record", func(log *Log) { log.Entries[1].Record = nil }},
		{"activation jump", func(log *Log) { *log = integrityAppend(*log, Activated{Epoch: 3}) }},
		{"foreign process fact", func(log *Log) {
			*log = integrityAppend(*log, ExecutionFailed{Process: otherRoot, Failure: controlValue(NewFailure(FailureKindExecution, "test.failure", "failed")), At: log.Entries[0].Record.(Started).At})
		}},
		{"invalid resolved limits", func(log *Log) {
			value := log.Entries[0].Record.(Started)
			value.Limits.MaxDepth = 0
			log.Entries[0].Record = value
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			log := controlValue(valid.Clone())
			test.mutate(&log)
			if err := log.Validate(); err == nil {
				t.Fatal("invalid durable envelope was accepted")
			}
			if inspection, err := log.Inspect(); err == nil || len(inspection.Processes) != 0 {
				t.Fatal("invalid history became a public read model")
			}
			if cloned, err := log.Clone(); err == nil || cloned.Head() != 0 {
				t.Fatal("invalid history was reissued as an owned log")
			}
		})
	}
	if (Log{}).Head() != 0 || (Log{}).Epoch() != 0 {
		t.Fatal("absent history advertised committed progress")
	}
}

func TestCheckpointLookupCannotRewriteItsLog(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		log := publicBoundaryLog(t, terminal)
		before := controlValue(jsonv2.Marshal(log))
		checkpoint, cursor, found := log.LatestCheckpoint()
		if !found || cursor != log.Head() {
			t.Fatal("latest checkpoint was not selected from committed history")
		}
		publicBoundaryMutateRecords([]Entry{{Record: checkpoint}})
		after, err := jsonv2.Marshal(log)
		if err != nil || !bytes.Equal(before, after) || log.Validate() != nil {
			t.Fatal("checkpoint lookup exposed writable log storage")
		}
	}
}

func TestBatchWirePreservesWriterCoordinatesAndExactRetry(t *testing.T) {
	log := integrityInitialLog(t)
	started := log.Entries[0].Record.(Started)
	initial := Batch{Root: log.Root, Records: []Record{started, Activated{Epoch: 1}}}
	signal := SignalAdmitted{Process: log.Root, Signal: Signal{ID: controlValue(ParseSignalID("request")), Payload: started.Input}, At: started.At}
	for _, batch := range []Batch{
		initial,
		{Root: log.Root, Epoch: 1, After: 2, Records: []Record{signal}},
		{Root: log.Root, Epoch: 1, After: 3, Records: []Record{Activated{Epoch: 2}}},
	} {
		wire := controlValue(jsonv2.Marshal(batch))
		var restored Batch
		if err := jsonv2.Unmarshal(wire, &restored); err != nil || !bytes.Equal(wire, controlValue(jsonv2.Marshal(restored))) {
			t.Fatalf("batch changed across persistence: %s %v", wire, err)
		}
		entries := restored.Entries()
		head := entries[len(entries)-1]
		if epoch, retry, err := restored.Admit(head.Epoch, head.LSN, entries); err != nil || !retry || epoch != head.Epoch {
			t.Fatalf("exact acknowledgment retry failed: epoch=%d retry=%t error=%v", epoch, retry, err)
		}
	}
	foreign := signal
	foreign.Process = foregroundStartID().Root()
	for _, batch := range []Batch{
		{Root: log.Root, Records: []Record{signal}},
		{Root: log.Root, Records: []Record{started}},
		{Root: log.Root, Epoch: 1, Records: initial.Records},
		{Root: log.Root, Records: []Record{started, Activated{Epoch: 2}}},
		{Root: log.Root, Epoch: 1, After: 2, Records: []Record{foreign}},
		{Root: log.Root, Epoch: 1, After: 2, Records: []Record{Activated{Epoch: 3}}},
		{Root: log.Root, Epoch: 1, After: 2, Records: []Record{Activated{Epoch: 2}, Activated{Epoch: 3}}},
		{Root: log.Root, Epoch: Epoch(math.MaxUint64), After: 2, Records: []Record{Activated{Epoch: 1}}},
	} {
		if err := batch.Validate(); err == nil {
			t.Fatalf("invalid batch admitted: %+v", batch)
		}
		if _, _, err := batch.Admit(batch.Epoch, batch.After, nil); err == nil {
			t.Fatal("writer admission bypassed batch validity")
		}
	}
}

func TestLogRejectsFactsWithoutTheirOwningProcess(t *testing.T) {
	log := integrityInitialLog(t)
	started := log.Entries[0].Record.(Started)
	missing := controlValue(log.Root.Child(controlValue(ParseChildKey("missing"))))
	effect := controlValue(NewDispatcherEffect(started.Input.JSON()))
	id := EffectID{Process: missing, Step: 1}
	failure := controlValue(NewFailure(FailureKindExecution, "test.failure", "missing process"))
	for _, record := range []Record{
		SignalAdmitted{Process: missing, Signal: Signal{ID: controlValue(ParseSignalID("signal")), Payload: started.Input}, At: started.At},
		Stepped{Process: missing, Step: 1, Transition: controlValue(Continue(0))},
		EffectIntent{ID: id, Effect: effect, Policy: EffectPolicy{Replay: ReplayPolicyNever}, At: started.At},
		EffectSettled{ID: id, Outcome: controlValue(NewSettlement(SettlementStatusSucceeded, started.Input.JSON())), At: started.At},
		Exited{Process: missing, Termination: Termination{output: started.Input}, At: started.At},
		ExecutionFailed{Process: missing, Failure: failure, At: started.At},
		Reaped{Process: missing},
		ControlRequested{Process: missing, Kind: ControlPause, Reason: "pause", At: started.At},
	} {
		if _, _, err := EncodeRecord(record); err != nil {
			t.Fatalf("record must have a valid envelope: %T %v", record, err)
		}
		if err := integrityAppend(log, record).Validate(); !errors.Is(err, ErrInvalidLog) {
			t.Fatalf("%T advanced an absent process: %v", record, err)
		}
	}
}

func TestLogRejectsProgressThatDoesNotFollowItsPrefix(t *testing.T) {
	log := integrityInitialLog(t)
	started := log.Entries[0].Record.(Started)
	signal := SignalAdmitted{Process: log.Root, Signal: Signal{ID: controlValue(ParseSignalID("signal")), Payload: started.Input}, At: started.At}
	conflict := signal
	conflict.Signal.Payload = controlValue(EncodePayload("changed"))
	effect := controlValue(NewDispatcherEffect(started.Input.JSON()))
	step := Stepped{Process: log.Root, Step: 1, Transition: controlValue(Yield(0, []Effect{effect}))}
	intent := EffectIntent{ID: EffectID{Process: log.Root, Step: 1}, Effect: effect, Policy: EffectPolicy{Replay: ReplayPolicyNever}, At: started.At}
	changedIntent := intent
	changedIntent.Effect = controlValue(NewDispatcherEffect([]byte(`"changed"`)))
	escalated := intent
	escalated.Policy.RequiredCapabilities = controlValue(NewCapabilitySet(controlValue(ParseCapability("files.write"))))
	complete := Stepped{Process: log.Root, Step: 1, Transition: controlValue(Complete(0, started.Input))}
	failure := ExecutionFailed{Process: log.Root, Failure: controlValue(NewFailure(FailureKindExecution, "test.failure", "rejected")), At: started.At}
	pause := ControlRequested{Process: log.Root, Kind: ControlPause, Reason: "hold", At: started.At}
	resume := ControlRequested{Process: log.Root, Kind: ControlResume, At: started.At}
	for _, test := range []struct {
		name    string
		records []Record
	}{
		{"repeated start", []Record{started}},
		{"repeated signal", []Record{signal, signal}},
		{"conflicting signal", []Record{signal, conflict}},
		{"signal after terminal intent", []Record{complete, signal}},
		{"step skipped", []Record{Stepped{Process: log.Root, Step: 2, Transition: controlValue(Continue(0))}}},
		{"message never delivered", []Record{Stepped{Process: log.Root, Step: 1, Delivered: 1, Transition: controlValue(Continue(1))}}},
		{"intent without step", []Record{intent}},
		{"intent differs from step", []Record{step, changedIntent}},
		{"repeated intent", []Record{step, intent, intent}},
		{"intent exceeds capabilities", []Record{step, escalated}},
		{"next step before intents", []Record{step, Stepped{Process: log.Root, Step: 2, Transition: controlValue(Continue(0))}}},
		{"failure after terminal intent", []Record{complete, failure}},
		{"repeated failure", []Record{failure, failure}},
		{"repeated pause", []Record{pause, pause}},
		{"resume without pause", []Record{resume}},
		{"repeated resume", []Record{pause, resume, resume}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := integrityAppend(log, test.records...).Validate(); !errors.Is(err, ErrInvalidLog) {
				t.Fatalf("contradictory progress became committed evidence: %v", err)
			}
		})
	}
}

func TestRecoveredLogCannotExceedItsAcceptedByteLimits(t *testing.T) {
	for _, checkpointLimit := range []bool{false, true} {
		log := integrityInitialLog(t)
		started := log.Entries[0].Record.(Started)
		if checkpointLimit {
			started.Limits.MaxCheckpointBytes = NewQuota(1)
		} else {
			started.Limits.MaxRecordBytes = NewQuota(1)
		}
		log.Entries[0].Record = started
		state := controlValue(ParseExecutionState("typed_fixture", started.Input.JSON()))
		log = integrityAppend(log, Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{{Started: started, State: state}}})
		if err := log.Validate(); !errors.Is(err, ErrResourceLimitExceeded) {
			t.Fatalf("accepted limits did not bound recovered history: %v", err)
		}
		store := &integrityStore{log: log}
		runtime, program := integrityRuntime(t, store)
		if _, err := runtime.Recover(t.Context(), program, log.Root); !errors.Is(err, ErrResourceLimitExceeded) || store.appends != 0 {
			t.Fatalf("oversized log acquired a new writer: error=%v appends=%d", err, store.appends)
		}
	}
}

func TestPersistedAnswersCannotInventOrReuseAWait(t *testing.T) {
	log := integrityInitialLog(t)
	started := log.Entries[0].Record.(Started)
	wait := kernelWait()
	wait.MaxAnswerBytes = 2
	yield := Stepped{Process: log.Root, Step: 1, Transition: controlValue(Yield(0, nil, wait))}
	answer := SignalAdmitted{Process: log.Root, Signal: answerSignal(wait.Key, controlValue(EncodePayload(1))), At: started.At}
	for _, test := range []struct {
		name    string
		records []Record
	}{
		{"wait never declared", []Record{answer}},
		{"answer exceeds byte limit", []Record{yield, SignalAdmitted{Process: log.Root, Signal: answerSignal(wait.Key, controlValue(EncodePayload(100))), At: started.At}}},
		{"answer violates schema", []Record{yield, SignalAdmitted{Process: log.Root, Signal: answerSignal(wait.Key, controlValue(EncodePayload(""))), At: started.At}}},
		{"answered wait reused", []Record{yield, answer, Stepped{Process: log.Root, Step: 2, Delivered: 1, Transition: controlValue(Yield(1, nil, wait))}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := integrityAppend(log, test.records...).Validate(); !errors.Is(err, ErrInvalidLog) {
				t.Fatalf("persisted answer escaped its admitted wait: %v", err)
			}
		})
	}
}

func TestPersistedWorkCannotSpendUnfundedBudgets(t *testing.T) {
	for _, budget := range []Budget{{Steps: NewQuota(0)}, {Effects: NewQuota(0)}, {Messages: NewQuota(0)}} {
		log := integrityInitialLog(t)
		started := log.Entries[0].Record.(Started)
		started.Budget = budget
		log.Entries[0].Record = started
		effect := controlValue(NewDispatcherEffect(started.Input.JSON()))
		step := Stepped{Process: log.Root, Step: 1, Transition: controlValue(Yield(0, []Effect{effect}))}
		intent := EffectIntent{ID: EffectID{Process: log.Root, Step: 1}, Effect: effect, Policy: EffectPolicy{Replay: ReplayPolicyNever}, At: started.At}
		if err := integrityAppend(log, step, intent).Validate(); !errors.Is(err, ErrResourceLimitExceeded) {
			t.Fatalf("persisted work escaped accepted budgets: %+v %v", budget, err)
		}
		if budget.Messages.limited {
			signal := SignalAdmitted{Process: log.Root, Signal: Signal{ID: controlValue(ParseSignalID("unfunded")), Payload: started.Input}, At: started.At}
			if err := integrityAppend(log, signal).Validate(); !errors.Is(err, ErrResourceLimitExceeded) {
				t.Fatalf("persisted input created an unfunded message: %v", err)
			}
		}
	}
}

func TestCheckpointWireRequiresRecoverableProcessMaterialization(t *testing.T) {
	started := integrityInitialLog(t).Entries[0].Record.(Started)
	for _, checkpoint := range []Checkpoint{
		{},
		{StartedProcesses: 1, Processes: []ProcessCheckpoint{{Started: started}}},
		{StartedProcesses: 1, Processes: []ProcessCheckpoint{{State: controlValue(ParseExecutionState("fixture", []byte(`0`)))}}},
	} {
		if _, _, err := EncodeRecord(checkpoint); !errors.Is(err, ErrInvalidLog) {
			t.Fatalf("unrecoverable checkpoint encoded as committed state: %v", err)
		}
	}
}
