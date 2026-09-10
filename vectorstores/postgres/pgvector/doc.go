// Package pgvector implements vector-store capabilities with the pgvector
// PostgreSQL extension. Documents live in a regular PostgreSQL table with a
// typed `vector(N)` column; metadata is stored in `jsonb`.
// Documents containing media are rejected before embedding or writes; this
// store persists text and metadata only. Index batches commit independently,
// so an error can leave earlier batches stored.
//
// Requirements: PostgreSQL 13+ with the `vector` extension installed
// (the store runs `CREATE EXTENSION IF NOT EXISTS vector` under
// [StoreConfig.InitializeSchema] = true).
//
// Distance metrics — three of pgvector's six operators are exposed:
//
//   - [DistanceCosine] (`<=>`) — cosine distance, `vector_cosine_ops`
//   - [DistanceL2]     (`<->`) — Euclidean distance, `vector_l2_ops`
//   - [DistanceIP]     (`<#>`) — negative inner product, `vector_ip_ops`
//
// Index types: HNSW (default, best query perf) / IVFFlat (faster
// builds) / none (exact sequential-scan). Vector binding uses
// pgvector-go's typed [pgvec.Vector]; the connection is a standard
// pgx pool.
//
// Metadata filters compile to parameterized SQL; values flow through `$N`
// placeholders, while JSON numbers and booleans use explicit PostgreSQL casts.
//
// See https://github.com/pgvector/pgvector for the extension docs.
package pgvector
