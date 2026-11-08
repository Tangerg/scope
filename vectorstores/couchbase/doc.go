// Package couchbase stores precomputed vectors in an exclusively bound Couchbase
// collection. StoreConfig.CollectionName derives the physical collection from
// the declared encoder space and coordinate width. The host provisions this
// named collection in a chosen bucket and scope; InitializeSchema creates only
// its primary query index. Construction validates SQL++ VECTOR_DISTANCE.
//
// Upsert admits and encodes the entire batch before the first native KV call.
// Coordinates retain their float64 values within the native float32 range;
// cosine additionally requires a positive float64 squared norm. Writes, queries
// and stored records share those constraints because VECTOR_DISTANCE otherwise
// returns NULL. Each complete uncompressed JSON value must fit 20 MiB, including
// metadata, coordinates and escaping. KV keys preserve document IDs up to the
// named-collection limit of 246 bytes. A write failure reports evidence for
// acknowledged, uncertain and unattempted IDs.
//
// A primary index is not a vector index. Only QueryByScan and DeleteMatching read the
// complete collection. Each requires explicit record, byte and time limits.
// QueryByScan charges each native JSON result row (including CAS and all properties)
// before decoding or filtering it. Request-plus reads capture records but do
// not provide a collection-wide snapshot. Rankings use the captured vectors
// and native distances; conditional deletion verifies each captured CAS.
// The host owns the SDK cluster. DeleteIDs is an independent KV operation.
package couchbase
