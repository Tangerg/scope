package run

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"
)

func TestCheckpointCannotRewriteCommittedMetadata(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Checkpoint)
	}{
		{"started process count", func(c *Checkpoint) { c.StartedProcesses++ }},
		{"duplicate process", func(c *Checkpoint) { c.Processes = append(c.Processes, c.Processes[0]); c.StartedProcesses++ }},
		{"accepted input", func(c *Checkpoint) { c.Processes[0].Started.Input = controlValue(EncodePayload("rewritten")) }},
		{"process identity", func(c *Checkpoint) { c.Processes[0].Started.Process = controlValue(ParseProcessID("another-root")) }},
		{"step position", func(c *Checkpoint) { c.Processes[0].Step++ }},
		{"usage", func(c *Checkpoint) { c.Processes[0].Usage.CommittedSteps++ }},
		{"allocated quota", func(c *Checkpoint) { c.Processes[0].Allocated.Steps++ }},
		{"child history", func(c *Checkpoint) { c.Processes[0].ChildCount++ }},
		{"pause", func(c *Checkpoint) { c.Processes[0].Paused = true }},
		{"yield", func(c *Checkpoint) { c.Processes[0].Yielded = true }},
		{"inbox", func(c *Checkpoint) {
			c.Processes[0].Inbox = Inbox{Signal{ID: controlValue(ParseSignalID("invented")), Payload: controlValue(EncodePayload("invented"))}}
		}},
		{"signal receipt", func(c *Checkpoint) {
			c.Processes[0].Receipts = []SignalReceipt{{ID: controlValue(ParseSignalID("invented")), Digest: ComputeDigest([]byte("invented"))}}
		}},
		{"wait", func(c *Checkpoint) { c.Processes[0].Waits = []Wait{{Key: controlValue(ParseWaitKey("invented"))}} }},
		{"effect", func(c *Checkpoint) {
			started := c.Processes[0].Started
			c.Processes[0].Pending = []PendingCheckpoint{{Intent: EffectIntent{ID: EffectID{Process: started.Process, Step: 1}, Effect: controlValue(NewDispatcherEffect([]byte(`"invented"`))), Policy: EffectPolicy{Replay: ReplayPolicyNever}, At: started.At}}}
		}},
		{"terminal intent", func(c *Checkpoint) {
			c.Processes[0].Terminal = &Termination{output: controlValue(EncodePayload("invented"))}
		}},
		{"exit", func(c *Checkpoint) {
			c.Processes[0].Exited = &Exited{Process: c.Processes[0].Started.Process, Termination: Termination{output: controlValue(EncodePayload("invented"))}, At: c.Processes[0].Started.At}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			log := integrityInitialLog(t)
			started := log.Entries[0].Record.(Started)
			checkpoint := Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{{Started: started, State: controlValue(ParseExecutionState("fixture", started.Input.JSON()))}}}
			if err := integrityAppend(log, checkpoint).Validate(); err != nil {
				t.Fatalf("valid recovery baseline: %v", err)
			}
			test.change(&checkpoint)
			if _, _, err := EncodeRecord(checkpoint); err != nil {
				t.Fatalf("fixture must have valid wire structure: %v", err)
			}
			corrupted := integrityAppend(log, checkpoint)
			if err := corrupted.Validate(); !errors.Is(err, ErrInvalidLog) {
				t.Fatalf("checkpoint replaced committed metadata: %v", err)
			}
			store := &integrityStore{log: corrupted}
			runtime, program := integrityRuntime(t, store)
			if _, err := runtime.Recover(t.Context(), program, log.Root); !errors.Is(err, ErrInvalidLog) {
				t.Fatalf("recovered a forged checkpoint: %v", err)
			}
			if store.appends != 0 {
				t.Fatal("invalid checkpoint advanced the writer epoch")
			}
		})
	}
}

