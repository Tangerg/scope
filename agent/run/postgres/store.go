package postgres

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/agent/run"
)

const rootLockSeed int64 = 0

// rootLockSQL takes the transaction-scoped lock that serializes appends to one
// root. The relation name scopes the key, so stores in different namespaces
// never block each other.
const rootLockSQL = "SELECT pg_advisory_xact_lock(hashtextextended(jsonb_build_array($1::text,$2::text)::text,$3))"

// Writer confines Host writes to the transaction whose append owns their commit.
type Writer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type writer struct{ tx pgx.Tx }

func (w writer) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return w.tx.Exec(ctx, sql, args...)
}

// Mutation may only use its supplied transaction; external effects cannot be
// rolled back together with an append.
type Mutation struct {
	Apply func(context.Context, Writer) error
}

type Config struct {
	Pool    *pgxpool.Pool
	Schema  string
	Timeout time.Duration
	// Prepare is deterministic and free of I/O. An acknowledged retry does not
	// invoke it again, so Host writes share the log's idempotency boundary.
	Prepare func(run.Batch) (Mutation, error)
}

type Store struct {
	pool     *pgxpool.Pool
	log      string
	timeout  time.Duration
	prepare  func(run.Batch) (Mutation, error)
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
	log := logRelation(schema)
	probe, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()
	rows, err := config.Pool.Query(probe, "SELECT root,lsn,epoch,kind,body FROM "+log+" LIMIT 0")
	if err != nil {
		return nil, fmt.Errorf("postgres: validate store relations: %w", err)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: validate store relations: %w", err)
	}
	lifetime, stop := context.WithCancel(context.WithoutCancel(ctx))
	return &Store{pool: config.Pool, log: log, timeout: config.Timeout, prepare: config.Prepare, shutdown: lifetime, stop: stop}, nil
}

// Close cancels storage operations without deleting the durable log. The Host
// joins its runtimes before closing the shared pool.
func (s *Store) Close() { s.stop() }

func (s *Store) operation(ctx context.Context) (context.Context, func()) {
	op, cancel := context.WithTimeout(ctx, s.timeout)
	stop := context.AfterFunc(s.shutdown, cancel)
	if s.shutdown.Err() != nil {
		cancel()
	}
	return op, func() { stop(); cancel() }
}

func (s *Store) Load(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
	if !root.Valid() || root.Root() != root {
		return run.Log{}, false, run.ErrInvalidIdentity
	}
	op, done := s.operation(ctx)
	defer done()
	// One statement locates the newest checkpoint and reads from it in one MVCC
	// snapshot, even while another writer activates the tree or appends one.
	rows, err := s.pool.Query(op, "SELECT l.lsn::text,l.epoch::text,l.kind,l.body FROM "+s.log+" l WHERE l.root=$1 AND l.lsn>=COALESCE((SELECT max(c.lsn) FROM "+s.log+" c WHERE c.root=$1 AND c.kind="+checkpointKind+"),1) ORDER BY l.lsn", root.String())
	if err != nil {
		return run.Log{}, false, fmt.Errorf("postgres: load log %s: %w", root, err)
	}
	defer rows.Close()
	log := run.Log{Root: root}
	for entry, err := range scanEntries(rows) {
		if err != nil {
			return run.Log{}, false, err
		}
		log.Entries = append(log.Entries, entry)
	}
	if len(log.Entries) == 0 {
		return run.Log{}, false, nil
	}
	if err := log.Validate(); err != nil {
		return run.Log{}, false, fmt.Errorf("postgres: invalid log %s: %w", root, err)
	}
	return log, true, nil
}

func (s *Store) Read(ctx context.Context, root run.ProcessID, after run.LSN) iter.Seq2[run.Entry, error] {
	return func(yield func(run.Entry, error) bool) {
		if !root.Valid() || root.Root() != root {
			yield(run.Entry{}, run.ErrInvalidIdentity)
			return
		}
		op, done := s.operation(ctx)
		defer done()
		rows, err := s.pool.Query(op, "SELECT l.lsn::text,l.epoch::text,l.kind,l.body FROM "+s.log+" l WHERE l.root=$1 AND l.lsn>$2::text::numeric ORDER BY l.lsn", root.String(), strconv.FormatUint(uint64(after), 10))
		if err != nil {
			yield(run.Entry{}, fmt.Errorf("postgres: read log %s: %w", root, err))
			return
		}
		defer rows.Close()
		for entry, err := range scanEntries(rows) {
			if !yield(entry, err) || err != nil {
				return
			}
		}
	}
}

