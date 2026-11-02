package runtest

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

type storeProbe struct {
	append func(context.Context, run.Batch) error
	load   func(context.Context, run.ProcessID) (run.Log, bool, error)
	read   func(context.Context, run.ProcessID, run.LSN) iter.Seq2[run.Entry, error]
}

func (s *storeProbe) Append(ctx context.Context, batch run.Batch) error {
	return s.append(ctx, batch)
}
func (s *storeProbe) Load(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
	return s.load(ctx, root)
}
func (s *storeProbe) Read(ctx context.Context, root run.ProcessID, after run.LSN) iter.Seq2[run.Entry, error] {
	return s.read(ctx, root, after)
}

func TestStoreConformanceRejectsBrokenDurabilityGuarantees(t *testing.T) {
	cause := errors.New("store probe failed")
	for _, test := range []struct {
		name      string
		check     func(*testing.T, run.Store, run.Batch) error
		configure func(*storeProbe, run.Store)
		want      string
	}{
		{"unacknowledged append", checkStoreRoundTrip, func(probe *storeProbe, _ run.Store) {
			probe.append = func(context.Context, run.Batch) error { return cause }
		}, "append initial records"},
		{"acknowledged records missing", checkStoreRoundTrip, func(probe *storeProbe, _ run.Store) {
			probe.append = func(context.Context, run.Batch) error { return nil }
		}, "committed log is missing"},
		{"load substitutes root", checkStoreRoundTrip, func(probe *storeProbe, store run.Store) {
			probe.load = func(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
				log, found, err := store.Load(ctx, root)
				if found {
					log.Root = (run.StartID{Scope: "other", Key: "other"}).Root()
				}
				return log, found, err
			}
		}, "another root"},
		{"load corrupts entry epoch", checkStoreRoundTrip, func(probe *storeProbe, store run.Store) {
			probe.load = func(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
				log, found, err := store.Load(ctx, root)
				if found {
					log.Entries[0].Epoch++
				}
				return log, found, err
			}
		}, "loaded log is invalid"},
		{"load changes accepted input", checkStoreRoundTrip, func(probe *storeProbe, store run.Store) {
			probe.load = func(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
				log, found, err := store.Load(ctx, root)
				if found {
					started := log.Entries[0].Record.(run.Started)
					started.Input, err = run.EncodePayload("substituted")
					log.Entries[0].Record = started
				}
				return log, found, err
			}
		}, "record 0 changed"},
		{"initial retry rejected", checkStoreRetry, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				if _, found, err := store.Load(ctx, batch.Root); err != nil {
					return err
				} else if found && batch.After == 0 {
					return run.ErrCommitConflict
				}
				return store.Append(ctx, batch)
			}
		}, "idempotent append after 0"},
		{"historical retry rejected", checkStoreRetry, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				log, found, err := store.Load(ctx, batch.Root)
				if err != nil {
					return err
				}
				if found && batch.Epoch > 0 && batch.After < log.Head() {
					return run.ErrCommitConflict
				}
				return store.Append(ctx, batch)
			}
		}, "idempotent append after 2"},
		{"activation acknowledgment cannot retry", checkStoreRetry, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				log, found, err := store.Load(ctx, batch.Root)
				if err != nil {
					return err
				}
				if _, activation := batch.Records[0].(run.Activated); activation && found && log.Head() > batch.After {
					return run.ErrEpochConflict
				}
				return store.Append(ctx, batch)
			}
		}, "idempotent activation"},
		{"activation is acknowledged without persistence", checkStoreRetry, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				if _, activation := batch.Records[0].(run.Activated); activation {
					return nil
				}
				return store.Append(ctx, batch)
			}
		}, "activation retry head"},
		{"position conflict falsely acknowledged", checkStoreConflict, acknowledgeConflict, "occupied position"},
		{"gap falsely acknowledged", checkStoreConflict, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				log, found, err := store.Load(ctx, batch.Root)
				if err != nil {
					return err
				}
				if found && batch.After > log.Head() {
					return nil
				}
				return store.Append(ctx, batch)
			}
		}, "log gap"},
		{"rejected gap still commits", checkStoreConflict, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				err := store.Append(ctx, batch)
				log, found, loadErr := store.Load(ctx, batch.Root)
				if errors.Is(err, run.ErrCommitConflict) && loadErr == nil && found && batch.After > log.Head() {
					batch.After = log.Head()
					if appendErr := store.Append(ctx, batch); appendErr != nil {
						return appendErr
					}
				}
				return err
			}
		}, "rejected append changed head"},
		{"stale retry acknowledged", checkStoreFencing, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				err := store.Append(ctx, batch)
				if errors.Is(err, run.ErrEpochConflict) {
					return nil
				}
				return err
			}
		}, "fenced writer"},
		{"new writer is incorrectly fenced", checkStoreFencing, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				if batch.Epoch == 2 {
					return run.ErrEpochConflict
				}
				return store.Append(ctx, batch)
			}
		}, "current writer append"},
		{"new writer receives false acknowledgment", checkStoreFencing, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				if batch.Epoch == 2 {
					return nil
				}
				return store.Append(ctx, batch)
			}
		}, "current writer head"},
		{"concurrent writers both acknowledged", checkStoreConcurrentAppend, acknowledgeConflict, "both conflicting appends"},
		{"concurrent loser misclassified", checkStoreConcurrentAppend, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				err := store.Append(ctx, batch)
				if errors.Is(err, run.ErrCommitConflict) {
					return cause
				}
				return err
			}
		}, "losing writer"},
		{"all concurrent writes refused", checkStoreConcurrentAppend, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				if batch.Epoch > 0 {
					return run.ErrCommitConflict
				}
				return store.Append(ctx, batch)
			}
		}, "neither append succeeded"},
		{"canceled append acknowledged", checkStoreCancellation, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				return store.Append(context.WithoutCancel(ctx), batch)
			}
		}, "canceled append"},
		{"canceled append mutated storage", checkStoreCancellation, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				if err := store.Append(context.WithoutCancel(ctx), batch); err != nil {
					return err
				}
				return ctx.Err()
			}
		}, "canceled append changed storage"},
		{"canceled load ignored", checkStoreCancellation, func(probe *storeProbe, store run.Store) {
			probe.load = func(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
				return store.Load(context.WithoutCancel(ctx), root)
			}
		}, "canceled load"},
		{"malformed batch acknowledged", checkInvalidStoreBatch, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				if batch.Validate() != nil {
					return nil
				}
				return store.Append(ctx, batch)
			}
		}, "typed nil record"},
		{"malformed batch commits a prefix", checkInvalidStoreBatch, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				if _, typedNil := batch.Records[len(batch.Records)-1].(*run.Started); typedNil {
					batch.Records = batch.Records[:1]
					if err := store.Append(ctx, batch); err != nil {
						return err
					}
					return run.ErrInvalidLog
				}
				return store.Append(ctx, batch)
			}
		}, "invalid batch partially appended"},
		{"activation mixed with ordinary facts", checkInvalidStoreBatch, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				if _, activation := batch.Records[len(batch.Records)-1].(run.Activated); activation && batch.Epoch > 0 {
					return nil
				}
				return store.Append(ctx, batch)
			}
		}, "activation mixed"},
		{"append invents an absent tree", checkInvalidStoreBatch, func(probe *storeProbe, store run.Store) {
			probe.append = func(ctx context.Context, batch run.Batch) error {
				err := store.Append(ctx, batch)
				if errors.Is(err, run.ErrEpochConflict) {
					return nil
				}
				return err
			}
		}, "append to an absent tree"},
		{"rejected creation retains an empty head", checkInvalidStoreBatch, func(probe *storeProbe, store run.Store) {
			var leaked run.ProcessID
			probe.append = func(ctx context.Context, batch run.Batch) error {
				err := store.Append(ctx, batch)
				if errors.Is(err, run.ErrEpochConflict) {
					leaked = batch.Root
				}
				return err
			}
			probe.load = func(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
				if root == leaked {
					return run.Log{Root: root}, true, nil
				}
				return store.Load(ctx, root)
			}
		}, "rejected creation retained an empty log"},
		{"invalid root silently means absent", checkInvalidStoreBatch, func(probe *storeProbe, store run.Store) {
			probe.load = func(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
				if !root.Valid() {
					return run.Log{}, false, nil
				}
				return store.Load(ctx, root)
			}
		}, "invalid root did not return an identity error"},
		{"caller owns committed batch slice", checkStoreOwnership, func(probe *storeProbe, store run.Store) {
			var retained run.Batch
			probe.append = func(ctx context.Context, batch run.Batch) error {
				if err := store.Append(ctx, batch); err != nil {
					return err
				}
				retained = batch
				return nil
			}
			probe.load = func(context.Context, run.ProcessID) (run.Log, bool, error) {
				return run.Log{Root: retained.Root, Entries: retained.Entries()}, true, nil
			}
		}, "loaded log is invalid"},
		{"load exposes committed slice", checkStoreOwnership, func(probe *storeProbe, store run.Store) {
			var retained run.Log
			probe.load = func(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
				if !retained.Root.Valid() {
					log, found, err := store.Load(ctx, root)
					if err != nil || !found {
						return log, found, err
					}
					retained = log
				}
				return retained, true, nil
			}
		}, "loaded log is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := run.NewMemoryStore()
			probe := &storeProbe{append: store.Append, load: store.Load, read: store.Read}
			test.configure(probe, store)
			if err := test.check(t, probe, storeInitialBatch(t)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("conformance accepted or misidentified a broken guarantee: %v; want %q", err, test.want)
			}
		})
	}
}

