// Package chroma adapts a host-owned Chroma Collection to Core vector-store
// capabilities. The host creates or resolves the native collection, configures
// its index, authentication and retry policy, and closes its SDK handles.
// Scope supplies embeddings explicitly and creates no SDK embedding function.
//
// StoreConfig contains Collection, EmbeddingModel and DocumentBatcher. The
// native #embedding schema must contain an enabled vector index with an
// explicit cosine, l2 or ip space. Construction reads that schema and verifies
// every stored record. Collection metadata and a second metric configuration
// never select scoring. Chroma 1.5.5 exercises this contract in native tests.
// A native SDK collection satisfies Collection directly:
//
//	collection, err := client.GetCollection(ctx, "documents")
//	if err != nil { return err }
//	defer collection.Close()
//	store, err := chroma.NewStore(ctx, chroma.StoreConfig{
//		Collection: collection, EmbeddingModel: model, DocumentBatcher: batcher,
//	})
//
// Each native record contains its original ID, text and float32 embedding.
// Native metadata contains exactly one string, scope_metadata, holding the
// complete Core metadata.Map JSON. Core's codec owns nil versus {}, explicit
// null, nested JSON and exact numeric values. Core filter.Match alone owns
// predicate membership and type errors. Unknown native metadata fields and
// malformed records fail the current contract. Media fails before indexing I/O.
//
// Search enumerates the observed collection with paginated Get operations and
// validates every record before query embedding, relevance caps or thresholds.
// Get is exhaustive; an ANN query never enumerates a predicate. Search limits
// native Query to the selected IDs and requests up to TopK candidates. ANN
// remains approximate and may omit eligible records. Native responses must
// contain consistent columns, unique eligible IDs and valid records and scores.
// Core rechecks membership on current query records. Pagination and query calls
// do not form a collection snapshot: newly inserted records are outside the
// observed set, and replacements or removals can change returned candidates.
//
// Native distances determine ordering, with document ID byte order for ties
// among returned candidates. Core converts cosine, squared L2 or
// 1-inner-product distance to Score; MinScore applies after the complete native
// response has been validated. Raw distances preserve ordering when Score
// saturates. Scanning retains selected IDs in memory and increases search cost.
//
// Store provides explicit DeleteIDs and does not implement FilterDeleter.
// Chroma's filtered delete first reads IDs and then appends unconditional
// deletion records to its log; it cannot protect concurrent replacement with
// a native conditional write. A private lock or extra version field cannot
// repair that missing guarantee. Select a backend with an atomic predicate or
// conditional deletion when that capability is required.
//
// Index prepares every model output, codec value and vector before its first
// upsert, so validation failure publishes no earlier batch. Successful earlier
// upserts can precede a later transport failure or cancellation; there is no
// transaction across batches. Native SDK errors retain the caller context's
// cancellation cause at the Scope boundary.
//
// Breaking replacement: Client, CollectionName, InitializeSchema, DistanceMetric,
// Store.Close and Store.DeleteWhere are removed. Inject the native Collection
// and retain lifecycle ownership in the host. Rebuild old flat-metadata records
// from source documents in an exclusive collection. No old-format reader or
// compatibility shim remains.
package chroma
