//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
	"github.com/Tangerg/scope/core/jsonschema"
)

const storageTimeout = 5 * time.Second
const databaseEnvironment = "SCOPE_RUN_POSTGRES_DSN"

type postgresFixture struct {
	pool   *pgxpool.Pool
	schema string
	dsn    string
}

func newPostgresFixture(t *testing.T) *postgresFixture {
	t.Helper()
	dsn := os.Getenv(databaseEnvironment)
	if dsn == "" {
		t.Fatal("SCOPE_RUN_POSTGRES_DSN is required; run scripts/check-run-store.sh")
	}
	admin, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := admin.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	schema := "scope_run_" + strings.ToLower(rand.Text())
	if _, createErr := admin.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); createErr != nil {
		t.Fatal(createErr)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), storageTimeout)
		defer cancel()
		connection, connectErr := pgx.Connect(ctx, dsn)
		if connectErr != nil {
			t.Error(connectErr)
			return
		}
		if _, dropErr := connection.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); dropErr != nil {
			t.Error(dropErr)
		}
		if closeErr := connection.Close(ctx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 4
	config.ConnConfig.RuntimeParams["search_path"] = pgx.Identifier{schema}.Sanitize()
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ddl, err := SchemaSQL(schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), ddl+`CREATE TABLE business_records(root text NOT NULL,after_lsn numeric(20,0) NOT NULL,payload text NOT NULL,PRIMARY KEY(root,after_lsn));`); err != nil {
		t.Fatal(err)
	}
	return &postgresFixture{pool: pool, schema: schema, dsn: dsn}
}

func (p *postgresFixture) store(t *testing.T, prepare func(run.Batch) (Mutation, error)) *Store {
	t.Helper()
	store, err := New(t.Context(), Config{Pool: p.pool, Schema: p.schema, Timeout: storageTimeout, Prepare: prepare})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func (p *postgresFixture) businessRows(t *testing.T) int {
	t.Helper()
	var count int
	if err := p.pool.QueryRow(t.Context(), "SELECT count(*) FROM business_records").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPostgresStoreConformance(t *testing.T) {
	runtest.RunStoreConformance(t, func(t *testing.T) run.Store { return newPostgresFixture(t).store(t, nil) })
}

func TestHostMutationCommitsExactlyOnceWithItsAppend(t *testing.T) {
	fixture := newPostgresFixture(t)
	var prepared atomic.Int32
	store := fixture.store(t, func(batch run.Batch) (Mutation, error) {
		prepared.Add(1)
		return Mutation{Apply: func(ctx context.Context, writer Writer) error {
			_, err := writer.Exec(ctx, "INSERT INTO business_records(root,after_lsn,payload) VALUES($1,$2,$3)", batch.Root.String(), uint64(batch.After), "owned")
			return err
		}}, nil
	})
	batch := postgresInitialBatch(t)
	if err := store.Append(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if prepared.Load() != 1 || fixture.businessRows(t) != 1 {
		t.Fatalf("prepare calls=%d rows=%d", prepared.Load(), fixture.businessRows(t))
	}
}

func TestFailedHostMutationRollsBackLog(t *testing.T) {
	fixture := newPostgresFixture(t)
	cause := errors.New("host write rejected")
	store := fixture.store(t, func(batch run.Batch) (Mutation, error) {
		return Mutation{Apply: func(ctx context.Context, writer Writer) error {
			if _, err := writer.Exec(ctx, "INSERT INTO business_records(root,after_lsn,payload) VALUES($1,$2,$3)", batch.Root.String(), uint64(batch.After), "rollback"); err != nil {
				return err
			}
			return cause
		}}, nil
	})
	batch := postgresInitialBatch(t)
	if err := store.Append(t.Context(), batch); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if _, found, err := store.Load(t.Context(), batch.Root); err != nil || found {
		t.Fatalf("rolled back append found=%t error=%v", found, err)
	}
	if fixture.businessRows(t) != 0 {
		t.Fatal("failed append committed Host mutation")
	}
}

func TestCorruptDatabaseRecordsCannotBecomeAcceptedHistory(t *testing.T) {
	for _, corruption := range []struct{ name, sql string }{
		{"kind", "UPDATE log SET kind='unknown' WHERE lsn=1"},
		{"body", "UPDATE log SET body='{}'::bytea WHERE lsn=1"},
		{"position", "UPDATE log SET lsn=lsn+1 WHERE lsn=2"},
		{"epoch", "UPDATE log SET epoch=epoch+1 WHERE lsn=2"},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			fixture := newPostgresFixture(t)
			store := fixture.store(t, nil)
			batch := postgresInitialBatch(t)
			if err := store.Append(t.Context(), batch); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.pool.Exec(t.Context(), corruption.sql); err != nil {
				t.Fatal(err)
			}
			if _, found, err := store.Load(t.Context(), batch.Root); err == nil || found {
				t.Fatalf("corrupt log accepted found=%t error=%v", found, err)
			}
		})
	}
}

func TestStorageDeadlineInterruptsRootLockWait(t *testing.T) {
	fixture := newPostgresFixture(t)
	store := fixture.store(t, nil)
	// Schema validation must finish before the deliberately short lock deadline.
	store.timeout = 50 * time.Millisecond
	batch := postgresInitialBatch(t)
	tx, err := fixture.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(context.WithoutCancel(t.Context())); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			t.Error(rollbackErr)
		}
	}()
	if _, lockErr := tx.Exec(t.Context(), rootLockSQL, store.log, batch.Root.String(), rootLockSeed); lockErr != nil {
		t.Fatal(lockErr)
	}
	if err := store.Append(t.Context(), batch); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked append: %v", err)
	}
}

