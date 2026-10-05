package clickhouse

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

const nativeRollbackTimeout = 30 * time.Second

// database/sql's BeginTx only owns the driver's insert buffer. Native SQL
// transactions need BEGIN/COMMIT on one leased TCP session instead.
type nativeTransaction struct {
	conn    *sql.Conn
	ctx     context.Context
	settled bool
}

func newNativeTransaction(ctx context.Context, db *sql.DB) (*nativeTransaction, error) {
	ctx = nativeStatementContext(ctx, false)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: lease transaction session: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN TRANSACTION"); err != nil {
		closeErr := discardSession(conn)
		return nil, errors.Join(fmt.Errorf("clickhouse: begin native transaction: %w", err), closeErr)
	}
	return &nativeTransaction{conn: conn, ctx: ctx}, nil
}

func (n *nativeTransaction) Query(statement string, args ...any) (*sql.Rows, error) {
	return n.conn.QueryContext(n.ctx, statement, args...)
}

func (n *nativeTransaction) Exec(statement string, args ...any) (sql.Result, error) {
	return n.conn.ExecContext(n.ctx, statement, args...)
}

func (n *nativeTransaction) Commit() error {
	if _, err := n.conn.ExecContext(n.ctx, "COMMIT"); err != nil {
		return fmt.Errorf("clickhouse: commit native transaction: %w", err)
	}
	n.settled = true
	return nil
}

func (n *nativeTransaction) Close() error {
	if !n.settled {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(n.ctx), nativeRollbackTimeout)
		defer cancel()
		if _, err := n.conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			return errors.Join(fmt.Errorf("clickhouse: rollback native transaction: %w", err), discardSession(n.conn))
		}
		n.settled = true
	}
	if err := n.conn.Close(); err != nil {
		return fmt.Errorf("clickhouse: release transaction session: %w", err)
	}
	return nil
}

// A failed boundary command can leave session state uncertain. Discarding its
// physical connection prevents another borrower inheriting that transaction.
func discardSession(conn *sql.Conn) error {
	err := conn.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		err = nil
	}
	closeErr := conn.Close()
	if errors.Is(closeErr, sql.ErrConnDone) {
		closeErr = nil
	}
	return errors.Join(err, closeErr)
}

func nativeStatementContext(ctx context.Context, implicit bool) context.Context {
	// SDK options can replace bound parameters or silently switch Exec to async.
	// The adapter owns its SQL protocol; ordinary context values still propagate.
	return clickhouse.Context(ctx, func(options *clickhouse.QueryOptions) error {
		*options = clickhouse.QueryOptions{}
		return nil
	}, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":         false,
		"implicit_transaction": implicit,
		"throw_on_unsupported_query_inside_transaction": true,
		"wait_changes_become_visible_after_commit_mode": "wait",
		"lightweight_deletes_sync":                      2,
		"apply_deleted_mask":                            true,
		"read_overflow_mode":                            "throw",
		"result_overflow_mode":                          "throw",
		"timeout_overflow_mode":                         "throw",
	}))
}
