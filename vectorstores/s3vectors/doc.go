// Package s3vectors exposes AWS S3 Vectors through the Core vector-store capability interfaces.
// S3 Vectors is a purpose-built, fully managed vector storage tier
// that lives next to regular S3 buckets — vectors live in a *vector
// bucket* under a typed *vector index*.
//
// Requirements: an AWS account with S3 Vectors enabled (currently
// available in a subset of regions), a vector bucket + index
// provisioned out of band (ARM / Terraform / CloudFormation / SDK
// control plane), and an aws-sdk-go-v2 s3vectors client. The store
// does NOT create indexes — index dimensionality / metric / metadata
// schema are declared at index creation.
//
// Distance metrics. S3 Vectors indexes are registered with one of `cosine` or
// `euclidean` at creation, and [StoreConfig.DistanceMetric] is what maps
// QueryVectors' raw distance into a higher-is-better score in [0, 1]. Because
// the response carries the distance and nothing that identifies the metric
// behind it, a mismatch would rescale every score and leave MinScore filtering
// by a threshold in the wrong scale, so [NewStore] reads the index's registered
// metric and refuses a configured value that disagrees with
// [ErrIncompatibleIndex].
//
// That read is GetIndex, and s3vectors:GetIndex is its own action: granting
// PutVectors and QueryVectors does not imply it, so an IAM policy scoped to the
// data plane alone has to add it at the index ARN. Dimensionality is not
// compared: this store declares none, and S3 Vectors rejects a wrong-width
// vector on write.
//
// Filter visitor produces S3 Vectors' Mongo-flavored JSON filter
// document — `{"author": {"$eq": "Alice"}}`,
// `{"year": {"$gte": 2020}}`,
// `{"$and": [...]}`, `{"$not": {...}}`. Metadata keys are addressed
// flat (no nested-path support).
//
// Batching. PutVectors caps at 500 vectors per request; the document
// batcher should produce shards smaller than that. The store passes
// each shard through as one PutVectors call.
//
// Delete. S3 Vectors has no filter-based DeleteVectors, and QueryVectors is an
// approximate nearest-neighbor search that answers with up to topK candidates
// rather than every match — it cannot enumerate a filter. The store therefore
// walks the index with ListVectors, which is exhaustive and key-paginated, and
// decides membership with the shared client-side evaluation in
// [github.com/Tangerg/scope/core/vectorstore/filter.Match]. Listing completes
// before anything is deleted, so pagination never observes its own mutations.
// Filtered deletion needs s3vectors:GetVectors alongside
// s3vectors:ListVectors, because membership reads each vector's metadata.
//
// Null tests emit $exists, which S3 Vectors documents as checking whether the
// key is present "regardless of the value that's stored". Filterable metadata
// holds strings, numbers, booleans and lists and cannot hold null, so an
// absent key is the only null-ish state.
//
// LIKE is refused by the query filter, whose documented operator set has no
// pattern match. Filtered deletion is unaffected: it enumerates with
// ListVectors and decides membership with filter.Match, so the same filter
// that Search rejects will delete correctly.
//
// Operation limits. A query ranks at most [MaxTopK] results and one
// QueryVectors response carries at most [MaxResultsPerQueryPage] of them, so
// Search follows the continuation token until the ranked run is complete
// rather than treating one page as the answer. PutVectors and DeleteVectors
// each carry at most [MaxVectorsPerWrite] vectors, so Index and DeleteIDs
// split at that bound instead of leaving a documented provider limit to the
// caller's batcher.
//
// See https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-vectors.html.
// Metadata numbers are encoded with the SDK's arbitrary-precision Smithy number
// representation. The service remains responsible for its storage limits.
package s3vectors
