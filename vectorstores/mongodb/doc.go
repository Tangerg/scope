// Package mongodb stores prepared vectors in a host-owned MongoDB Vector Search
// collection. The Host provisions and owns the driver, collection and indexes.
// NewStore and each data operation read the named READY, queryable vectorSearch
// definition. Its one vector field must be SpaceID.StorageKey(); a different
// space of the same dimension cannot bind the index. The native definition
// supplies the width and similarity policy, and must index _id as a filter.
//
// Upsert validates every FLOAT32 vector before submitting one ordered BulkWrite.
// Replacements require both the string _id and the space-specific vector field.
// A conflicting identity in another space cannot overwrite that record.
// Uncertain delivery, rejected writes and ordered unattempted tails remain
// distinguishable through vectorstore.WriteError. An acknowledged write does not
// guarantee visibility in the asynchronous vector index. No operation embeds text.
//
// Core metadata is stored losslessly in metadata_json. metadata_terms is an
// atomic derived projection of those bytes. Query uses native vector ranking;
// scalar top-level string equality requires metadata_terms in the vector index.
// Unsupported filters fail without enumerating documents. DeleteWhere uses an
// indexed term under binary collation and requires a matching ordinary ascending
// metadata_terms index; its hint prevents a silent full collection scan.
//
// Scan and DeleteMatching are explicit alternatives for arbitrary Core predicates.
// Their budget covers source BSON bytes and records, including nonmatches, and
// elapsed time. Server aggregation admits at most MaxRecords+1 candidates and
// withholds payloads beyond the grant. Budget exhaustion returns no partial
// search result and happens before any delete. The ranking pass validates current
// metadata; conditional deletion compares observed metadata under binary collation.
// The separate selection and action have no shared snapshot. Identical metadata
// does not prove a record revision, and concurrent new matches may be missed.
//
// Ordinary tests are offline. Integration tests require SCOPE_MONGODB_URI on a
// deployment with Vector Search; fixtures provision and remove isolated databases.
package mongodb
