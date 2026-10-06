// Package postgres is a history Store backed by PostgreSQL via pgx.
//
// Each conversation's messages live in a single table; messages are
// serialized to BYTEA through the shared tagged core/chat wire codec, so
// ordered parts, tool results, media, and metadata round-trip with full
// fidelity. The package reads and writes only the current tagged format.
// Core owns message encoding; PostgreSQL never parses or normalizes it.
// Construction verifies the message column even without initialization.
// Existing JSONB tables are rejected and must be rebuilt from a trusted
// source; values already normalized by JSONB cannot be reconstructed here.
//
// Schema (created by InitializeSchema=true):
//
//	CREATE TABLE <schema>.<table> (
//	    seq             BIGSERIAL    PRIMARY KEY,
//	    conversation_id TEXT         NOT NULL,
//	    message         BYTEA        NOT NULL,
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
// One Write is one explicit transaction. Every INSERT must acknowledge one
// row before the transaction commits; a missing acknowledgment or execution
// failure rolls back the complete batch. Failures before commit report zero
// accepted messages. A lost commit acknowledgment returns an uncertain
// WriteOutcome because the transaction may already have committed.
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
