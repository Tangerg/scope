//go:build integration

package treecommitterconformance

import (
	"context"
	"crypto/rand"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
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
	identity       string
	content        agent.Digest
	snapshot       agent.TreeSnapshot
	previous       agent.Digest
	previousWriter agent.TreeIncarnationID
	sequence       uint64
}

type postgresCommitter struct {
	pool     *pgxpool.Pool
	shutdown context.Context
	timeout  time.Duration
	business []byte
	gate     func(context.Context, commitCut, postgresWrite) error
}

func (p *postgresCommitter) ActivateTree(ctx context.Context, activation agent.TreeActivation) error {
	content, err := activation.ContentDigest()
	if err != nil {
		return err
	}
	return p.commit(ctx, postgresWrite{
		identity: activation.Identity(), content: content, snapshot: activation.TreeSnapshot(),
		previous: activation.PreviousTreeDigest(), previousWriter: activation.PreviousIncarnationID(),
	})
}

func (p *postgresCommitter) CommitEffect(ctx context.Context, boundary agent.EffectBoundary) error {
	content, err := boundary.ContentDigest()
	if err != nil {
		return err
	}
	return p.commit(ctx, postgresWrite{
		identity: boundary.Identity(), content: content, snapshot: boundary.TreeSnapshot(),
		previous: boundary.PreviousTreeDigest(), previousWriter: boundary.TreeSnapshot().IncarnationID(), sequence: boundary.Sequence(),
	})
}

func (p *postgresCommitter) CommitCheckpoint(ctx context.Context, checkpoint agent.TreeCheckpoint) error {
	content, err := checkpoint.ContentDigest()
	if err != nil {
		return err
	}
	return p.commit(ctx, postgresWrite{
		identity: checkpoint.Identity(), content: content, snapshot: checkpoint.TreeSnapshot(),
		previous: checkpoint.PreviousTreeDigest(), previousWriter: checkpoint.TreeSnapshot().IncarnationID(), sequence: checkpoint.Sequence(),
	})
}

func (p *postgresCommitter) LoadTree(ctx context.Context, rootID agent.ProcessID) (agent.TreeSnapshot, bool, error) {
	var encoded []byte
	err := p.pool.QueryRow(ctx, "SELECT snapshot FROM tree_heads WHERE root_id=$1", rootID.String()).Scan(&encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return agent.TreeSnapshot{}, false, nil
	}
	if err != nil {
		return agent.TreeSnapshot{}, false, err
	}
	head, err := agent.ParseTreeSnapshot(encoded)
	if err == nil && head.RootID() != rootID {
		return agent.TreeSnapshot{}, false, agent.ErrInvalidTreeSnapshot
	}
	return head, err == nil, err
}

func (p *postgresCommitter) commit(ctx context.Context, write postgresWrite) (commitErr error) {
	storageContext, cancel := context.WithTimeout(ctx, p.timeout)
	stop := context.AfterFunc(p.shutdown, cancel)
	defer func() { stop(); cancel() }()
	if p.shutdown.Err() != nil {
		cancel()
	}
	if p.gate != nil {
		if err := p.gate(storageContext, cutBeforeTransaction, write); err != nil {
			return err
		}
	}
	tx, err := p.pool.Begin(storageContext)
	if err != nil {
		return err
	}
	// pgx does not automatically roll back a canceled transaction. Cleanup
	// gets its own bound so a failed storage deadline cannot retain the lock.
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), p.timeout)
		defer cleanupCancel()
		if rollbackErr := tx.Rollback(cleanupContext); !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			commitErr = errors.Join(commitErr, rollbackErr)
		}
	}()
	// The transaction lock also serializes creation of an absent head. Every
	// writer uses this same key; a hash collision only serializes extra roots.
	if _, err = tx.Exec(storageContext, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", write.snapshot.RootID().String()); err != nil {
		return err
	}
	duplicate, err := p.install(storageContext, tx, write)
	if err != nil {
		return err
	}
	if !duplicate && p.gate != nil {
		if gateErr := p.gate(storageContext, cutBeforeCommit, write); gateErr != nil {
			return gateErr
		}
	}
	if err = tx.Commit(storageContext); err != nil {
		return err
	}
	if !duplicate && p.gate != nil {
		return p.gate(storageContext, cutAfterCommit, write)
	}
	return nil
}

