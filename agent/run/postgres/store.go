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
	tables   relations
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
	tables := newRelations(schema)
	probe, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()
	rows, err := config.Pool.Query(probe, "SELECT h.root,h.lsn,h.epoch,l.root,l.lsn,l.epoch,l.kind,l.body FROM "+tables.heads+" h,"+tables.log+" l LIMIT 0")
	if err != nil {
		return nil, fmt.Errorf("postgres: validate store relations: %w", err)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: validate store relations: %w", err)
	}
	lifetime, stop := context.WithCancel(context.WithoutCancel(ctx))
	return &Store{pool: config.Pool, tables: tables, timeout: config.Timeout, prepare: config.Prepare, shutdown: lifetime, stop: stop}, nil
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
	// One statement reads the head and the recovery cut from one MVCC snapshot,
	// even while another writer activates the tree or appends a checkpoint.
	rows, err := s.pool.Query(op, "SELECT h.lsn::text,h.epoch::text,l.lsn::text,l.epoch::text,l.kind,l.body FROM "+s.tables.heads+" h LEFT JOIN "+s.tables.log+" l ON l.root=h.root AND l.lsn>=COALESCE((SELECT max(c.lsn) FROM "+s.tables.log+" c WHERE c.root=h.root AND c.kind=$2),1) WHERE h.root=$1 ORDER BY l.lsn", root.String(), string(run.RecordKindCheckpoint))
	if err != nil {
		return run.Log{}, false, fmt.Errorf("postgres: load log %s: %w", root, err)
	}
	defer rows.Close()
	log := run.Log{Root: root}
	var head run.LSN
	var epoch run.Epoch
	found := false
	for rows.Next() {
		var headText, epochText string
		var position, writerEpoch, kind *string
		var body []byte
		if scanErr := rows.Scan(&headText, &epochText, &position, &writerEpoch, &kind, &body); scanErr != nil {
			return run.Log{}, false, scanErr
		}
		if !found {
			head, epoch, err = parseHead(headText, epochText)
			if err != nil {
				return run.Log{}, false, err
			}
			found = true
		}
		if position == nil || writerEpoch == nil || kind == nil {
			return run.Log{}, false, errors.New("postgres: log head has no records")
		}
		entry, err := decodeEntry(*position, *writerEpoch, *kind, body)
		if err != nil {
			return run.Log{}, false, err
		}
		log.Entries = append(log.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return run.Log{}, false, err
	}
	if !found {
		return run.Log{}, false, nil
	}
	if err := log.Validate(); err != nil {
		return run.Log{}, false, fmt.Errorf("postgres: invalid log %s: %w", root, err)
	}
	if log.Head() != head || log.Epoch() != epoch {
		return run.Log{}, false, errors.New("postgres: log head does not address its last record")
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
		rows, err := s.pool.Query(op, "SELECT lsn::text,epoch::text,kind,body FROM "+s.tables.log+" WHERE root=$1 AND lsn>$2::text::numeric ORDER BY lsn", root.String(), strconv.FormatUint(uint64(after), 10))
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
	// A fresh READ COMMITTED statement sees a creator that won an absent-head
	// race. The root lock serializes both creation and later CAS appends.
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
	if _, lockErr := tx.Exec(op, "SELECT pg_advisory_xact_lock(hashtextextended($1,$2))", batch.Root.String(), rootLockSeed); lockErr != nil {
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
	nextEpoch, duplicate, err := batch.Admit(epoch, head, existing)
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
	entries := batch.Entries()
	last := entries[len(entries)-1].LSN
	if _, err := tx.Exec(op, "INSERT INTO "+s.tables.heads+"(root,lsn,epoch) VALUES($1,$2::text::numeric,$3::text::numeric) ON CONFLICT(root) DO UPDATE SET lsn=excluded.lsn,epoch=excluded.epoch", batch.Root.String(), strconv.FormatUint(uint64(last), 10), strconv.FormatUint(uint64(nextEpoch), 10)); err != nil {
		return err
	}
	if err := s.writeEntries(op, tx, batch.Root, entries); err != nil {
		return err
	}
	if mutation.Apply != nil {
		if err := mutation.Apply(op, writer{tx}); err != nil {
			return err
		}
	}
	return tx.Commit(op)
}

func (s *Store) head(ctx context.Context, tx pgx.Tx, root run.ProcessID) (run.LSN, run.Epoch, error) {
	var positionText, epochText string
	err := tx.QueryRow(ctx, "SELECT lsn::text,epoch::text FROM "+s.tables.heads+" WHERE root=$1", root.String()).Scan(&positionText, &epochText)
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
	rows, err := tx.Query(ctx, "SELECT lsn::text,epoch::text,kind,body FROM "+s.tables.log+" WHERE root=$1 AND lsn>$2::text::numeric AND lsn<=$3::text::numeric ORDER BY lsn", batch.Root.String(), strconv.FormatUint(uint64(batch.After), 10), strconv.FormatUint(uint64(end), 10))
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
		kinds[i], bodies[i] = kind, body
	}
	_, err := tx.Exec(ctx, "INSERT INTO "+s.tables.log+"(root,lsn,epoch,kind,body) SELECT $1,r.lsn::numeric,r.epoch::numeric,r.kind,r.body FROM unnest($2::text[],$3::text[],$4::text[],$5::bytea[]) r(lsn,epoch,kind,body)", root.String(), positions, epochs, kinds, bodies)
	return err
}

func parseHead(position, epoch string) (run.LSN, run.Epoch, error) {
	head, err := parsePosition[run.LSN](position)
	if err != nil {
		return 0, 0, err
	}
	writer, err := parsePosition[run.Epoch](epoch)
	return head, writer, err
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
	record, err := run.DecodeRecord(kind, body)
	if err != nil {
		return run.Entry{}, err
	}
	return run.Entry{LSN: lsn, Epoch: writerEpoch, Record: record}, nil
}

var _ run.Store = (*Store)(nil)
