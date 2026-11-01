// Package azurecosmos stores precomputed float32 vectors in one native Cosmos
// logical partition. The host supplies the SDK client and container. StoreConfig
// Space and PartitionKey map to the physical routing key Space.StorageKey() +
// ":" + PartitionKey: the same document ID in two equal-width vector spaces
// therefore names two distinct Cosmos items. The prefix counts toward the
// container's 101- or 2048-byte partition-key limit. Reindex old unprefixed
// partitions explicitly. This routing boundary does not restrict other native
// writers who can access the same physical partition.
//
// Construction and each data operation read the native container policy. The
// /embedding path owns float32 widths (1..4096) and native metric; /partition_key
// must be a single Hash partition key. Input and readback reject nonfinite
// coordinates and cosine vectors that are zero after narrowing. The strict
// record carries a single Core metadata JSON string to preserve exact numbers.
// The container is host-owned and must not be administratively replaced while
// operations are running.
//
// Query issues a parameterized native VectorDistance TOP query and orders by
// raw score, including when different native scores have the same projected
// value. Filtered queries fail with ErrUnsupportedFilter, without enumerating
// the partition. Scan explicitly grants a record, raw item-byte and time budget
// and charges every item, including nonmatches; errors return no partial
// results. Consistency follows the SDK/session and separate continuation pages
// do not form an atomic snapshot. SDK transport buffers one native page before
// individual items can be charged.
//
// Upsert prepares all items (each at most 2 MiB) before the first write.
// DeleteIDs expresses unconditional key intent and ignores missing keys;
// DeleteMatching requires a predicate, completes a bounded Scan, then uses each
// observed ETag as If-Match. A replacement cannot be deleted by a stale scan.
// Individual requests are independent: WriteError records acknowledged,
// rejected, unknown and unattempted IDs; retries and client lifetime belong to
// the host. Live tests require an isolated vector-capable Azure Cosmos database;
// see store_integration_test.go for the explicit service configuration.
package azurecosmos