func TestExitedMustRepeatTheCommittedTerminalIntent(t *testing.T) {
	log := integrityInitialLog(t)
	started := log.Entries[0].Record.(Started)
	accepted := controlValue(EncodePayload("accepted"))
	committed := integrityAppend(log, Stepped{Process: log.Root, Step: 1, Transition: controlValue(Complete(0, accepted))})
	exit := Exited{Process: log.Root, Termination: Termination{output: accepted}, At: started.At}
	if err := integrityAppend(committed, exit).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		log  Log
	}{
		{"without terminal intent", integrityAppend(log, exit)},
		{"different output", integrityAppend(committed, Exited{Process: log.Root, Termination: Termination{output: controlValue(EncodePayload("forged"))}, At: started.At})},
		{"different cause", integrityAppend(committed, Exited{Process: log.Root, Termination: Termination{cause: TerminationCauseHostCancellation, reason: "forged cancellation"}, At: started.At})},
		{"duplicate exit", integrityAppend(committed, exit, exit)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.log.Validate(); !errors.Is(err, ErrInvalidLog) {
				t.Fatalf("accepted a contradictory terminal record: %v", err)
			}
		})
	}
}

func TestChildStartedRequiresItsExactCommittedSpawn(t *testing.T) {
	log := integrityInitialLog(t)
	parent := log.Entries[0].Record.(Started)
	key := controlValue(ParseChildKey("child"))
	child := Started{Process: controlValue(log.Root.Child(key)), Program: parent.Program, Input: parent.Input, At: parent.At}
	if err := integrityAppend(log, child).Validate(); !errors.Is(err, ErrInvalidChildStart) {
		t.Fatalf("child started without a Spawn intent: %v", err)
	}
	spawn := controlValue(NewChildStartEffect(ChildSpec{Key: key, ProgramRef: child.Program, Input: child.Input}))
	intent := EffectIntent{ID: EffectID{Process: log.Root, Step: 1}, Effect: spawn, Policy: EffectPolicy{Replay: ReplayPolicySameIdentity}, At: parent.At}
	prepared := integrityAppend(log, Stepped{Process: log.Root, Step: 1, Transition: controlValue(Yield(0, []Effect{spawn}))}, intent)
	settled := EffectSettled{ID: intent.ID, Outcome: controlValue(NewSettlement(SettlementStatusSucceeded, controlValue(EncodePayload(child.Process)).JSON())), At: parent.At}
	if err := integrityAppend(prepared, child).Validate(); !errors.Is(err, ErrInvalidLog) {
		t.Fatalf("child Started became visible without its Spawn receipt: %v", err)
	}
	if err := integrityAppend(prepared, child, settled).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*Started)
	}{
		{"input", func(s *Started) { s.Input = controlValue(EncodePayload("another input")) }},
		{"key", func(s *Started) {
			s.Process = controlValue(log.Root.Child(controlValue(ParseChildKey("another-child"))))
		}},
		{"root policy duplicated", func(s *Started) { s.Limits = parent.Limits }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := child
			test.change(&changed)
			if err := integrityAppend(prepared, changed, settled).Validate(); !errors.Is(err, ErrInvalidLog) {
				t.Fatalf("child Started disagreed with its Spawn: %v", err)
			}
		})
	}
	for _, test := range []struct {
		name   string
		change func(*EffectSettled)
	}{
		{"different effect", func(s *EffectSettled) { s.ID.Index++ }},
		{"different process", func(s *EffectSettled) {
			s.Outcome = controlValue(NewSettlement(SettlementStatusSucceeded, controlValue(EncodePayload(log.Root)).JSON()))
		}},
		{"failed admission", func(s *EffectSettled) {
			s.Outcome = controlValue(NewSettlement(SettlementStatusFailed, []byte(`"rejected"`)))
		}},
		{"unknown admission", func(s *EffectSettled) {
			s.Outcome = controlValue(NewSettlement(SettlementStatusUnknown, []byte(`"uncertain"`)))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := settled
			test.change(&changed)
			if err := integrityAppend(prepared, child, changed).Validate(); !errors.Is(err, ErrInvalidLog) {
				t.Fatalf("child Started disagreed with its committed admission receipt: %v", err)
			}
		})
	}
	if err := integrityAppend(prepared, child, Activated{Epoch: 2}, settled).Validate(); !errors.Is(err, ErrInvalidLog) {
		t.Fatalf("activation split child Started from its Spawn receipt: %v", err)
	}
}