func acknowledgeConflict(probe *storeProbe, store run.Store) {
	probe.append = func(ctx context.Context, batch run.Batch) error {
		err := store.Append(ctx, batch)
		if errors.Is(err, run.ErrCommitConflict) {
			return nil
		}
		return err
	}
}

func TestStoreConformanceRetainsUnexpectedStorageErrors(t *testing.T) {
	cause := errors.New("store unavailable")
	store := run.NewMemoryStore()
	batch := storeInitialBatch(t)
	appendStoreBatch(t, store, batch)
	probe := &storeProbe{append: store.Append, load: func(context.Context, run.ProcessID) (run.Log, bool, error) { return run.Log{}, false, cause }, read: store.Read}
	if _, err := readStoreLog(t.Context(), probe, batch.Root); !errors.Is(err, cause) {
		t.Fatalf("load diagnosis lost underlying error: %v", err)
	}
}

func TestEveryStoreCheckPreservesOperationalErrors(t *testing.T) {
	cause := errors.New("store operation unavailable")
	for _, check := range []struct {
		name string
		run  func(*testing.T, run.Store, run.Batch) error
	}{
		{"round trip", checkStoreRoundTrip},
		{"retry", checkStoreRetry},
		{"conflict", checkStoreConflict},
		{"fencing", checkStoreFencing},
		{"concurrency", checkStoreConcurrentAppend},
		{"cancellation", checkStoreCancellation},
		{"invalid batch", checkInvalidStoreBatch},
		{"ownership", checkStoreOwnership},
	} {
		t.Run(check.name, func(t *testing.T) {
			for _, operation := range []string{"append", "load"} {
				t.Run(operation, func(t *testing.T) {
					store := run.NewMemoryStore()
					probe := &storeProbe{append: store.Append, load: store.Load, read: store.Read}
					if operation == "append" {
						probe.append = func(context.Context, run.Batch) error { return cause }
					} else {
						probe.load = func(context.Context, run.ProcessID) (run.Log, bool, error) { return run.Log{}, false, cause }
					}
					if err := check.run(t, probe, storeInitialBatch(t)); !errors.Is(err, cause) {
						t.Fatalf("conformance hid an operational failure: %v", err)
					}
				})
			}
		})
	}
}
