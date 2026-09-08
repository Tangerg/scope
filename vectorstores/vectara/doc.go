// Package vectara exposes Vectara's managed RAG service
// through the Core vector-store capability interfaces. Vectara handles embedding, chunking, and
// retrieval internally — the store sends raw text to the v2 API and
// does NOT need an [embedding.Model]. This is unlike every other
// scope vector store.
//
// Requirements: a Vectara account, an API key with corpus-level
// write + query scope, and a corpus provisioned via the Vectara
// console or control-plane API. The embedder, retrieval model, and
// chunking strategy are configured on the corpus itself.
//
// Authentication. API key via the `x-api-key` header.
//
// Search shape. The store hits Vectara's v2 query endpoint —
// `POST /v2/corpora/<corpus_key>/query` — with the user's raw query
// and a `metadata_filter` string derived from the filter visitor.
// Scores come from Vectara directly (higher = more similar).
//
// Filter visitor produces Vectara's metadata-filter SQL-like syntax
// — `doc.author = 'Alice'`, `doc.year >= 2020`, `doc.tag IN ('a',
// 'b')`, `NOT (...)`, ` AND ` / ` OR `. Metadata keys are addressed
// under the `doc.` prefix by default; pass [StoreConfig.MetadataPrefix]
// = `"part"` to filter part-level metadata instead.
//
// Documents are uploaded as `type: "core"` with a single
// `document_parts` entry holding the raw text — Vectara does its own
// chunking on the server side.
//
// Delete. Vectara has no bulk filter-delete; the store enumerates
// matching ids via the list endpoint (paged via `page_key`) and
// issues per-id DELETEs against `/v2/corpora/<corpus_key>/documents/
// <doc_id>`.
//
// A missing `page_key` is the only evidence the listing is complete, so a page
// holding fewer documents than the requested limit — or none at all — does not
// end the walk. The full id set is collected before the first DELETE, because a
// page key belongs to the listing that produced it and deleting mid-walk would
// resume through a corpus that has already changed. Deletion itself is not
// atomic: a failure leaves the earlier documents deleted and names the id that
// failed, and repeating the call finishes the rest.
//
// Null tests emit `IS NULL`, which Vectara documents as checking "whether or
// not a value is NULL (empty or missing)" — the same pair of states the filter
// AST reads as nil. HAS is refused because filterable metadata fields are
// scalar.
//
// See https://docs.vectara.com/docs/rest-api/.
package vectara