func TestFrameworkAcknowledgmentRequiresTheCommittedRecipientChange(t *testing.T) {
	log := integrityInitialLog(t)
	parent := log.Entries[0].Record.(Started)
	key := controlValue(ParseChildKey("child"))
	child := Started{Process: controlValue(log.Root.Child(key)), Program: parent.Program, Input: parent.Input, At: parent.At}
	spawn := controlValue(NewChildStartEffect(ChildSpec{Key: key, ProgramRef: child.Program, Input: child.Input}))
	spawnIntent := EffectIntent{ID: EffectID{Process: log.Root, Step: 1}, Effect: spawn, Policy: EffectPolicy{Replay: ReplayPolicySameIdentity}, At: parent.At}
	spawned := integrityAppend(log,
		Stepped{Process: log.Root, Step: 1, Transition: controlValue(Yield(0, []Effect{spawn}))}, spawnIntent,
		child, EffectSettled{ID: spawnIntent.ID, Outcome: controlValue(NewSettlement(SettlementStatusSucceeded, controlValue(EncodePayload(child.Process)).JSON())), At: parent.At})
	signal := Signal{ID: controlValue(ParseSignalID("message")), Payload: controlValue(EncodePayload("delivered"))}
	for _, test := range []struct {
		name   string
		effect func(ProcessID) Effect
		change func(ProcessID) Record
	}{
		{"send", func(process ProcessID) Effect { return controlValue(NewChildSignalEffect(process, signal)) }, func(process ProcessID) Record {
			return SignalAdmitted{Process: process, Signal: signal, At: parent.At}
		}},
		{"cancel", func(process ProcessID) Effect { return controlValue(NewChildCancelEffect(process, "stop child")) }, func(process ProcessID) Record {
			return ControlRequested{Process: process, Kind: ControlCancel, Reason: "stop child", At: parent.At}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			effect := test.effect(child.Process)
			intent := EffectIntent{ID: EffectID{Process: log.Root, Step: 2}, Effect: effect, Policy: EffectPolicy{Replay: ReplayPolicySameIdentity}, At: parent.At}
			prepared := integrityAppend(spawned, Stepped{Process: log.Root, Step: 2, Delivered: 1, Transition: controlValue(Yield(1, []Effect{effect}))}, intent)
			receipt := EffectSettled{ID: intent.ID, Outcome: controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`{}`))), At: parent.At}
			if err := integrityAppend(prepared, test.change(child.Process), receipt).Validate(); err != nil {
				t.Fatalf("valid framework operation rejected: %v", err)
			}
			if err := integrityAppend(prepared, receipt).Validate(); !errors.Is(err, ErrInvalidLog) {
				t.Fatalf("framework acknowledgment invented an uncommitted recipient change: %v", err)
			}
			if changed, isSignal := test.change(child.Process).(SignalAdmitted); isSignal {
				changed.Signal.Payload = controlValue(EncodePayload("another value"))
				if err := integrityAppend(prepared, changed, receipt).Validate(); !errors.Is(err, ErrInvalidLog) {
					t.Fatalf("framework acknowledgment accepted a different signal payload: %v", err)
				}
			}
			unauthorized := test.effect(log.Root)
			intent.Effect = unauthorized
			wrongParent := integrityAppend(spawned, Stepped{Process: log.Root, Step: 2, Delivered: 1, Transition: controlValue(Yield(1, []Effect{unauthorized}))}, intent, test.change(log.Root), receipt)
			if err := wrongParent.Validate(); !errors.Is(err, ErrInvalidLog) {
				t.Fatalf("framework acknowledgment invented direct-child authority: %v", err)
			}
		})
	}
}

