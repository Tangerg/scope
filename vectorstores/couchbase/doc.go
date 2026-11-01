// Package couchbase stores precomputed vectors in an exclusively bound Couchbase
// collection. StoreConfig.CollectionName derives the physical collection from
// the declared encoder space and coordinate width. The host provisions this
// named collection in a chosen bucket and scope; InitializeSchema creates only
// its primary query index. Construction validates SQL++ VECTOR_DISTANCE.
//
// Upsert encodes the entire batch before the first native KV call and reports
// evidence for acknowledged, uncertain and unattempted IDs on failure. KV keys
// preserve document IDs up to the named-collection limit of 246 bytes.
//
// A primary index is not a vector index. Only Scan and DeleteMatching read the
// complete collection. Each requires explicit record, byte and time limits.
// Scan charges each native JSON result row (including CAS and all properties)
// before decoding or filtering it. Request-plus reads capture records but do
// not provide a collection-wide snapshot. Rankings use the captured vectors
// and native distances; conditional deletion verifies each captured CAS.
// The host owns the SDK cluster. DeleteIDs is an independent KV operation.
package couchbase
