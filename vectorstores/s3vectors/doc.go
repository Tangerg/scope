// Package s3vectors exposes AWS S3 Vectors through the Core vector-store
// capability interfaces. Documents store text in the reserved scope_content
// metadata key. Documents containing media are rejected before indexing I/O.
//
// Provision the vector bucket and index out of band. NewStore reads GetIndex
// and checks StoreConfig.DistanceMetric against the index configuration;
// s3vectors:GetIndex permission is required. Index dimensions remain owned by
// the service and are checked there when vectors are written.
//
// Search without a filter uses the native approximate QueryVectors operation.
// Filtered search lists all metadata and vector data with ListVectors, evaluates
// every predicate through Core filter.Match, and ranks every matching vector
// before applying TopK. S3's native equality also matches array members, and
// QueryVectors cannot restrict results by vector key, so a native metadata
// filter cannot preserve Core's scalar, array, null, nested-path and LIKE
// semantics. Filtered search therefore requires s3vectors:ListVectors and
// s3vectors:GetVectors and reads the entire index on each call.
//
// Local ranking uses the index's cosine distance or Euclidean straight-line
// distance. Raw distance owns ordering; normalized scores are projections.
// Equal distances are ordered by document ID. Invalid vector dimensions or
// non-finite vector data return errors. Scores use the same mappings as native
// search, and MinScore is applied before the final TopK limit.
//
// DeleteWhere uses the same complete metadata enumeration and Core membership
// evaluation. Listing and predicate evaluation finish before DeleteVectors
// starts. Listing, metadata or predicate errors are returned, without treating
// a failed scan as an empty match set. Filtered deletion also needs ListVectors
// and GetVectors permissions. Concurrent writes are not isolated by listing;
// callers requiring a consistent snapshot must coordinate writes externally.
//
// Search accepts at most MaxTopK results. Native query pages carry at most
// MaxResultsPerQueryPage hits, and Search follows their continuation tokens.
// Index and DeleteIDs split writes at MaxVectorsPerWrite. A short or empty
// ListVectors page does not end enumeration when it has a continuation token.
//
// Metadata numbers retain their arbitrary-precision JSON representation across
// Smithy encoding and Core evaluation. The service owns its storage limits.
//
// See https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-vectors-indexes.html
// and https://docs.aws.amazon.com/AmazonS3/latest/API/API_S3VectorBuckets_ListVectors.html.
package s3vectors
