// Package typesense ranks precomputed vectors in a provisioned Typesense
// collection. Include SpaceField(space) beside the stored content, opaque
// metadata and cosine embedding fields. An index without this exact marker
// must be rebuilt; native automatic embedding is rejected.
//
// Query uses native semantic ranking or native lexical/vector fusion. Supply
// lexical text explicitly in hybrid mode. Ordinary queries do not export the
// collection. Native filters on opaque Core JSON cannot guarantee equivalent
// results; Query therefore rejects them before model or provider I/O. QueryByScan
// instead requires explicit authority and a record, byte and time budget.
// The export and ranking calls do not share snapshot isolation.
//
// Upsert preflights the complete batch before importing. It requests native
// IDs in each acknowledgment, validates them against the actual submitted
// keys, and keeps accepted, rejected and uncertain records distinct. An
// acknowledgment does not imply that a read replica can search the write.
package typesense
