package agent_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/agent"
)

type storageContextKey struct{}

func TestHostStorageDeadlineAndShutdownReachTransaction(t *testing.T) {
	for _, event := range []string{"deadline", "shutdown"} {
		for _, phase := range []string{"before", "after"} {
			t.Run(event+"/"+phase, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					store := newHostTreeStore()
					shutdown, stop := context.WithCancel(t.Context())
					defer stop()
					host := &hostTreeCommitter{store: store, shutdown: shutdown, timeout: 5 * time.Second}
					reached := make(chan hostCommitWrite, 1)
					block := func(ctx context.Context, write hostCommitWrite) error {
						if write.sequence != 3 {
							return nil
						}
						if ctx.Value(storageContextKey{}) != "trace" {
							return errors.New("Host trace value did not reach storage")
						}
						if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) != host.timeout {
							return errors.New("Host deadline did not reach storage")
						}
						reached <- write
						<-ctx.Done()
						return ctx.Err()
					}
					if phase == "before" {
						store.before = block
					} else {
						store.after = block
					}
					executionContext, finish := context.WithTimeout(context.WithValue(t.Context(), storageContextKey{}, "trace"), time.Hour)
					defer finish()
					engine, deployment, dispatcher, process := startHostStorageProcess(t, executionContext, host)
					caller, leave := context.WithCancel(executionContext)
					write := <-reached
					leave()
					if err := process.Join(caller); !errors.Is(err, context.Canceled) {
						t.Fatalf("caller did not leave its wait: %v", err)
					}
					wantError := context.DeadlineExceeded
					if event == "shutdown" {
						stop()
						wantError = context.Canceled
					}
					if err := process.Join(t.Context()); !errors.Is(err, wantError) {
						t.Fatalf("storage error=%v, want %v", err, wantError)
					}
					reconcileContext, release := context.WithTimeout(t.Context(), time.Second)
					defer release()
					confirmed, err := store.reconcile(reconcileContext, write)
					if err != nil || confirmed != (phase == "after") {
						t.Fatalf("reconciliation confirmed=%t error=%v", confirmed, err)
					}
					head, exists, err := store.LoadTree(reconcileContext, process.Relation().ProcessID())
					wantHead := write.previous
					if phase == "after" {
						wantHead = write.snapshot.Digest()
					}
					if err != nil || !exists || head.Digest() != wantHead {
						t.Fatalf("storage outcome does not match reconciliation: %v", err)
					}
					if err := engine.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
					if phase == "after" {
						restoredHost := &hostTreeCommitter{store: store, shutdown: t.Context(), timeout: time.Second}
						restoredEngine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: restoredHost})
						if err != nil {
							t.Fatal(err)
						}
						restored, err := restoredEngine.RestoreTree(t.Context(), deployment, head)
						if err != nil {
							t.Fatal(err)
						}
						result, err := restored.Await(t.Context())
						if err != nil || result.Termination().Status() != agent.StatusCompleted {
							t.Fatalf("reconciled recovery: status=%s error=%v", result.Termination().Status(), err)
						}
						if err := restoredEngine.Close(t.Context()); err != nil {
							t.Fatal(err)
						}
						if current, err := store.reconcile(reconcileContext, write); err != nil || current {
							t.Fatalf("historical settlement was still current after activation: %t %v", current, err)
						}
					}
					if attempts := dispatcher.attempts.Load(); attempts != 1 {
						t.Fatalf("storage failure repeated the external Effect: attempts=%d", attempts)
					}
				})
			})
		}
	}
}

func TestHostSlowAcknowledgmentSurvivesCallerLeaving(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newHostTreeStore()
		host := &hostTreeCommitter{store: store, shutdown: t.Context(), timeout: 5 * time.Second}
		reached := make(chan context.Context, 1)
		acknowledge := make(chan struct{})
		store.after = func(ctx context.Context, write hostCommitWrite) error {
			if write.sequence != 3 {
				return nil
			}
			reached <- ctx
			select {
			case <-acknowledge:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		caller, leave := context.WithCancel(t.Context())
		engine, _, dispatcher, process := startHostStorageProcess(t, t.Context(), host)
		storageContext := <-reached
		leave()
		if err := process.Join(caller); !errors.Is(err, context.Canceled) {
			t.Fatalf("caller wait: %v", err)
		}
		if err := storageContext.Err(); err != nil {
			t.Fatalf("caller revoked an accepted storage transaction: %v", err)
		}
		close(acknowledge)
		result, err := process.Await(t.Context())
		if err != nil || result.Termination().Status() != agent.StatusCompleted || dispatcher.attempts.Load() != 1 {
			t.Fatalf("slow acknowledgment changed execution: status=%s attempts=%d error=%v", result.Termination().Status(), dispatcher.attempts.Load(), err)
		}
		if err := engine.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func startHostStorageProcess(t *testing.T, ctx context.Context, committer agent.TreeCommitter) (*agent.Engine, agent.Deployment, *countingDispatcher, *agent.Process) {
	t.Helper()
	definition, err := newEchoDefinition()
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &countingDispatcher{next: echoDispatcher{}}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("storage-context-test")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("storage-context-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodePayload(echoInput{Value: "committed"})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(ctx, deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	return engine, deployment, dispatcher, process
}
