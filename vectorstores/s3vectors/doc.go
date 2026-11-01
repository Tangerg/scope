// Package s3vectors stores precomputed vectors in an explicitly bound Amazon S3
// vector index. Provision it using NonFilterableKeys(space). Key, dimensions,
// metric and coordinate-space schema are verified; an existing index without
// the exact marker must be rebuilt. The Host owns credentials and index lifecycle.
//
// Query ranks native results without listing any records. The metadata payload
// preserves Core JSON exactly but is not searchable by equivalent native
// predicates. Use Scan only when the caller is authorized to read the entire
// index. Scan is bounded and complete on success but pagination is not a snapshot.
//
// Writes preflight the complete batch, including float32 narrowing and native
// request limits, before the first put. A failed group reports acknowledgments,
// unconfirmed submissions and unattempted keys separately. A successful write
// does not imply immediate search visibility.
package s3vectors