func (s *Store) Append(ctx context.Context, batch run.Batch) (err error) {
	owned, err := batch.Clone()
	if err != nil {
		return err
	}
	batch = owned
	op, done := s.operation(ctx)
	defer done()
	// The root lock serializes creation and every later CAS append; a fresh
	// READ COMMITTED statement after it sees the head the previous holder wrote.
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
	if _, lockErr := tx.Exec(op, rootLockSQL, s.log, batch.Root.String(), rootLockSeed); lockErr != nil {
		return lockErr
	}
	head, epoch, err := s.head(op, tx, batch.Root)
	if err != nil {
		return err
	}
	var existing []run.Entry
	if batch.After < head {
		existing, err = s.rangeEntries(op, tx, batch)
		if err != nil {
			return err
		}
	}
	_, duplicate, err := batch.Admit(epoch, head, existing)
	if err != nil {
		return err
	}
	if duplicate {
		return tx.Commit(op)
	}
	var mutation Mutation
	if s.prepare != nil {
		declaration, cloneErr := batch.Clone()
		if cloneErr != nil {
			return cloneErr
		}
		mutation, err = s.prepare(declaration)
		if err != nil {
			return err
		}
		if mutation.Apply == nil {
			return errors.New("postgres: host mutation has no Apply function")
		}
	}
	if err := s.writeEntries(op, tx, batch.Root, batch.Entries()); err != nil {
		return err
	}
	if mutation.Apply != nil {
		if err := mutation.Apply(op, writer{tx}); err != nil {
			return err
		}
	}
	return tx.Commit(op)
}

// head reads the coordinates of the last committed entry, which is the only
// record of where the tree's log ends and which writer Epoch holds it.
func (s *Store) head(ctx context.Context, tx pgx.Tx, root run.ProcessID) (run.LSN, run.Epoch, error) {
	var positionText, epochText string
	err := tx.QueryRow(ctx, "SELECT l.lsn::text,l.epoch::text FROM "+s.log+" l WHERE l.root=$1 ORDER BY l.lsn DESC LIMIT 1", root.String()).Scan(&positionText, &epochText)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return parseHead(positionText, epochText)
}

func (s *Store) rangeEntries(ctx context.Context, tx pgx.Tx, batch run.Batch) ([]run.Entry, error) {
	end := batch.After + run.LSN(len(batch.Records))
	rows, err := tx.Query(ctx, "SELECT l.lsn::text,l.epoch::text,l.kind,l.body FROM "+s.log+" l WHERE l.root=$1 AND l.lsn>$2::text::numeric AND l.lsn<=$3::text::numeric ORDER BY l.lsn", batch.Root.String(), strconv.FormatUint(uint64(batch.After), 10), strconv.FormatUint(uint64(end), 10))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []run.Entry
	for entry, err := range scanEntries(rows) {
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// scanEntries reads coordinates as text because numeric(20,0) holds the whole
// uint64 range. Every query orders by the qualified numeric column: a bare
// ORDER BY lsn binds to the text output column and sorts "10" before "9".
func scanEntries(rows pgx.Rows) iter.Seq2[run.Entry, error] {
	return func(yield func(run.Entry, error) bool) {
		for rows.Next() {
			var position, epoch, kind string
			var body []byte
			if err := rows.Scan(&position, &epoch, &kind, &body); err != nil {
				yield(run.Entry{}, err)
				return
			}
			entry, err := decodeEntry(position, epoch, kind, body)
			if !yield(entry, err) || err != nil {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(run.Entry{}, err)
		}
	}
}

func (s *Store) writeEntries(ctx context.Context, tx pgx.Tx, root run.ProcessID, entries []run.Entry) error {
	positions := make([]string, len(entries))
	epochs := make([]string, len(entries))
	kinds := make([]string, len(entries))
	bodies := make([][]byte, len(entries))
	for i, entry := range entries {
		kind, body, err := run.EncodeRecord(entry.Record)
		if err != nil {
			return err
		}
		positions[i] = strconv.FormatUint(uint64(entry.LSN), 10)
		epochs[i] = strconv.FormatUint(uint64(entry.Epoch), 10)
		kinds[i], bodies[i] = string(kind), body
	}
	_, err := tx.Exec(ctx, "INSERT INTO "+s.log+"(root,lsn,epoch,kind,body) SELECT $1,r.lsn::numeric,r.epoch::numeric,r.kind,r.body FROM unnest($2::text[],$3::text[],$4::text[],$5::bytea[]) r(lsn,epoch,kind,body)", root.String(), positions, epochs, kinds, bodies)
	return err
}

func parseHead(position, epoch string) (run.LSN, run.Epoch, error) {
	head, err := parsePosition[run.LSN](position)
	if err != nil {
		return 0, 0, err
	}
	current, err := parsePosition[run.Epoch](epoch)
	return head, current, err
}

func parsePosition[T ~uint64](text string) (T, error) {
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("postgres: invalid log position %q: %w", text, err)
	}
	return T(value), nil
}

func decodeEntry(position, epoch, kind string, body []byte) (run.Entry, error) {
	lsn, err := parsePosition[run.LSN](position)
	if err != nil {
		return run.Entry{}, err
	}
	writerEpoch, err := parsePosition[run.Epoch](epoch)
	if err != nil {
		return run.Entry{}, err
	}
	record, err := run.DecodeRecord(run.RecordKind(kind), body)
	if err != nil {
		return run.Entry{}, err
	}
	return run.Entry{LSN: lsn, Epoch: writerEpoch, Record: record}, nil
}

var _ run.Store = (*Store)(nil)
