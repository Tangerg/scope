// Package postgres is a history Store backed by PostgreSQL via pgx.
//
// Each conversation's messages live in a single table; messages are
// serialized to JSONB through the shared tagged core/chat wire codec, so
// ordered parts, tool results, media, and metadata round-trip with full
// fidelity. The package reads and writes only the current tagged format.
//
// Schema (created by InitializeSchema=true):
//
//	CREATE TABLE <schema>.<table> (
//	    seq             BIGSERIAL    PRIMARY KEY,
//	    conversation_id TEXT         NOT NULL,
//	    message         JSONB        NOT NULL,
//	    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
//	);
//	CREATE INDEX <index> ON <schema>.<table> (conversation_id, seq);
//
// Reads order by `seq`, which PostgreSQL assigns from the table's own
// sequence. That is why this store needs no [history.Sequence]: no clock takes
// part in the ordering, so there is no clock regression to guard against.
// Concurrent calls and writes from distinct Store instances have no defined
// relative order.
//
// Write atomicity. One Write is one pgx batch, and pgx runs "all queries ...
// in an implicit transaction unless explicit transaction control statements
// are executed", so a Write applies whole or not at all. A transport error
// can hide a committed transaction, so execution failures return an uncertain
// WriteOutcome rather than asserting that nothing was written.
//
// Example:
//
//	pool, _ := pgxpool.New(ctx, "postgres://...")
//	store, _ := postgres.NewStore(ctx, postgres.StoreConfig{
//	    Pool:             pool,
//	    InitializeSchema: true, // create the table+index on first use
//	})
//	defer pool.Close()
package postgres
