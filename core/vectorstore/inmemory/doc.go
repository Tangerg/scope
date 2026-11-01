// Package inmemory implements exact vector search over process-local records.
// Upsert and Query accept precomputed coordinates in the configured space.
// Query evaluates metadata before scoring, scans all records under a read
// lock, and orders score ties by document ID. The Store owns no model or I/O.
package inmemory
