// Package cockroachdb implements Core vector-store capabilities with CockroachDB
// VECTOR and native VECTOR INDEX over a host-owned pgx pool. CockroachDB v25.4 or
// newer is required. InitializeSchema optionally creates the current table.
//
// Current columns are id BYTES PRIMARY KEY, content STRING NOT NULL, metadata
// BYTES NOT NULL (under MetadataColumn), and embedding VECTOR(N) NOT NULL. Native
// schema inspection owns width; Dimensions only seeds a new column. Binary ID
// equality preserves Core identity, and metadata contains complete Core JSON
// bytes. Recreate former string-ID/JSONB tables and reindex authoritative data;
// no compatibility read or automatic alteration is provided.
//
// DistanceMetric selects a native distance operator and newly created vector
// index opclass. It does not override native width or an existing index. Core
// filter.Match alone selects metadata in a serializable source snapshot. Native
// row locks protect predicate deletion until commit; Index prepares all vectors
// and commits its whole request atomically. Serialization and native errors remain
// errors without Scope retries. Unfiltered Search ranks directly with native
// distance. Only filtered Search reads the complete source for Core selection.
// Both validate returned rows before MinScore and reject hybrid mode. Content
// cannot contain NUL; binary IDs and encoded metadata can.
// See the parent postgres package for the shared execution contract.
package cockroachdb
