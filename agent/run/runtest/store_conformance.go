package runtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

const conformanceBuild = "runtest.log"

// RunStoreConformance uses a fresh store for each case so another case cannot
// accidentally supply the head or fencing evidence the implementation omits.
func RunStoreConformance(t *testing.T, factory func(*testing.T) run.Store) {
	t.Helper()
	if factory == nil {
		t.Fatal("runtest: store factory is nil")
	}
	for _, check := range []struct {
		name string
		run  func(*testing.T, run.Store, run.Batch) error
	}{
		{"append and load preserve records", checkStoreRoundTrip},
		{"lost acknowledgments are idempotent", checkStoreRetry},
		{"conflicting positions are rejected", checkStoreConflict},
		{"activation fences previous writers", checkStoreFencing},
		{"one concurrent append wins", checkStoreConcurrentAppend},
		{"canceled calls do not mutate", checkStoreCancellation},
		{"invalid batches are rejected atomically", checkInvalidStoreBatch},
		{"caller mutation cannot rewrite history", checkStoreOwnership},
		{"load starts at the newest checkpoint", checkStoreRecoveryCut},
		{"read yields history after a position", checkStoreHistoryRead},
		{"positions order numerically past single digits", checkStoreNumericOrder},
	} {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(t, factory(t), storeInitialBatch(t)); err != nil {
				t.Fatal(err)
			}
		})
	}
	runRecoveryConformance(t, factory)
	runChildConformance(t, factory)
	runRuntimeConformance(t, factory)
}

func checkStoreRoundTrip(t *testing.T, store run.Store, batch run.Batch) error {
	if _, found, err := store.Load(t.Context(), batch.Root); err != nil || found {
		return errors.Join(fmt.Errorf("absent log: found=%t", found), err)
	}
	if err := store.Append(t.Context(), batch); err != nil {
		return fmt.Errorf("append initial records: %w", err)
	}
	log, err := readStoreLog(t.Context(), store, batch.Root)
	if err != nil {
		return err
	}
	if err := compareStoreEntries(log.Entries, batch.Entries()); err != nil {
		return err
	}
	if log.Head() != 2 || log.Epoch() != 1 {
		return fmt.Errorf("initial head=%d epoch=%d", log.Head(), log.Epoch())
	}
	return nil
}

func checkStoreRetry(t *testing.T, store run.Store, batch run.Batch) error {
	first := storeSignalBatch(t, batch.Root, 1, 2, "first", "accepted")
	second := storeSignalBatch(t, batch.Root, 1, 3, "second", "later")
	for _, attempt := range []run.Batch{batch, batch, first, second, first} {
		if err := store.Append(t.Context(), attempt); err != nil {
			return fmt.Errorf("idempotent append after %d: %w", attempt.After, err)
		}
	}
	log, err := readStoreLog(t.Context(), store, batch.Root)
	if err != nil {
		return err
	}
	if len(log.Entries) != 4 {
		return fmt.Errorf("retry appended records: %d", len(log.Entries))
	}
	if compareErr := compareStoreEntries(log.Entries[2:3], first.Entries()); compareErr != nil {
		return compareErr
	}
	activation := run.Batch{Root: batch.Root, Epoch: 1, After: 4, Records: []run.Record{run.Activated{Epoch: 2}}}
	for range 2 {
		if appendErr := store.Append(t.Context(), activation); appendErr != nil {
			return fmt.Errorf("idempotent activation: %w", appendErr)
		}
	}
	log, err = readStoreLog(t.Context(), store, batch.Root)
	if err != nil {
		return err
	}
	if log.Head() != 5 || log.Epoch() != 2 {
		return fmt.Errorf("activation retry head=%d epoch=%d", log.Head(), log.Epoch())
	}
	return nil
}

