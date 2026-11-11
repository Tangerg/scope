package run

import (
	"errors"
	"slices"
	"testing"
)

func TestEntryValidationPreservesTheZeroEpochRootStart(t *testing.T) {
	log := integrityInitialLog(t)
	if log.Entries[0].Epoch != 0 {
		t.Fatal("fixture lost the root start's pre-activation epoch")
	}
	for _, entry := range log.Entries {
		if err := entry.ValidateFor(log.Root); err != nil {
			t.Fatalf("valid entry rejected: position=%d epoch=%d error=%v", entry.LSN, entry.Epoch, err)
		}
	}
	foreign := controlValue(ParseProcessID("foreign-tree"))
	child := controlValue(log.Root.Child(controlValue(ParseChildKey("child"))))
	for _, test := range []struct {
		name  string
		root  ProcessID
		entry Entry
	}{
		{"missing root", ProcessID{}, log.Entries[0]},
		{"non-root owner", child, log.Entries[0]},
		{"foreign root", foreign, log.Entries[0]},
		{"zero position", log.Root, Entry{Epoch: 1, Record: Activated{Epoch: 1}}},
		{"missing record", log.Root, Entry{LSN: 1}},
		{"invalid record", log.Root, Entry{LSN: 1, Record: Stepped{}}},
		{"pointer record", log.Root, Entry{LSN: 1, Record: new(Activated{Epoch: 1})}},
		{"foreign variant", log.Root, Entry{LSN: 1, Record: foreignIntegrityRecord{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.entry.ValidateFor(test.root); !errors.Is(err, ErrInvalidLog) {
				t.Fatalf("invalid entry acquired membership: %v", err)
			}
		})
	}
}

func TestCheckpointMembershipHasOneEntryBoundary(t *testing.T) {
	log := integrityInitialLog(t)
	started := log.Entries[0].Record.(Started)
	state := controlValue(NewExecutionState("fixture", started.Input))
	checkpoint := Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{{Started: started, State: state}}}
	entry := Entry{LSN: log.Head() + 1, Epoch: log.Epoch(), Record: checkpoint}
	if err := entry.ValidateFor(log.Root); err != nil {
		t.Fatalf("valid checkpoint rejected: %v", err)
	}
	if checkpoint.root() != log.Root || (Checkpoint{}).root().Valid() {
		t.Fatal("checkpoint membership was not derived from its process facts")
	}
	foreignStarted := started
	foreignStarted.Process = controlValue(ParseProcessID("foreign-tree"))
	foreign := Checkpoint{StartedProcesses: 1, Processes: []ProcessCheckpoint{{Started: foreignStarted, State: state}}}
	if _, _, err := EncodeRecord(foreign); err != nil {
		t.Fatal("foreign checkpoint is not structurally valid", err)
	}
	mixed := Checkpoint{StartedProcesses: 2, Processes: append(slices.Clone(checkpoint.Processes), foreign.Processes...)}
	slices.SortFunc(mixed.Processes, func(left, right ProcessCheckpoint) int { return left.Started.Process.compare(right.Started.Process) })
	for _, test := range []struct {
		name       string
		checkpoint Checkpoint
	}{
		{"foreign tree", foreign},
		{"mixed trees", mixed},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := entry
			candidate.Record = test.checkpoint
			if err := candidate.ValidateFor(log.Root); !errors.Is(err, ErrInvalidLog) {
				t.Fatal("entry admitted foreign process facts", err)
			}
			batch := Batch{Root: log.Root, Epoch: log.Epoch(), After: log.Head(), Records: []Record{test.checkpoint}}
			if err := batch.Validate(); !errors.Is(err, ErrInvalidLog) {
				t.Fatal("batch admitted foreign process facts", err)
			}
			if err := integrityAppend(log, test.checkpoint).Validate(); !errors.Is(err, ErrInvalidLog) {
				t.Fatal("complete history admitted foreign process facts", err)
			}
			store := &integrityStore{log: Log{Root: log.Root, Entries: []Entry{candidate}}}
			runtime, _ := integrityRuntime(t, store)
			visited := false
			err := runtime.scan(t.Context(), log.Root, log.Head(), func(Entry) bool { visited = true; return true })
			if !errors.Is(err, ErrInvalidLog) || visited {
				t.Fatal("runtime observation projected foreign process facts", err)
			}
		})
	}
}