func TestRootLocksAreConfinedToTheStoreNamespace(t *testing.T) {
	first := newPostgresFixture(t)
	firstStore := first.store(t, nil)
	second := newPostgresFixture(t)
	secondStore := second.store(t, nil)
	secondStore.timeout = time.Second
	batch := postgresInitialBatch(t)
	tx, err := first.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	}()
	if _, err := tx.Exec(t.Context(), rootLockSQL, firstStore.log, batch.Root.String(), rootLockSeed); err != nil {
		t.Fatal(err)
	}
	if err := secondStore.Append(t.Context(), batch); err != nil {
		t.Fatalf("another namespace blocked an unrelated root: %v", err)
	}
}

func postgresInitialBatch(t *testing.T) run.Batch {
	t.Helper()
	memory := run.NewMemoryStore()
	runtime, err := run.New(t.Context(), run.Config{Store: memory, Build: "postgres.conformance"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := runtime.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	schema, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: "postgres.fixture", Description: "Exercise transactional append.", InputSchema: schema, OutputSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	program, err := run.Bind[string, string](runtime.Compiler(), run.BindingConfig{Definition: &postgresDefinition{descriptor: descriptor}, Revision: "one"})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[string]{ID: run.StartID{Scope: "postgres", Key: "conformance"}, Input: "input"})
	if err != nil {
		t.Fatal(err)
	}
	if _, awaitErr := handle.Await(t.Context()); awaitErr != nil {
		t.Fatal(awaitErr)
	}
	log, found, err := memory.Load(t.Context(), handle.ID())
	if err != nil || !found {
		t.Fatalf("fixture log found=%t error=%v", found, err)
	}
	return run.Batch{Root: handle.ID(), Records: []run.Record{log.Entries[0].Record, log.Entries[1].Record}}
}

type postgresDefinition struct{ descriptor run.Descriptor }

func (p *postgresDefinition) Descriptor() run.Descriptor                             { return p.descriptor }
func (p *postgresDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }
func (p *postgresDefinition) Start(input run.Payload) (run.Execution, error) {
	return &postgresExecution{input: input}, nil
}
func (p *postgresDefinition) Restore(_ context.Context, state run.ExecutionState) (run.Execution, error) {
	value, err := state.Decode[string]("postgres.fixture")
	if err != nil {
		return nil, err
	}
	input, err := run.EncodePayload(value)
	return &postgresExecution{input: input}, err
}

type postgresExecution struct{ input run.Payload }

func (p *postgresExecution) Step(_ context.Context, turn run.Turn) (run.Transition, error) {
	return run.Complete(uint32(len(turn.Messages)), p.input)
}
func (p *postgresExecution) Snapshot() (run.ExecutionState, error) {
	return run.ParseExecutionState("postgres.fixture", p.input.JSON())
}

func TestHostPrepareCannotRewriteTheAcceptedBatch(t *testing.T) {
	fixture := newPostgresFixture(t)
	store := fixture.store(t, func(batch run.Batch) (Mutation, error) {
		batch.Records[1] = run.Activated{Epoch: 999}
		return Mutation{Apply: func(context.Context, Writer) error { return nil }}, nil
	})
	batch := postgresInitialBatch(t)
	if err := store.Append(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	log, found, err := store.Load(t.Context(), batch.Root)
	if err != nil || !found || log.Head() != 2 || log.Epoch() != 1 {
		t.Fatalf("Prepare rewrote accepted records: found=%t head=%d epoch=%d error=%v", found, log.Head(), log.Epoch(), err)
	}
	if activation := batch.Records[1].(run.Activated); activation.Epoch != 1 {
		t.Fatal("Prepare mutated its caller's batch")
	}
}
