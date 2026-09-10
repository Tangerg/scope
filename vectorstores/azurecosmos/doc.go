// Package azurecosmos exposes Azure Cosmos DB for NoSQL's
// VectorDistance() function through the Core vector-store capability interfaces. Documents are
// regular Cosmos items (`{id, partition_key, content, metadata, embedding}`) and
// retrieval runs a parameterised SQL query that orders rows by
// VectorDistance.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
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
// Container agreement. [NewStore] reads the container and refuses one it
// cannot serve with [ErrIncompatibleContainer]: the partition-key path has to
// be /<PartitionKeyField>, and the vector embedding policy has to declare
// /<EmbeddingField> with the configured [StoreConfig.DistanceFunction].
// VectorDistance() answers with a raw number and nothing that identifies the
// function behind it, so a disagreement there would rescale every score rather
// than fail, leaving MinScore to filter by a threshold in the wrong scale.
// Dimensions are not compared: this store declares none, and Cosmos rejects a
// vector of the wrong width on write.
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
// Null tests emit `NOT IS_DEFINED(<path>) OR IS_NULL(<path>)`. IS_NULL alone
// is not enough: the documented example evaluates IS_NULL on an absent
// property to false, while the filter AST reads an absent key as nil and
// absent is the ordinary case for metadata. IS_DEFINED separates the states so
// the disjunction covers exactly the ones the AST calls null.
//
// See https://learn.microsoft.com/azure/cosmos-db/nosql/vector-search.
package azurecosmos
