// Package postgres is a history Store backed by PostgreSQL via pgx.
//
// Each conversation's messages live in a single table; messages are
// serialized to JSONB through the shared tagged core/chat wire codec, so
// ordered parts, tool results, media, and metadata round-trip with full
// fidelity. The package reads and writes only the current tagged format.
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