func checkStoreConflict(t *testing.T, store run.Store, batch run.Batch) error {
	first := storeSignalBatch(t, batch.Root, 1, 2, "one", "accepted")
	for _, accepted := range []run.Batch{batch, first} {
		if err := store.Append(t.Context(), accepted); err != nil {
			return fmt.Errorf("append accepted records: %w", err)
		}
	}
	conflict := storeSignalBatch(t, batch.Root, 1, 2, "one", "different")
	if err := store.Append(t.Context(), conflict); !errors.Is(err, run.ErrCommitConflict) {
		return errors.Join(errors.New("occupied position did not return a commit conflict"), err)
	}
	gap := storeSignalBatch(t, batch.Root, 1, 4, "two", "gap")
	if err := store.Append(t.Context(), gap); !errors.Is(err, run.ErrCommitConflict) {
		return errors.Join(errors.New("log gap did not return a commit conflict"), err)
	}
	log, err := readStoreLog(t.Context(), store, batch.Root)
	if err != nil {
		return err
	}
	if log.Head() != 3 {
		return fmt.Errorf("rejected append changed head to %d", log.Head())
	}
	return compareStoreEntries(log.Entries[2:], first.Entries())
}

func checkStoreFencing(t *testing.T, store run.Store, batch run.Batch) error {
	first := storeSignalBatch(t, batch.Root, 1, 2, "one", "accepted")
	activation := run.Batch{Root: batch.Root, Epoch: 1, After: 3, Records: []run.Record{run.Activated{Epoch: 2}}}
	for _, accepted := range []run.Batch{batch, first, activation} {
		if err := store.Append(t.Context(), accepted); err != nil {
			return fmt.Errorf("append before fencing: %w", err)
		}
	}
	for _, stale := range []run.Batch{batch, first, storeSignalBatch(t, batch.Root, 1, 4, "two", "stale")} {
		if err := store.Append(t.Context(), stale); !errors.Is(err, run.ErrEpochConflict) {
			return errors.Join(errors.New("fenced writer did not return an epoch conflict"), err)
		}
	}
	next := storeSignalBatch(t, batch.Root, 2, 4, "two", "current")
	if err := store.Append(t.Context(), next); err != nil {
		return fmt.Errorf("current writer append: %w", err)
	}
	log, err := readStoreLog(t.Context(), store, batch.Root)
	if err != nil {
		return err
	}
	if log.Head() != 5 || log.Epoch() != 2 {
		return fmt.Errorf("current writer head=%d epoch=%d", log.Head(), log.Epoch())
	}
	return nil
}

func checkStoreConcurrentAppend(t *testing.T, store run.Store, batch run.Batch) error {
	if err := store.Append(t.Context(), batch); err != nil {
		return fmt.Errorf("append initial records: %w", err)
	}
	attempts := []run.Batch{
		storeSignalBatch(t, batch.Root, 1, 2, "left", "left"),
		storeSignalBatch(t, batch.Root, 1, 2, "right", "right"),
	}
	start := make(chan struct{})
	var group sync.WaitGroup
	failures := make([]error, len(attempts))
	for index, attempt := range attempts {
		group.Go(func() {
			<-start
			failures[index] = store.Append(t.Context(), attempt)
		})
	}
	close(start)
	group.Wait()
	winner := -1
	for index, err := range failures {
		if err == nil {
			if winner != -1 {
				return errors.New("both conflicting appends were acknowledged")
			}
			winner = index
		} else if !errors.Is(err, run.ErrCommitConflict) {
			return fmt.Errorf("losing writer did not return a commit conflict: %w", err)
		}
	}
	if winner == -1 {
		return fmt.Errorf("neither append succeeded: %v", failures)
	}
	log, err := readStoreLog(t.Context(), store, batch.Root)
	if err != nil {
		return err
	}
	return compareStoreEntries(log.Entries[2:], attempts[winner].Entries())
}

func checkStoreCancellation(t *testing.T, store run.Store, batch run.Batch) error {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.Append(ctx, batch); !errors.Is(err, context.Canceled) {
		return errors.Join(errors.New("canceled append did not return context cancellation"), err)
	}
	if _, found, err := store.Load(t.Context(), batch.Root); err != nil || found {
		return errors.Join(fmt.Errorf("canceled append changed storage: found=%t", found), err)
	}
	if _, _, err := store.Load(ctx, batch.Root); !errors.Is(err, context.Canceled) {
		return errors.Join(errors.New("canceled load did not return context cancellation"), err)
	}
	return nil
}

