//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
	"github.com/Tangerg/scope/runstores/postgres"
)

const storageTimeout = 5 * time.Second

type commitCut string

const (
	cutBeforeTransaction commitCut = "before_transaction"
	cutBeforeCommit      commitCut = "before_commit"
	cutAfterCommit       commitCut = "after_commit"
	cutAfterDispatch     commitCut = "after_dispatch"
)

type postgresWrite struct {
	snapshot run.TreeSnapshot
	sequence uint64
}

type postgresCommitter struct {
	pool     *pgxpool.Pool
	schema   string
	shutdown context.Context
	timeout  time.Duration
	business []byte
	gate     func(context.Context, commitCut, postgresWrite) error
	once     sync.Once
	store    *postgres.Store
	err      error
}

func (p *postgresCommitter) connection(ctx context.Context) (*postgres.Store, error) {
	p.once.Do(func() {
		p.store, p.err = postgres.New(ctx, postgres.Config{
			Pool: p.pool, Schema: p.schema, Timeout: p.timeout, Prepare: p.prepare,
		})
		if p.store != nil {
			context.AfterFunc(p.shutdown, p.store.Close)
		}
	})
	return p.store, p.err
}

func (p *postgresCommitter) prepare(c postgres.Commit) (postgres.Mutation, error) {
	payload := bytes.Clone(p.business)
	return postgres.Mutation{Digest: run.ComputeDigest(payload), Apply: func(ctx context.Context, tx postgres.Writer) error {
		if len(payload) > 0 {
			if _, err := tx.Exec(ctx, "INSERT INTO business_records(identity,payload) VALUES($1,$2)", c.Identity(), payload); err != nil {
				return err
			}
		}
		if p.gate != nil {
			return p.gate(ctx, cutBeforeCommit, postgresWrite{snapshot: c.TreeSnapshot(), sequence: c.Sequence()})
		}
		return nil
	}}, nil
}

func (p *postgresCommitter) exercise(ctx context.Context, w postgresWrite, commit func(*postgres.Store) error) error {
	s, err := p.connection(ctx)
	if err != nil {
		return err
	}
	if p.gate != nil {
		if err = p.gate(ctx, cutBeforeTransaction, w); err != nil {
			return err
		}
	}
	if err = commit(s); err != nil {
		return err
	}
	if p.gate != nil {
		return p.gate(ctx, cutAfterCommit, w)
	}
	return nil
}
func (p *postgresCommitter) ActivateTree(ctx context.Context, a run.TreeActivation) error {
	return p.exercise(ctx, postgresWrite{snapshot: a.TreeSnapshot()}, func(s *postgres.Store) error { return s.ActivateTree(ctx, a) })
}
func (p *postgresCommitter) CommitEffect(ctx context.Context, b run.EffectBoundary) error {
	return p.exercise(ctx, postgresWrite{snapshot: b.TreeSnapshot(), sequence: b.Sequence()}, func(s *postgres.Store) error { return s.CommitEffect(ctx, b) })
}
func (p *postgresCommitter) CommitCheckpoint(ctx context.Context, c run.TreeCheckpoint) error {
	return p.exercise(ctx, postgresWrite{snapshot: c.TreeSnapshot(), sequence: c.Sequence()}, func(s *postgres.Store) error { return s.CommitCheckpoint(ctx, c) })
}
func (p *postgresCommitter) LoadTree(ctx context.Context, id run.ProcessID) (run.TreeSnapshot, bool, error) {
	s, err := p.connection(ctx)
	if err != nil {
		return run.TreeSnapshot{}, false, err
	}
	return s.LoadTree(ctx, id)
}
func (p *postgresCommitter) LookupStart(ctx context.Context, id run.StartID) (run.StartReceipt, bool, error) {
	s, err := p.connection(ctx)
	if err != nil {
		return run.StartReceipt{}, false, err
	}
	return s.LookupStart(ctx, id)
}

func openPool(ctx context.Context, dsn, schema string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	config.MaxConns = 2
	config.ConnConfig.RuntimeParams["search_path"] = pgx.Identifier{schema}.Sanitize()
	config.ConnConfig.RuntimeParams["application_name"] = schema
	return pgxpool.NewWithConfig(ctx, config)
}

type postgresFixture struct {
	dsn       string
	schema    string
	committer *postgresCommitter
}

func newPostgresFixture(t *testing.T) postgresFixture {
	t.Helper()
	dsn := os.Getenv("SCOPE_TREECOMMITTER_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("SCOPE_TREECOMMITTER_POSTGRES_DSN is required; run scripts/check-tree-committer.sh")
	}
	schema := "scope_tree_" + strings.ToLower(rand.Text())
	admin, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), storageTimeout)
		defer cancel()
		if closeErr := admin.Close(ctx); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
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
	pool, err := openPool(t.Context(), dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ddl, err := postgres.SchemaSQL(schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), ddl+`
        CREATE TABLE business_records(identity text PRIMARY KEY REFERENCES commit_facts(identity),payload bytea NOT NULL);
		CREATE TABLE external_calls(effect_id text NOT NULL,attempt_id text NOT NULL,payload bytea NOT NULL,PRIMARY KEY(effect_id,attempt_id));
	`); err != nil {
		t.Fatal(err)
	}
	return postgresFixture{dsn: dsn, schema: schema, committer: &postgresCommitter{pool: pool, schema: schema, shutdown: t.Context(), timeout: storageTimeout}}
}

func TestPostgresTreeCommitterConformance(t *testing.T) {
	runtest.RunTreeCommitterConformance(t, func() runtest.TreeCommitterConformanceDriver {
		return newPostgresFixture(t).committer
	})
}

var _ runtest.TreeCommitterConformanceDriver = (*postgresCommitter)(nil)
