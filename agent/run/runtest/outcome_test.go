package runtest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/run"
)

type outcomeStore struct {
	store       run.Store
	readyKind   run.RecordKind
	ready       chan run.ProcessID
	once        sync.Once
	cancelError error
}

func (o *outcomeStore) Load(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
	return o.store.Load(ctx, root)
}
func (o *outcomeStore) Append(ctx context.Context, batch run.Batch) error {
	for _, record := range batch.Records {
		if _, control := record.(run.ControlRequested); control && o.cancelError != nil {
			return o.cancelError
		}
	}
	if err := o.store.Append(ctx, batch); err != nil {
		return err
	}
	for _, record := range batch.Records {
		kind, _, err := run.EncodeRecord(record)
		if err != nil {
			return err
		}
		if kind == string(o.readyKind) {
			o.once.Do(func() { o.ready <- batch.Root })
		}
	}
	return nil
}

func TestObserveReturnsCommittedResultsAndStartFailures(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		name := "completed"
		if unavailable {
			name = "start unavailable"
		}
		t.Run(name, func(t *testing.T) {
			cause := errors.New("initial append unavailable")
			store := run.NewMemoryStore()
			probe := &storeProbe{append: store.Append, load: store.Load}
			if unavailable {
				probe.append = func(context.Context, run.Batch) error { return cause }
			}
			runtime, program := recoveryRuntime(t, probe, newRecoveryDefinition(t, recoveryComplete), nil)
			result, err := Observe(t.Context(), runtime, program, conformanceInput(t))
			if unavailable {
				if result.Valid() || !errors.Is(err, cause) {
					t.Fatalf("uncommitted start manufactured a result or hid its cause: %+v %v", result, err)
				}
				return
			}
			if err != nil || result.Termination().Status() != run.StatusCompleted || result.Usage().CommittedSteps != 1 {
				t.Fatalf("committed result was not observed: %+v %v", result, err)
			}
			output, err := result.Output()
			if err != nil || string(output.JSON()) != string(conformanceInput(t).JSON()) {
				t.Fatalf("observation changed output: %s %v", output.JSON(), err)
			}
		})
	}
}

func TestObserveCancellationPreservesUnknownAndStorageFailures(t *testing.T) {
	storeFailure := errors.New("cancellation storage unavailable")
	for _, test := range []struct {
		name       string
		mode       recoveryMode
		ready      run.RecordKind
		storeError error
		want       error
	}{
		{"unknown external effect", recoveryEffect, run.RecordKindEffectSettled, nil, run.ErrEffectOutcomeUnknown},
		{"cancellation write failure", recoveryWait, run.RecordKindStepped, storeFailure, storeFailure},
		{"known local cancellation", recoveryWait, run.RecordKindStepped, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &outcomeStore{store: run.NewMemoryStore(), readyKind: test.ready, ready: make(chan run.ProcessID, 1), cancelError: test.storeError}
			dispatcher, err := NewScriptedDispatcher(ScriptedDispatcherConfig{ReplayPolicy: run.ReplayPolicyNever, Calls: []ScriptedCall{{SettlementStatus: run.SettlementStatusUnknown, SettlementPayload: []byte(`null`)}}})
			if err != nil {
				t.Fatal(err)
			}
			runtime, program := recoveryRuntime(t, store, newRecoveryDefinition(t, test.mode), dispatcher)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type observation struct {
				result run.Result[run.Payload]
				err    error
			}
			finished := make(chan observation, 1)
			go func() {
				result, observeErr := Observe(ctx, runtime, program, conformanceInput(t))
				finished <- observation{result, observeErr}
			}()
			guard, stop := context.WithTimeout(t.Context(), conformanceTimeout)
			defer stop()
			var root run.ProcessID
			select {
			case root = <-store.ready:
			case <-guard.Done():
				t.Fatal(guard.Err())
			}
			cancel()
			const cancellationResponseLimit = time.Second
			response, cancelResponse := context.WithTimeout(guard, cancellationResponseLimit)
			defer cancelResponse()
			select {
			case observed := <-finished:
				if test.want == nil {
					if observed.err != nil || observed.result.Termination().Status() != run.StatusCanceled {
						t.Fatalf("known cancellation did not drain: %+v %v", observed.result, observed.err)
					}
				} else if !errors.Is(observed.err, test.want) || !errors.Is(observed.err, context.Canceled) || observed.result.Valid() {
					t.Fatalf("cancellation obscured pending work or its store failure: %+v %v", observed.result, observed.err)
				}
			case <-response.Done():
				t.Fatal("Observe ignored cancellation while waiting for an outcome that cannot become terminal")
			}
			if errors.Is(test.want, run.ErrEffectOutcomeUnknown) {
				log := loadStoreLog(t, store, root)
				for _, entry := range log.Entries {
					if _, exited := entry.Record.(run.Exited); exited {
						t.Fatal("Observe manufactured an Exit for unresolved external work")
					}
				}
			}
		})
	}
}