func TestFrameworkIntentHasOneKernelOwnedPolicy(t *testing.T) {
	log := integrityInitialLog(t)
	started := log.Entries[0].Record.(Started)
	capabilities := controlValue(NewCapabilitySet(controlValue(ParseCapability("test.external"))))
	started.Capabilities = capabilities
	log.Entries[0].Record = started
	key := controlValue(ParseChildKey("child"))
	child := controlValue(log.Root.Child(key))
	signal := Signal{ID: controlValue(ParseSignalID("answer")), Payload: started.Input}
	for _, test := range []struct {
		name   string
		effect Effect
	}{
		{"spawn", controlValue(NewChildStartEffect(ChildSpec{Key: key, ProgramRef: started.Program, Input: started.Input}))},
		{"send", controlValue(NewChildSignalEffect(child, signal))},
		{"cancel", controlValue(NewChildCancelEffect(child, "stop"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			step := Stepped{Process: log.Root, Step: 1, Transition: controlValue(Yield(0, []Effect{test.effect}))}
			intent := EffectIntent{ID: EffectID{Process: log.Root, Step: 1}, Effect: test.effect, Policy: EffectPolicy{Replay: ReplayPolicySameIdentity}, At: started.At}
			if err := integrityAppend(log, step, intent).Validate(); err != nil {
				t.Fatalf("kernel framework policy was rejected: %v", err)
			}
			for _, policy := range []EffectPolicy{
				{Replay: ReplayPolicyNever},
				{Replay: ReplayPolicySameIdentity, RequiredCapabilities: capabilities},
			} {
				intent.Policy = policy
				if err := integrityAppend(log, step, intent).Validate(); !errors.Is(err, ErrInvalidLog) {
					t.Errorf("framework intent accepted an external policy %+v: %v", policy, err)
				}
				if _, _, err := EncodeRecord(intent); !errors.Is(err, ErrInvalidLog) {
					t.Errorf("wire encoding accepted an external framework policy %+v: %v", policy, err)
				}
			}
		})
	}
	call := controlValue(NewDispatcherEffect(started.Input.JSON()))
	intent := EffectIntent{ID: EffectID{Process: log.Root, Step: 1}, Effect: call, Policy: EffectPolicy{Replay: ReplayPolicyNever, RequiredCapabilities: capabilities}, At: started.At}
	if err := integrityAppend(log, Stepped{Process: log.Root, Step: 1, Transition: controlValue(Yield(0, []Effect{call}))}, intent).Validate(); err != nil {
		t.Fatalf("dispatcher-owned policy was rejected: %v", err)
	}
}

func TestTerminalCheckpointHasNoOpaqueExecutionState(t *testing.T) {
	for _, exited := range []bool{false, true} {
		name := "terminal intent"
		if exited {
			name = "exited"
		}
		t.Run(name, func(t *testing.T) {
			log := integrityInitialLog(t)
			started := log.Entries[0].Record.(Started)
			terminal := Termination{output: started.Input}
			log = integrityAppend(log, Stepped{Process: log.Root, Step: 1, Transition: controlValue(Complete(0, started.Input))})
			process := ProcessCheckpoint{Started: started, Step: 1, Usage: Usage{CommittedSteps: 1}, Terminal: &terminal}
			if exited {
				exit := Exited{Process: log.Root, Termination: terminal, At: started.At}
				log = integrityAppend(log, exit)
				process.Exited = &exit
			}
			checkpoint := Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{process}}
			if err := integrityAppend(log, checkpoint).Validate(); err != nil {
				t.Fatal(err)
			}
			checkpoint.Processes[0].State = controlValue(ParseExecutionState("fixture", started.Input.JSON()))
			if err := integrityAppend(log, checkpoint).Validate(); !errors.Is(err, ErrInvalidLog) {
				t.Fatalf("terminal checkpoint retained mutable execution state: %v", err)
			}
		})
	}
}

func TestRecordBoundaryRejectsPointersAndForeignVariants(t *testing.T) {
	started := integrityInitialLog(t).Entries[0].Record.(Started)
	for _, record := range []Record{nil, (*Started)(nil), &started, (*Activated)(nil), &Activated{Epoch: 1}, foreignIntegrityRecord{}} {
		if _, _, err := EncodeRecord(record); !errors.Is(err, ErrInvalidLog) {
			t.Fatalf("record %T accepted: %v", record, err)
		}
	}
}

