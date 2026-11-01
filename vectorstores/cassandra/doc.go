// Package cassandra stores precomputed vectors in an exclusively owned
// Cassandra table. NewStore verifies its physical SpaceID binding in the table
// comment, native vector<float,N> width and single-column primary key.
// Set InitializeSchema and CreateDimensions to provision a new bound table;
// the constructor never changes a pre-existing table's space.
//
// Upsert validates every input and native metric before the first mutation.
// Each successful INSERT is acknowledged independently; WriteError distinguishes
// the acknowledged prefix, uncertain native write and unattempted suffix.
//
// This schema has no vector index. Scan and DeleteMatching require an explicit
// record, byte and time grant. The scan charges every observed row, including
// nonmatches, for its ID, content, original metadata JSON and float32 vector.
// It decodes exact metadata, applies Core filter semantics before scoring and
// emits no partial top-K. Cassandra has no collection snapshot. Conditional
// deletion compares the captured columns and cannot delete a replacement.
// DeleteIDs does not scan. The host owns its session and retry policy.
package cassandra
