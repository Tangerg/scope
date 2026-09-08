// Package azurecosmos exposes Azure Cosmos DB for NoSQL's
// VectorDistance() function through the Core vector-store capability interfaces. Documents are
// regular Cosmos items (`{id, partition_key, content, metadata, embedding}`) and
// retrieval runs a parameterised SQL query that orders rows by
// VectorDistance.
//
// Requirements: a Cosmos DB account with vector search enabled
// (currently a feature flag on the NoSQL API; opt in from the
// portal). The container needs a vector embedding policy + indexing
// policy that match the configured [DistanceFunction] and the
// embedding model's dimensionality — the store assumes this is
// provisioned out of band (ARM / Terraform / Portal).
//
// Distance functions: [DistanceCosine] / [DistanceDotProduct] /
// [DistanceEuclidean]. The value passed at query time MUST match
// what the container's vector policy declares.
//
// Filter visitor produces Cosmos SQL — `c.metadata.key = @p1`,
// `c.metadata.year >= @p1`, `c.metadata.tag IN (@p1, @p2)`. Named
// parameters (`@pN`) are used to match Cosmos SDK's QueryParameter
// shape. LIKE maps to `CONTAINS(c.metadata.key, @p)` — the leading
// / trailing `%` markers are stripped.
//
// A Store binds one logical partition through [StoreConfig.PartitionKey].
// The container's partition-key path must match /<PartitionKeyField>, which
// defaults to /partition_key. Writes include that value in every document;
// searches and filtered deletion use the same partition. The Go SDK cannot
// execute TOP/ORDER BY vector queries across partitions. Different partitions
// require separate Store values. The document ID always uses Cosmos' required
// "id" property, and configurable storage fields must be distinct.
//
// Completeness. The SDK pager decides when a query is exhausted, so neither
// search nor filtered deletion infers the end from a page's length. Filtered
// deletion enumerates every matching id before removing the first one, because
// a continuation belongs to the query that produced it. Neither Index nor
// DeleteWhere is atomic: both issue one item operation at a time, so a failure
// leaves the earlier ones applied and names the id that failed.
//
// See https://learn.microsoft.com/azure/cosmos-db/nosql/vector-search.
package azurecosmos
