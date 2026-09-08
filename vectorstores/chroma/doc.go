// Package chroma exposes Chroma through the Core vector-store capability interfaces. Documents
// are stored as records inside a Chroma collection
// (`{id, document, embedding, metadata}`); retrieval runs the
// collection's nearest-neighbor query.
//
// Requirements: a reachable Chroma server (self-hosted or Chroma
// Cloud). The store uses the official Go client over HTTP.
//
// Vector similarity functions: cosine / L2 / inner-product. The
// chosen value is recorded in the collection metadata at creation
// time and cannot be changed without rebuilding the collection.
//
// Filter visitor produces Chroma's flat where-clause syntax —
// `{"$and": [...]}`, `{"author": {"$eq": "Alice"}}`,
// `{"$contains": "..."}` for LIKE. The result feeds the `where`
// field on the query call. Metadata fields are addressed at the top
// level (no `metadata.` prefix); Chroma stores metadata flat.
//
// Write and delete evidence. Chroma answers an upsert with a status alone, so
// a batch is either accepted whole or reported as an error — there is no
// per-item result to reconcile. A delete carrying neither ids nor a where
// clause selects the entire collection, so DeleteWhere refuses a filter that
// compiles to nothing rather than sending an unfiltered request.
//
// Null tests are refused. Chroma's where clause offers $eq, $ne, $gt, $gte,
// $lt, $lte, $in, $nin, $contains and $not_contains, none of which asks
// whether a key is present, so an IS NULL filter fails rather than being
// approximated.
//
// See https://docs.trychroma.com/ for the full API surface.
package chroma
