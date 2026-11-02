package postgres

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/agent/run"
)

// Writer executes Host statements within a Store-owned transaction. The Store
// alone decides when to finish the transaction.
type Writer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}
type writer struct{ tx pgx.Tx }

func (w writer) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return w.tx.Exec(ctx, sql, args...)
}

// Mutation identifies the exact Host write set; Apply may only use the
// provided transaction and must not finish it or perform external effects.
type Mutation struct {
	Digest run.Digest
	Apply  func(context.Context, Writer) error
}

type Config struct {
	Pool   *pgxpool.Pool
	Schema string
	// Timeout bounds each operation independently of the engine's detached commit context.
	Timeout time.Duration
	// Prepare must be deterministic and free of I/O. A nil function binds no Host writes.
	Prepare func(Commit) (Mutation, error)
}

type Store struct {
	pool     *pgxpool.Pool
	tables   relations
	timeout  time.Duration
	prepare  func(Commit) (Mutation, error)
	shutdown context.Context
	stop     context.CancelFunc
}

func New(ctx context.Context, config Config) (*Store, error) {
	if config.Pool == nil || config.Timeout <= 0 {
		return nil, errors.New("postgres: pool and positive storage timeout are required")
	}
	schema, err := namespace(config.Schema)
	if err != nil {
		return nil, err
	}
	tables := newRelations(schema)
	probe, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()
	rows, err := config.Pool.Query(probe, "SELECT h.root_id,h.digest,h.incarnation,h.limits,h.sequence,p.root_id,p.process_id,p.snapshot,f.identity,f.scope_content,f.business_content,s.scope,s.start_key,s.intent,s.root_id FROM "+tables.heads+" h,"+tables.processes+" p,"+tables.facts+" f,"+tables.starts+" s LIMIT 0")
	if err != nil {
		return nil, fmt.Errorf("postgres: validate store relations: %w", err)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: validate store relations: %w", err)
	}
	lifetime, stop := context.WithCancel(context.Background())
	return &Store{pool: config.Pool, tables: tables, timeout: config.Timeout, prepare: config.Prepare, shutdown: lifetime, stop: stop}, nil
}

// Close interrupts outstanding database operations. Join runtime-owned tasks
// before closing the Host's pool. Close never removes durable evidence.
func (s *Store) Close() { s.stop() }

func (s *Store) operation(ctx context.Context) (context.Context, func()) {
	op, cancel := context.WithTimeout(ctx, s.timeout)
	stop := context.AfterFunc(s.shutdown, cancel)
	if s.shutdown.Err() != nil {
		cancel()
	}
	return op, func() { stop(); cancel() }
}

func (s *Store) LoadTree(ctx context.Context, id run.ProcessID) (run.TreeSnapshot, bool, error) {
	if !id.Valid() {
		return run.TreeSnapshot{}, false, run.ErrInvalidTreeSnapshot
	}
	op, done := s.operation(ctx)
	defer done()
	var digest, writer string
	var encodedLimits []byte
	var keys []string
	var records [][]byte
	// Ordering both arrays by the same primary key keeps each opaque record
	// paired with its physical address within one MVCC snapshot.
	err := s.pool.QueryRow(op, "SELECT h.digest,h.incarnation,h.limits,m.keys,m.records FROM "+s.tables.heads+" h CROSS JOIN LATERAL (SELECT array_agg(p.process_id ORDER BY p.process_id),array_agg(p.snapshot ORDER BY p.process_id) FROM "+s.tables.processes+" p WHERE p.root_id=h.root_id) m(keys,records) WHERE h.root_id=$1", id.String()).Scan(&digest, &writer, &encodedLimits, &keys, &records)
	if errors.Is(err, pgx.ErrNoRows) {
		return run.TreeSnapshot{}, false, nil
	}
	if err != nil {
		return run.TreeSnapshot{}, false, err
	}
	var incarnation run.TreeIncarnationID
	if err = incarnation.UnmarshalText([]byte(writer)); err != nil {
		return run.TreeSnapshot{}, false, err
	}
	var limits run.TreeLimits
	if err = jsonv2.Unmarshal(encodedLimits, &limits); err != nil {
		return run.TreeSnapshot{}, false, err
	}
	members := make([]json.RawMessage, len(records))
	for i, record := range records {
		members[i] = record
	}
	head, err := run.AssembleTreeSnapshot(incarnation, limits, members)
	if err == nil && (head.RootID() != id || head.Digest().String() != digest) {
		err = run.ErrInvalidTreeSnapshot
	}
	if err != nil {
		return run.TreeSnapshot{}, false, err
	}
	// A record under the wrong key has the same tree digest but would receive
	// another member's next incremental update. Verify the indexed bytes, too.
	if len(keys) != len(records) {
		return run.TreeSnapshot{}, false, run.ErrInvalidTreeSnapshot
	}
	indexed := make(map[string]run.Digest, len(records))
	for i, key := range keys {
		indexed[key] = run.ComputeDigest(records[i])
	}
	for _, member := range head.ProcessSnapshots() {
		if indexed[member.Relation().ProcessID().String()] != member.Digest() {
			return run.TreeSnapshot{}, false, run.ErrInvalidTreeSnapshot
		}
	}
	return head, true, nil
}

type rowReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Store) LookupStart(ctx context.Context, id run.StartID) (run.StartReceipt, bool, error) {
	if !id.Valid() {
		return run.StartReceipt{}, false, run.ErrInvalidStart
	}
	op, done := s.operation(ctx)
	defer done()
	return s.lookupStart(op, s.pool, id)
}

func (s *Store) lookupStart(ctx context.Context, query rowReader, id run.StartID) (run.StartReceipt, bool, error) {
	var intent, root string
	err := query.QueryRow(ctx, "SELECT intent,root_id FROM "+s.tables.starts+" WHERE scope=$1 AND start_key=$2", []byte(id.Scope), []byte(id.Key)).Scan(&intent, &root)
	if errors.Is(err, pgx.ErrNoRows) {
		return run.StartReceipt{}, false, nil
	}
	if err != nil {
		return run.StartReceipt{}, false, err
	}
	digest, err := run.ParseDigest(intent)
	if err != nil {
		return run.StartReceipt{}, false, err
	}
	pid, err := run.ParseProcessID(root)
	if err != nil {
		return run.StartReceipt{}, false, err
	}
	return run.StartReceipt{ID: id, Intent: digest, RootID: pid}, true, nil
}

// Commit is the frozen tree change whose Host write set will share its transaction.
type Commit struct {
	changes  iter.Seq2[run.ProcessID, run.ProcessSnapshot]
	identity string
	content  run.Digest
	snapshot run.TreeSnapshot
	previous run.Digest
	writer   run.TreeIncarnationID
	sequence uint64
	start    *run.StartReceipt
}

func (c Commit) Identity() string               { return c.identity }
func (c Commit) TreeSnapshot() run.TreeSnapshot { return c.snapshot }
func (c Commit) Sequence() uint64               { return c.sequence }

func (s *Store) ActivateTree(ctx context.Context, a run.TreeActivation) error {
	content, err := a.ContentDigest()
	if err != nil {
		return err
	}
	return s.commit(ctx, Commit{changes: a.ChangedProcesses(), identity: a.Identity(), content: content, snapshot: a.TreeSnapshot(), previous: a.PreviousTreeDigest(), writer: a.PreviousIncarnationID()})
}

func (s *Store) CommitEffect(ctx context.Context, b run.EffectBoundary) error {
	content, err := b.ContentDigest()
	if err != nil {
		return err
	}
	return s.commit(ctx, Commit{changes: b.ChangedProcesses(), identity: b.Identity(), content: content, snapshot: b.TreeSnapshot(), previous: b.PreviousTreeDigest(), writer: b.TreeSnapshot().IncarnationID(), sequence: b.Sequence()})
}

func (s *Store) CommitCheckpoint(ctx context.Context, c run.TreeCheckpoint) error {
	content, err := c.ContentDigest()
	if err != nil {
		return err
	}
	p := Commit{changes: c.ChangedProcesses(), identity: c.Identity(), content: content, snapshot: c.TreeSnapshot(), previous: c.PreviousTreeDigest(), writer: c.TreeSnapshot().IncarnationID(), sequence: c.Sequence()}
	if receipt, ok := c.Start(); ok {
		p.start = &receipt
	}
	return s.commit(ctx, p)
}

func (s *Store) commit(ctx context.Context, p Commit) (err error) {
	op, done := s.operation(ctx)
	defer done()
	var mutation Mutation
	if s.prepare != nil {
		mutation, err = s.prepare(p)
		if err != nil {
			return err
		}
		if mutation.Apply == nil || !mutation.Digest.Valid() {
			return errors.New("postgres: invalid host mutation")
		}
	}
	// READ COMMITTED gives a losing INSERT a fresh view of the winner after
	// its unique-key wait. An inherited repeatable-read snapshot cannot do so.
	tx, err := s.pool.BeginTx(op, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout)
		defer cancel()
		if rollbackErr := tx.Rollback(cleanup); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	if p.start != nil {
		if err = s.admitStart(op, tx, *p.start); err != nil {
			return err
		}
	}
	// A row lock cannot serialize creation of an absent head. A hash
	// collision can serialize unrelated roots, but cannot grant write access.
	if _, err = tx.Exec(op, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", p.snapshot.RootID().String()); err != nil {
		return err
	}
	duplicate, err := s.install(op, tx, p, mutation.Digest)
	if err != nil {
		return err
	}
	if !duplicate && mutation.Apply != nil {
		if err = mutation.Apply(op, writer{tx}); err != nil {
			return err
		}
	}
	return tx.Commit(op)
}