func checkInvalidStoreBatch(t *testing.T, store run.Store, initial run.Batch) error {
	if err := store.Append(t.Context(), initial); err != nil {
		return fmt.Errorf("append initial records: %w", err)
	}
	var absent *run.Started
	invalid := storeSignalBatch(t, initial.Root, 1, 2, "invalid", "not committed")
	invalid.Records = append(invalid.Records, absent)
	if err := store.Append(t.Context(), invalid); !errors.Is(err, run.ErrInvalidLog) {
		return errors.Join(errors.New("typed nil record did not reject the batch"), err)
	}
	mixed := storeSignalBatch(t, initial.Root, 1, 2, "mixed", "not committed")
	mixed.Records = append(mixed.Records, run.Activated{Epoch: 2})
	if err := store.Append(t.Context(), mixed); !errors.Is(err, run.ErrInvalidLog) {
		return errors.Join(errors.New("activation mixed with ordinary records was not rejected"), err)
	}
	log, err := readStoreLog(t.Context(), store, initial.Root)
	if err != nil {
		return err
	}
	if log.Head() != 2 {
		return errors.New("invalid batch partially appended")
	}
	absentRoot := (run.StartID{Scope: "runtest", Key: "missing-start"}).Root()
	missingStart := storeSignalBatch(t, absentRoot, 1, 2, "missing", "not committed")
	if err := store.Append(t.Context(), missingStart); !errors.Is(err, run.ErrEpochConflict) {
		return errors.Join(errors.New("append to an absent tree did not return an epoch conflict"), err)
	}
	if _, found, err := store.Load(t.Context(), absentRoot); err != nil || found {
		return errors.Join(fmt.Errorf("rejected creation retained an empty log: found=%t", found), err)
	}
	if _, _, err := store.Load(t.Context(), run.ProcessID{}); !errors.Is(err, run.ErrInvalidIdentity) {
		return errors.Join(errors.New("invalid root did not return an identity error"), err)
	}
	return nil
}

func checkStoreOwnership(t *testing.T, store run.Store, batch run.Batch) error {
	if err := store.Append(t.Context(), batch); err != nil {
		return fmt.Errorf("append initial records: %w", err)
	}
	want := batch.Entries()
	batch.Records[0] = run.Activated{Epoch: 999}
	first, err := readStoreLog(t.Context(), store, batch.Root)
	if err != nil {
		return err
	}
	if compareErr := compareStoreEntries(first.Entries, want); compareErr != nil {
		return compareErr
	}
	first.Entries[0].Record = run.Activated{Epoch: 998}
	second, err := readStoreLog(t.Context(), store, batch.Root)
	if err != nil {
		return err
	}
	return compareStoreEntries(second.Entries, want)
}

func storeInitialBatch(t *testing.T) run.Batch {
	t.Helper()
	runtime, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore(), Build: conformanceBuild})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := runtime.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	definition := newStoreDefinition(t)
	program, err := run.Bind[string, string](runtime.Compiler(), run.BindingConfig{Definition: definition, Revision: "one"})
	if err != nil {
		t.Fatal(err)
	}
	root := (run.StartID{Scope: "runtest", Key: "store"}).Root()
	input, err := run.EncodePayload("input")
	if err != nil {
		t.Fatal(err)
	}
	return run.Batch{Root: root, Records: []run.Record{
		run.Started{Process: root, Program: program.ProgramRef(), Input: input, Limits: run.DefaultTreeLimits(), At: time.Unix(0, 0).UTC()},
		run.Activated{Epoch: 1},
	}}
}

func checkStoreRecoveryCut(t *testing.T, store run.Store, batch run.Batch) error {
	checkpoint := storeCheckpoint(t, batch)
	sequence := []run.Batch{
		batch,
		storeSignalBatch(t, batch.Root, 1, 2, "before", "accepted"),
		{Root: batch.Root, Epoch: 1, After: 3, Records: []run.Record{checkpoint}},
		storeSignalBatch(t, batch.Root, 1, 4, "between", "accepted"),
		{Root: batch.Root, Epoch: 1, After: 5, Records: []run.Record{checkpoint}},
		storeSignalBatch(t, batch.Root, 1, 6, "after", "accepted"),
	}
	for _, appended := range sequence {
		if err := store.Append(t.Context(), appended); err != nil {
			return fmt.Errorf("append at %d: %w", appended.After, err)
		}
	}
	log, err := readStoreLog(t.Context(), store, batch.Root)
	if err != nil {
		return err
	}
	want := append(sequence[4].Entries(), sequence[5].Entries()...)
	if err := compareStoreEntries(log.Entries, want); err != nil {
		return fmt.Errorf("load is not the newest checkpoint and its suffix: %w", err)
	}
	return nil
}