func TestLogWireRejectsInvalidCoordinatesAndVariants(t *testing.T) {
	for _, wire := range []struct{ kind, body string }{
		{"unknown", `{}`},
		{string(RecordKindActivated), `{}`},
		{string(RecordKindActivated), `{"epoch":0}`},
		{string(RecordKindActivated), `{"epoch":1,"epoch":2}`},
		{string(RecordKindActivated), `{"epoch":1,"other":true}`},
		{string(RecordKindSignalAdmitted), `null`},
	} {
		if _, err := DecodeRecord(wire.kind, []byte(wire.body)); !errors.Is(err, ErrInvalidLog) {
			t.Fatalf("decoded invalid %s %s: %v", wire.kind, wire.body, err)
		}
	}
	log := integrityInitialLog(t)
	batch := Batch{Root: log.Root, Records: []Record{log.Entries[0].Record, log.Entries[1].Record}}
	for _, change := range []func(*batchWire){
		func(w *batchWire) { w.Entries[0].LSN++ },
		func(w *batchWire) { w.Entries[1].Epoch++ },
		func(w *batchWire) { w.After++ },
		func(w *batchWire) { w.Entries = nil },
	} {
		wire := batchWire{Root: batch.Root, Entries: batch.Entries()}
		change(&wire)
		data := controlValue(jsonv2.Marshal(wire))
		var decoded Batch
		if err := jsonv2.Unmarshal(data, &decoded); err == nil {
			t.Fatalf("decoded invalid batch: %s", data)
		}
	}
	var entry Entry
	if err := jsonv2.Unmarshal([]byte(`{"lsn":0,"epoch":1,"kind":"activated","body":{"epoch":1}}`), &entry); err == nil {
		t.Fatal("decoded zero LSN")
	}
	if err := (*Entry)(nil).UnmarshalJSON(nil); err == nil {
		t.Fatal("nil Entry receiver accepted JSON")
	}
	if err := (*Batch)(nil).UnmarshalJSON(nil); err == nil {
		t.Fatal("nil Batch receiver accepted JSON")
	}
}

type foreignIntegrityRecord struct{}

func (foreignIntegrityRecord) recordKind() RecordKind { return RecordKindStarted }
func (foreignIntegrityRecord) valid() bool            { return true }

type integrityStore struct {
	log     Log
	appends int
}

func (i *integrityStore) Load(context.Context, ProcessID) (Log, bool, error) {
	entries, err := cloneEntries(i.log.Entries)
	return Log{Root: i.log.Root, Entries: entries}, true, err
}
func (i *integrityStore) Append(context.Context, Batch) error {
	i.appends++
	return errors.New("unexpected append while validating a corrupt log")
}

func integrityRuntime(t *testing.T, store Store) (*Runtime, Program[string, string]) {
	t.Helper()
	runtime := controlValue(New(t.Context(), Config{Store: store, Build: "run.log-integrity"}))
	t.Cleanup(func() {
		if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	program := controlValue(Bind[string, string](runtime.Compiler(), BindingConfig{Definition: newTypedFixtureDefinition[string](t, "test.log-integrity"), Revision: "one"}))
	return runtime, program
}

func integrityInitialLog(t *testing.T) Log {
	t.Helper()
	_, program := integrityRuntime(t, NewMemoryStore())
	root := controlValue(ParseProcessID("log-integrity"))
	started := Started{Process: root, Program: program.ProgramRef(), Input: controlValue(EncodePayload("input")), Limits: DefaultTreeLimits(), At: time.Unix(1, 0).UTC()}
	batch := Batch{Root: root, Records: []Record{started, Activated{Epoch: 1}}}
	return Log{Root: root, Entries: batch.Entries()}
}

func integrityAppend(log Log, records ...Record) Log {
	batch := Batch{Root: log.Root, Epoch: log.Epoch(), After: log.Head(), Records: records}
	entries := append([]Entry(nil), log.Entries...)
	entries = append(entries, batch.Entries()...)
	return Log{Root: log.Root, Entries: entries}
}
