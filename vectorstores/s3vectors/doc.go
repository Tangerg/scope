// Package s3vectors implements Core indexing, semantic search and explicit ID
// deletion with Amazon S3 Vectors. The host owns its AWS SDK client, credentials,
// retries, transport lifecycle and the provisioned vector bucket and index.
//
// NewStore reads GetIndex and binds the index's native name, bucket, float32 data
// type, dimensions and distance metric. StoreConfig has no competing dimension
// or metric setting. Native non-filterable metadata keys must be exactly
// scope_content and scope_metadata. The derived scope_id field stays filterable.
// Construction validates every existing record without invoking the embedding
// model; it therefore requires GetIndex, ListVectors and GetVectors permissions.
//
// Every native record has exactly three metadata strings: scope_content carries
// text, scope_metadata carries the complete Core JSON object or null, and scope_id
// projects the canonical vector key for native ID selection. The ID projection
// must match the key. Scope never interprets a user metadata field through native
// scalar, array or missing-value rules. Nil and empty metadata, arbitrary JSON
// numbers and nested objects survive the SDK boundary as encoded Core JSON.
// User facts may contain scope_* keys inside their Core metadata object.
//
// Index validates the whole request, including native key and metadata budgets,
// before embedding. It embeds every batch and validates native dimensions,
// finite float32 narrowing and nonzero cosine vectors before the first PutVectors
// call. Writes replace complete records and split at both the 500-vector and
// 20 MiB request limits. Native publication is not atomic across calls: a service
// failure can leave earlier prepared chunks stored. Scope adds no retry loop.
//
// Search scans all native records and evaluates Core predicates through
// filter.Match before invoking the model or selecting TopK. It retains only
// matching keys, then queries native ANN using scope_id/$in groups. Unfiltered
// search uses the same native ranking operation. No local distance computation,
// provider predicate compiler or second ranking algorithm exists. Scope follows
// every native query continuation, validates all hits before thresholding and
// truncation, and sorts raw native distance with bytewise ID ties before score
// projection. TopK is bounded by the native limit of 10,000.
//
// Cosine distance projects with Core ScoreFromCosineDistance; Euclidean distance
// projects with ScoreFromDistance. Native query metrics must match the bound
// index. Non-finite or invalid distances, repeated keys or tokens, corrupt current
// records and changed predicate membership return errors and no partial response.
// Search requires ListVectors, GetVectors and QueryVectors permissions and reads
// the full index. Native listing and ANN calls provide no common snapshot; callers
// requiring a stable dataset must coordinate concurrent external writes.
//
// DeleteIDs validates and deduplicates all requested keys before any deletion,
// then uses native batches of at most 500. DeleteWhere is absent because native
// DeleteVectors cannot condition a deletion on the metadata observed by a scan.
// A read-then-delete approximation would delete a newly changed nonmatching row.
//
// Migration replaces the contract: remove DistanceMetric and its public enum,
// remove DeleteWhere consumers, and provision an index with the exact metadata
// configuration above. Native index dimensions, distance and non-filterable keys
// are immutable. Rebuild records from authoritative source documents into the
// current three-string representation; legacy records are rejected. No dual
// reads, compatibility adapter, schema version or migration registry is retained.
//
// Native integration requires SCOPE_S3_VECTORS_BUCKET, AWS_REGION and credentials
// from the AWS SDK default chain. It creates unique test indexes in that bucket,
// deletes only those indexes, and leaves the bucket intact. Missing setup fails.
//
// See https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-vectors-indexes.html
// and https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-vectors-limitations.html.
package s3vectors