func checkStoreHistoryRead(t *testing.T, store run.Store, batch run.Batch) error {
	sequence := []run.Batch{batch, storeSignalBatch(t, batch.Root, 1, 2, "first", "accepted"), storeSignalBatch(t, batch.Root, 1, 3, "second", "accepted")}
	var history []run.Entry
	for _, appended := range sequence {
		if err := store.Append(t.Context(), appended); err != nil {
			return fmt.Errorf("append at %d: %w", appended.After, err)
		}
		history = append(history, appended.Entries()...)
	}
	for _, after := range []run.LSN{0, 2, 4} {
		got, err := readStoreHistory(t.Context(), store, batch.Root, after)
		if err != nil {
			return err
		}
		if err := compareStoreEntries(got, history[after:]); err != nil {
			return fmt.Errorf("read after %d: %w", after, err)
		}
	}
	absent, err := readStoreHistory(t.Context(), store, (run.StartID{Scope: "runtest", Key: "absent"}).Root(), 0)
	if err != nil || len(absent) != 0 {
		return errors.Join(fmt.Errorf("absent tree yielded %d entries", len(absent)), err)
	}
	return nil
}

// checkStoreNumericOrder crosses the 9→10 boundary inside one batch, where a
// store that orders positions as text would read, retry, and find its head
// out of order.
func checkStoreNumericOrder(t *testing.T, store run.Store, batch run.Batch) error {
	history := batch.Entries()
	if err := store.Append(t.Context(), batch); err != nil {
		return fmt.Errorf("append initial records: %w", err)
	}
	for after := run.LSN(2); after < 8; after++ {
		single := storeSignalBatch(t, batch.Root, 1, after, fmt.Sprintf("single-%d", after), "accepted")
		if err := store.Append(t.Context(), single); err != nil {
			return fmt.Errorf("append at %d: %w", after, err)
		}
		history = append(history, single.Entries()...)
	}
	crossing := run.Batch{Root: batch.Root, Epoch: 1, After: 8}
	for index := range 4 {
		signal := storeSignalBatch(t, batch.Root, 1, 8, fmt.Sprintf("crossing-%d", index), "accepted")
		crossing.Records = append(crossing.Records, signal.Records...)
	}
	for range 2 {
		if err := store.Append(t.Context(), crossing); err != nil {
			return fmt.Errorf("append batch crossing position 10: %w", err)
		}
	}
	history = append(history, crossing.Entries()...)
	last := storeSignalBatch(t, batch.Root, 1, 12, "after-crossing", "accepted")
	if err := store.Append(t.Context(), last); err != nil {
		return fmt.Errorf("append after the crossing batch: %w", err)
	}
	history = append(history, last.Entries()...)
	for _, after := range []run.LSN{0, 9} {
		got, err := readStoreHistory(t.Context(), store, batch.Root, after)
		if err != nil {
			return err
		}
		if err := compareStoreEntries(got, history[after:]); err != nil {
			return fmt.Errorf("read after %d: %w", after, err)
		}
	}
	log, err := readStoreLog(t.Context(), store, batch.Root)
	if err != nil {
		return err
	}
	return compareStoreEntries(log.Entries, history)
}

