//go:build integration

package treecommitterconformance

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Tangerg/scope/agent"
)

type checkpointCapture struct {
	*postgresCommitter
	starts   chan agent.TreeCheckpoint
	contexts chan context.Context
}

func (c *checkpointCapture) CommitCheckpoint(ctx context.Context, checkpoint agent.TreeCheckpoint) error {
	if checkpoint.Kind() == agent.TreeCheckpointKindStart {
		c.starts <- checkpoint
		c.contexts <- ctx
	}
	return c.postgresCommitter.CommitCheckpoint(ctx, checkpoint)
}

func newProbeEngine(t *testing.T, committer agent.TreeCommitter, fixture postgresFixture) (*agent.Engine, agent.Deployment, agent.Payload) {
	t.Helper()
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: committer})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), storageTimeout)
		defer cancel()
		if closeErr := engine.Close(ctx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	deployment, err := probeDeployment(probeDispatcher{pool: fixture.committer.pool})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := agent.EncodePayload(probeValue{Value: "durable"})
	if err != nil {
		t.Fatal(err)
	}
	return engine, deployment, payload
}

func TestBusinessWriteConflict(t *testing.T) {
	fixture := newPostgresFixture(t)
	committer := fixture.committer
	committer.business = []byte("first business write")
	release := make(chan struct{})
	releasePending := sync.OnceFunc(func() { close(release) })
	defer releasePending()
	committer.gate = func(ctx context.Context, cut commitCut, write postgresWrite) error {
		if cut == cutBeforeTransaction && write.sequence == 2 {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	capture := &checkpointCapture{postgresCommitter: committer, starts: make(chan agent.TreeCheckpoint, 1), contexts: make(chan context.Context, 1)}
	engine, deployment, payload := newProbeEngine(t, capture, fixture)
	process, err := engine.Start(t.Context(), deployment, payload)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), storageTimeout)
		defer cancel()
		if killErr := process.Kill(ctx, "fixture cleanup"); killErr != nil && !errors.Is(killErr, agent.ErrProcessFinished) {
			t.Error(killErr)
		}
		if joinErr := process.Join(ctx); joinErr != nil {
			t.Error(joinErr)
		}
	})
	checkpoint := <-capture.starts
	if err := committer.CommitCheckpoint(t.Context(), checkpoint); err != nil {
		t.Fatalf("identical current retry: %v", err)
	}
	conflicting := &postgresCommitter{pool: committer.pool, shutdown: t.Context(), timeout: storageTimeout, business: []byte("different business write")}
	if err := conflicting.CommitCheckpoint(t.Context(), checkpoint); !errors.Is(err, agent.ErrCommitConflict) {
		t.Fatalf("same Scope commit with different business content: %v", err)
	}
	assertDatabaseCounts(t, committer, 1, 1, 1, 0)
	var stored []byte
	if err := committer.pool.QueryRow(t.Context(), "SELECT payload FROM business_records WHERE identity=$1", checkpoint.Identity()).Scan(&stored); err != nil || !bytes.Equal(stored, committer.business) {
		t.Fatalf("business content changed after retry: %q %v", stored, err)
	}
	releasePending()
	if err := process.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestBusinessWriteFailureRollsBackHeadAndFact(t *testing.T) {
	fixture := newPostgresFixture(t)
	fixture.committer.business = []byte("rejected database write")
	if _, err := fixture.committer.pool.Exec(t.Context(), "ALTER TABLE business_records ADD CONSTRAINT reject_fixture CHECK(false)"); err != nil {
		t.Fatal(err)
	}
	capture := &checkpointCapture{postgresCommitter: fixture.committer, starts: make(chan agent.TreeCheckpoint, 1), contexts: make(chan context.Context, 1)}
	engine, deployment, payload := newProbeEngine(t, capture, fixture)
	process, err := engine.Start(t.Context(), deployment, payload)
	pgError, isDatabaseError := errors.AsType[*pgconn.PgError](err)
	if process != nil || !isDatabaseError || pgError.Code != "23514" {
		t.Fatalf("expected database constraint rejection, got Process=%v error=%v", process, err)
	}
	checkpoint := <-capture.starts
	if _, published := engine.Process(checkpoint.TreeSnapshot().RootID()); published {
		t.Fatal("business failure published an Agent Process")
	}
	assertDatabaseCounts(t, fixture.committer, 0, 0, 0, 0)
}

func awaitDatabaseLock(t *testing.T, observer *pgx.Conn, schema string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), storageTimeout)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := observer.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND wait_event='advisory')", schema).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("storage did not reach the real PostgreSQL lock wait")
		case <-ticker.C:
		}
	}
}

func TestBlockedStorageDeadlineAndShutdown(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "deadline"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newPostgresFixture(t)
			committer := fixture.committer
			committer.timeout = time.Second
			shutdownContext, stopStorage := context.WithCancel(t.Context())
			defer stopStorage()
			committer.shutdown = shutdownContext
			release := make(chan struct{})
			committer.gate = func(ctx context.Context, cut commitCut, write postgresWrite) error {
				if cut == cutBeforeTransaction && write.sequence == 1 {
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			}
			capture := &checkpointCapture{postgresCommitter: committer, starts: make(chan agent.TreeCheckpoint, 1), contexts: make(chan context.Context, 1)}
			engine, deployment, payload := newProbeEngine(t, capture, fixture)
			callerContext, stopCaller := context.WithCancel(t.Context())
			defer stopCaller()
			completed := make(chan error, 1)
			go func() {
				_, err := engine.Start(callerContext, deployment, payload)
				completed <- err
			}()
			checkpoint := <-capture.starts
			storageContext := <-capture.contexts
			if storageContext.Done() != nil {
				t.Fatal("Agent passed caller cancellation to storage")
			}
			locker, err := pgx.Connect(t.Context(), fixture.dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), storageTimeout)
				defer cancel()
				if closeErr := locker.Close(ctx); closeErr != nil {
					t.Error(closeErr)
				}
			}()
			tx, err := locker.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", checkpoint.TreeSnapshot().RootID().String()); err != nil {
				t.Fatal(err)
			}
			observer, err := pgx.Connect(t.Context(), fixture.dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), storageTimeout)
				defer cancel()
				if closeErr := observer.Close(ctx); closeErr != nil {
					t.Error(closeErr)
				}
			}()
			close(release)
			awaitDatabaseLock(t, observer, fixture.schema)
			stopCaller()
			want := context.DeadlineExceeded
			if shutdown {
				stopStorage()
				want = context.Canceled
			}
			select {
			case commitErr := <-completed:
				if !errors.Is(commitErr, want) {
					t.Fatalf("storage termination error=%v, want %v", commitErr, want)
				}
			case <-time.After(storageTimeout):
				t.Fatal("storage failed to interrupt the blocked database call")
			}
			if rollbackErr := tx.Rollback(t.Context()); rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			assertDatabaseCounts(t, committer, 0, 0, 0, 0)
		})
	}
}