func (s *Store) admitStart(ctx context.Context, tx pgx.Tx, receipt run.StartReceipt) error {
	id := receipt.ID
	tag, err := tx.Exec(ctx, "INSERT INTO "+s.tables.starts+"(scope,start_key,intent,root_id) VALUES($1,$2,$3,$4) ON CONFLICT(scope,start_key) DO NOTHING", []byte(id.Scope), []byte(id.Key), receipt.Intent.String(), receipt.RootID.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	found, exists, err := s.lookupStart(ctx, tx, id)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("postgres: conflicting start receipt disappeared")
	}
	if found.Intent != receipt.Intent {
		return &run.StartConflictError{Receipt: found}
	}
	if found.RootID != receipt.RootID {
		return &run.StartAcceptedError{Receipt: found}
	}
	return nil
}

func (s *Store) install(ctx context.Context, tx pgx.Tx, p Commit, business run.Digest) (bool, error) {
	var digest, writer, sequenceText string
	root := p.snapshot.RootID()
	err := tx.QueryRow(ctx, "SELECT digest,incarnation,sequence::text FROM "+s.tables.heads+" WHERE root_id=$1", root.String()).Scan(&digest, &writer, &sequenceText)
	exists := !errors.Is(err, pgx.ErrNoRows)
	var sequence uint64
	if exists {
		if err != nil {
			return false, err
		}
		sequence, err = strconv.ParseUint(sequenceText, 10, 64)
		if err != nil {
			return false, err
		}
	}
	if p.sequence > 0 && p.previous.Valid() && (!exists || writer != p.writer.String()) {
		return false, run.ErrTreeIncarnationConflict
	}

	var content, savedBusiness string
	err = tx.QueryRow(ctx, "SELECT scope_content,business_content FROM "+s.tables.facts+" WHERE identity=$1", p.identity).Scan(&content, &savedBusiness)
	if err == nil {
		if content == p.content.String() && savedBusiness == business.String() && exists && sequence == p.sequence && digest == p.snapshot.Digest().String() {
			return true, nil
		}
		return false, run.ErrCommitConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if !p.previous.Valid() {
		if exists {
			return false, run.ErrTreeIncarnationConflict
		}
	} else if !exists || digest != p.previous.String() || writer != p.writer.String() || (p.sequence > 0 && (sequence == math.MaxUint64 || p.sequence != sequence+1)) {
		return false, run.ErrTreeIncarnationConflict
	}
	if _, err = tx.Exec(ctx, "INSERT INTO "+s.tables.facts+"(identity,scope_content,business_content) VALUES($1,$2,$3)", p.identity, p.content.String(), business.String()); err != nil {
		return false, err
	}
	limits, err := jsonv2.Marshal(p.snapshot.Limits())
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO "+s.tables.heads+"(root_id,digest,incarnation,limits,sequence) VALUES($1,$2,$3,$4,$5::text::numeric) ON CONFLICT(root_id) DO UPDATE SET digest=excluded.digest,incarnation=excluded.incarnation,limits=excluded.limits,sequence=excluded.sequence", root.String(), p.snapshot.Digest().String(), p.snapshot.IncarnationID().String(), limits, strconv.FormatUint(p.sequence, 10))
	if err != nil {
		return false, err
	}
	return false, s.writeProcesses(ctx, tx, p)
}

func (s *Store) writeProcesses(ctx context.Context, tx pgx.Tx, commit Commit) error {
	root := commit.snapshot.RootID().String()
	var changed, removed []string
	var records [][]byte
	for id, member := range commit.changes {
		if !member.Valid() {
			removed = append(removed, id.String())
			continue
		}
		changed = append(changed, id.String())
		records = append(records, member.JSON())
	}
	if len(removed) > 0 {
		if _, err := tx.Exec(ctx, "DELETE FROM "+s.tables.processes+" WHERE root_id=$1 AND process_id=ANY($2::text[])", root, removed); err != nil {
			return err
		}
	}
	if len(changed) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, "INSERT INTO "+s.tables.processes+"(root_id,process_id,snapshot) SELECT $1,p.id,p.data FROM unnest($2::text[],$3::bytea[]) p(id,data) ON CONFLICT(root_id,process_id) DO UPDATE SET snapshot=excluded.snapshot", root, changed, records)
	return err
}
