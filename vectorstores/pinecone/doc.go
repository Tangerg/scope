// Package pinecone stores and ranks precomputed float32 vectors under an
// explicit index-space binding. Provision a ready dense serverless index with
// the SpaceTag tag; the adapter verifies its space, host, dimensions and metric
// against the native control plane. A model or document batcher is not a store
// dependency. The Host must coordinate reprovisioning across control/data RPCs.
//
// Each upsert writes the original content, the lossless metadata_json, and the
// scalar keyword projection metadata_terms together. Reindex older two-field
// records. Query and DeleteWhere support native top-level scalar string equality;
// all other filters fail explicitly, without listing or fetching the namespace.
// No cross-RPC snapshot or search-visibility guarantee is implied. Native
// service limits and errors remain visible.
//
// Upsert validates all records before publishing any. Its native groups each
// contain at most MaxVectorsPerUpsert entries. On failure vectorstore.WriteError
// distinguishes acknowledged IDs, unconfirmed IDs with a possible count-only
// acknowledgment, and unattempted groups. It never guesses which IDs in a
// partial acknowledgment succeeded. Native acknowledgments do not imply that
// an eventually consistent query can already see a record.
//
// The constructor opens and owns the data connection, not the supplied control
// client. Default tests run against offline protocol fixtures. Tagged tests
// need explicitly provisioned Local or cloud indexes with the requested space;
// their namespaces are isolated.
package pinecone