func readStoreHistory(ctx context.Context, store run.Store, root run.ProcessID, after run.LSN) ([]run.Entry, error) {
	var entries []run.Entry
	for entry, err := range store.Read(ctx, root, after) {
		if err != nil {
			return nil, fmt.Errorf("read history after %d: %w", after, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// storeCheckpoint materializes only the root's Start; a Store treats the
// record as opaque, so its fidelity to the prefix is not under test here.
func storeCheckpoint(t *testing.T, batch run.Batch) run.Checkpoint {
	t.Helper()
	state, err := run.EncodeExecutionState("runtest.store", "input")
	if err != nil {
		t.Fatal(err)
	}
	return run.Checkpoint{StartedProcesses: 1, Processes: []run.ProcessCheckpoint{{Started: batch.Records[0].(run.Started), State: state}}}
}

func storeSignalBatch(t *testing.T, root run.ProcessID, epoch run.Epoch, after run.LSN, id, value string) run.Batch {
	t.Helper()
	payload, err := run.EncodePayload(value)
	if err != nil {
		t.Fatal(err)
	}
	signalID, err := run.ParseSignalID(id)
	if err != nil {
		t.Fatal(err)
	}
	return run.Batch{Root: root, Epoch: epoch, After: after, Records: []run.Record{
		run.SignalAdmitted{Process: root, Signal: run.Signal{ID: signalID, Payload: payload}, At: time.Unix(0, 0).UTC()},
	}}
}

func appendStoreBatch(t *testing.T, store run.Store, batch run.Batch) {
	t.Helper()
	if err := store.Append(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
}

func loadStoreLog(t *testing.T, store run.Store, root run.ProcessID) run.Log {
	t.Helper()
	log, err := readStoreLog(t.Context(), store, root)
	if err != nil {
		t.Fatal(err)
	}
	return log
}

func readStoreLog(ctx context.Context, store run.Store, root run.ProcessID) (run.Log, error) {
	log, found, err := store.Load(ctx, root)
	if err != nil {
		return run.Log{}, fmt.Errorf("load committed log: %w", err)
	}
	if !found {
		return run.Log{}, errors.New("committed log is missing")
	}
	if log.Root != root {
		return run.Log{}, errors.New("loaded log belongs to another root")
	}
	if err := log.Validate(); err != nil {
		return run.Log{}, fmt.Errorf("loaded log is invalid: %w", err)
	}
	return log, nil
}

func assertStoreEntries(t *testing.T, got, want []run.Entry) {
	t.Helper()
	if err := compareStoreEntries(got, want); err != nil {
		t.Fatal(err)
	}
}

func compareStoreEntries(got, want []run.Entry) error {
	if len(got) != len(want) {
		return fmt.Errorf("records=%d, want %d", len(got), len(want))
	}
	for index := range want {
		gotKind, gotBody, gotErr := run.EncodeRecord(got[index].Record)
		wantKind, wantBody, wantErr := run.EncodeRecord(want[index].Record)
		if gotErr != nil || wantErr != nil {
			return fmt.Errorf("record %d encode: %w", index, errors.Join(gotErr, wantErr))
		}
		if got[index].LSN != want[index].LSN || got[index].Epoch != want[index].Epoch || gotKind != wantKind || !bytes.Equal(gotBody, wantBody) {
			return fmt.Errorf("record %d changed: got=%+v %s want=%+v %s", index, got[index], gotBody, want[index], wantBody)
		}
	}
	return nil
}

type storeDefinition struct{ descriptor run.Descriptor }

func newStoreDefinition(t *testing.T) *storeDefinition {
	t.Helper()
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: "runtest.store", Description: "Exercise durable log ownership.", InputSchema: schema, OutputSchema: schema, SignalSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	return &storeDefinition{descriptor: descriptor}
}

func (s *storeDefinition) Descriptor() run.Descriptor                             { return s.descriptor }
func (s *storeDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (s *storeDefinition) Start(input run.Payload) (run.Execution, error) {
	return &storeExecution{input: input}, nil
}

func (s *storeDefinition) Restore(_ context.Context, state run.ExecutionState) (run.Execution, error) {
	value, err := state.Decode[string]("runtest.store")
	if err != nil {
		return nil, err
	}
	input, err := run.EncodePayload(value)
	return &storeExecution{input: input}, err
}

type storeExecution struct{ input run.Payload }

func (s *storeExecution) Step(_ context.Context, turn run.Turn) (run.Transition, error) {
	return run.Complete(uint32(len(turn.Messages)), s.input)
}

func (s *storeExecution) Snapshot() (run.ExecutionState, error) {
	return run.ParseExecutionState("runtest.store", s.input.JSON())
}