func (p *postgresCommitter) install(ctx context.Context, tx pgx.Tx, write postgresWrite) (bool, error) {
	var encoded []byte
	var sequenceText string
	err := tx.QueryRow(ctx, "SELECT snapshot,sequence::text FROM tree_heads WHERE root_id=$1", write.snapshot.RootID().String()).Scan(&encoded, &sequenceText)
	exists := !errors.Is(err, pgx.ErrNoRows)
	var head agent.TreeSnapshot
	var sequence uint64
	if exists {
		if err != nil {
			return false, err
		}
		head, err = agent.ParseTreeSnapshot(encoded)
		if err != nil {
			return false, err
		}
		sequence, err = strconv.ParseUint(sequenceText, 10, 64)
		if err != nil {
			return false, err
		}
	}
	if write.sequence > 0 && write.previous.Valid() && (!exists || head.IncarnationID() != write.previousWriter) {
		return false, agent.ErrTreeIncarnationConflict
	}
	var scopeContent, businessContent string
	err = tx.QueryRow(ctx, "SELECT scope_content,business_content FROM commit_facts WHERE identity=$1", write.identity).Scan(&scopeContent, &businessContent)
	businessDigest := agent.ComputeDigest(p.business).String()
	if err == nil {
		if scopeContent == write.content.String() && businessContent == businessDigest && exists && sequence == write.sequence && head.Digest() == write.snapshot.Digest() {
			return true, nil
		}
		return false, agent.ErrCommitConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if !write.previous.Valid() {
		if exists {
			return false, agent.ErrTreeIncarnationConflict
		}
	} else {
		if !exists || head.Digest() != write.previous || head.IncarnationID() != write.previousWriter {
			return false, agent.ErrTreeIncarnationConflict
		}
		if write.sequence > 0 && (sequence == math.MaxUint64 || write.sequence != sequence+1) {
			return false, agent.ErrTreeIncarnationConflict
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO commit_facts(identity,scope_content,business_content) VALUES($1,$2,$3)", write.identity, write.content.String(), businessDigest); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO tree_heads(root_id,snapshot,sequence) VALUES($1,$2,$3::text::numeric) ON CONFLICT(root_id) DO UPDATE SET snapshot=excluded.snapshot,sequence=excluded.sequence",
		write.snapshot.RootID().String(), write.snapshot.JSON(), strconv.FormatUint(write.sequence, 10)); err != nil {
		return false, err
	}
	if len(p.business) > 0 {
		if _, err = tx.Exec(ctx, "INSERT INTO business_records(identity,payload) VALUES($1,$2)", write.identity, p.business); err != nil {
			return false, err
		}
	}
	return false, nil
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
	if _, err = pool.Exec(t.Context(), `
		CREATE TABLE tree_heads(root_id text PRIMARY KEY,snapshot bytea NOT NULL,sequence numeric(20,0) NOT NULL CHECK(sequence BETWEEN 0 AND 18446744073709551615));
		CREATE TABLE commit_facts(identity text PRIMARY KEY,scope_content text NOT NULL,business_content text NOT NULL);
		CREATE TABLE business_records(identity text PRIMARY KEY REFERENCES commit_facts(identity),payload bytea NOT NULL);
		CREATE TABLE external_calls(effect_id text NOT NULL,attempt_id text NOT NULL,payload bytea NOT NULL,PRIMARY KEY(effect_id,attempt_id));
	`); err != nil {
		t.Fatal(err)
	}
	return postgresFixture{dsn: dsn, schema: schema, committer: &postgresCommitter{pool: pool, shutdown: t.Context(), timeout: storageTimeout}}
}

func TestPostgresTreeCommitterConformance(t *testing.T) {
	agenttest.RunTreeCommitterConformance(t, func() agenttest.TreeCommitterConformanceDriver {
		return newPostgresFixture(t).committer
	})
}

var _ agenttest.TreeCommitterConformanceDriver = (*postgresCommitter)(nil)
