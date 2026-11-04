// Package cassandra stores precomputed vectors in an exclusively owned
// Cassandra table. NewStore verifies its Paxos protocol and SpaceID binding in
// the table comment, native vector<float,N> width and single-column primary key.
// Set InitializeSchema and CreateDimensions to provision a new bound table;
// the constructor never changes a pre-existing table's space.
//
// All document mutations use QUORUM commits, SERIAL Paxos and server timestamps.
// Ordinary CQL writes cannot share this table: a future client timestamp can
// override a conditional deletion even when Cassandra reports it applied. Tables
// written by the former ordinary-write protocol are rejected by their native
// table binding and must be recreated. Changing that marker alone cannot repair
// old timestamp history or authorize ordinary writes to a current table.
//
// Upsert validates every input and native metric before the first mutation.
// A row is inserted conditionally or replaced conditionally if already present.
// Deletion between these proposals is a known rejection; the adapter never retries
// to resurrect it. WriteError retains the acknowledged prefix, rejected or
// uncertain row, and unattempted suffix.
//
// This schema has no vector index. QueryByScan and DeleteMatching require an explicit
// record, byte and time grant. The scan charges every observed row, including
// nonmatches, for its ID, content, original metadata JSON and float32 vector.
// It decodes exact metadata, applies Core filter semantics before scoring and
// emits no partial top-K. Cassandra has no collection snapshot. Conditional
// deletion compares the captured columns and cannot delete a replacement.
// DeleteIDs does not scan, and a missing identity is an idempotent success.
// The host owns its session and retry policy.
package cassandra
