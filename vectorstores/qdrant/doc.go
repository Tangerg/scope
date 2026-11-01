// Package qdrant stores and ranks precomputed coordinates in one concrete native
// collection. Provision one unnamed dense float32 vector and place its immutable
// encoder space ID in the collection metadata under SpaceMetadataKey. Width,
// metric and space are checked against the native collection; the Host must
// coordinate reprovisioning because schema checks and point RPCs are separate.
//
// A complete point owns its native ID, text and lossless metadata_json. The
// metadata_terms keyword array is derived in the same write and has no independent
// write API. Provision a keyword payload index on metadata_terms when required by
// native indexing policy. Previously stored two-field payloads need reindexing.
//
// Top-level string equality/inequality, null tests and their Boolean combinations
// are compiled into native conditions before global TopK. Scalar strings remain
// distinct from arrays, missing keys and null. Other expressions (including
// numeric ordering and positional array access) return vectorstore.ErrUnsupportedFilter;
// neither Query nor DeleteWhere enumerates the collection. Returned rows are
// checked against canonical metadata rather than filtered after TopK. Writes
// validate the entire batch, request native wait-for-application and preserve
// unconfirmed identities if a definitive acknowledgment is lost. The Host owns
// the client and controls direct access to this physical collection.
package qdrant
