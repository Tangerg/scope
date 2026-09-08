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
// Distance metrics. S3 Vectors indexes are registered with one of
// `cosine` or `euclidean` at creation. [StoreConfig.DistanceMetric]
// must match the index's registered metric — the store uses it to
// map QueryVectors' raw distance into a higher-is-better score in
// [0, 1].
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
// See https://docs.aws.amazon.com/AmazonS3/latest/userguide/s3-vectors.html.
package s3vectors
