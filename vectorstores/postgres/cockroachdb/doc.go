// Package cockroachdb stores and ranks precomputed vectors for an explicitly Host-bound
// vector space. It does not own an embedding model or the connection pool.
// Metadata is preserved as opaque bytes; filtered native queries return
// vectorstore.ErrUnsupportedFilter before querying rows.
package cockroachdb
