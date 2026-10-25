// Package pgvector implements Core vector-store capabilities with PostgreSQL's
// pgvector extension. The host owns the pgx pool. InitializeSchema optionally
// provisions the extension, schema, four current columns, and selected ANN index.
//
// Current columns are id BYTEA PRIMARY KEY, content TEXT NOT NULL, metadata
// BYTEA NOT NULL (under MetadataColumn), and embedding VECTOR(N) NOT NULL. Native
// schema inspection establishes width. Dimensions only seeds a new column.
// Binary ID equality preserves Core identity; metadata stores complete Core JSON
// bytes. Recreate former text-ID/JSONB tables and reindex authoritative documents.
// No compatibility adapter or automatic schema change is provided.
//
// postgres.DistanceCosine selects <=>, postgres.DistanceL2 selects <->, and
// postgres.DistanceIP selects <#>.
// IndexHNSW and IndexIVFFlat create native indexes with the selected opclass;
// IndexNone uses native exact scanning. DistanceMetric is a query choice. It does
// not override native schema or index configuration.
//
// Core filter.Match selects metadata in a serializable source snapshot. Predicate
// deletion holds native row locks through commit. Index prepares every vector and
// commits the whole request atomically; conflicts and native failures are errors.
// Unfiltered Search ranks directly with native distance. Filtered Search ranks
// the selected binary IDs; its full-source read preserves Core semantics rather
// than using SQL metadata indexes. Both validate returned rows before MinScore
// and reject hybrid mode. Content cannot contain NUL; binary identity and encoded
// metadata can. See the parent postgres package for shared guarantees.
//
// Native extension contracts: https://github.com/pgvector/pgvector.
package pgvector
