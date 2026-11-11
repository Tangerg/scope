// Package elastic owns the Elasticsearch and OpenSearch vector-store protocol:
// Store and its record layout, namespace declaration, result window, bulk
// writes, scrolls and ranking. An Engine supplies only what differs between the
// engines: SDK transport, embedding-field admission, vector constraints, score
// projection and nearest-neighbor syntax. This module imports neither native SDK.
//
// The host provisions the concrete index; Store never creates or modifies it.
// The mapping declares exactly content (text), the engine's embedding field and
// metadata_json (keyword with index:false and doc_values:false) under
// dynamic:strict, with complete stored _source. Aliases, required routing,
// runtime or derived fields and dynamic templates are incompatible. Each record's
// _source holds exactly those three fields and its native _id, limited to 512
// UTF-8 bytes, is its sole identity. Metadata is one Core JSON string rather
// than native mapped terms, so nil and empty metadata stay distinct and every
// Core value keeps its exact meaning. Media is unsupported.
//
// The mapping must bind _meta.scope_vector_space to the Host's immutable encoder
// space. Changing that declaration is a rebuild, not permission to reuse an old
// index. Store re-admits the mapping and settings before every mutation and
// query and refuses to proceed when the admitted Schema or result window
// changed; HTTP configuration checks and data requests remain distinct
// operations, so the Host must coordinate physical index replacement.
//
// Upsert checks every record's FLOAT32 representation before publishing NDJSON
// in independent MaxBulkBytes groups. Upsert, DeleteIDs and DeleteMatching share
// that encoded limit, including action framing and conditional-delete tokens,
// and admit every action before the first mutation. Successful ingestion is not
// a guarantee of search visibility.
//
// Query only sends native nearest-neighbor searches. Metadata is opaque to the
// engine, so a filtered Query returns vectorstore.ErrUnsupportedFilter before
// any request. A Host with authority over the entire physical index can call
// QueryByScan, which selects candidates through a budgeted Scroll, ranks each
// candidate group natively, and orders merged groups by native score before
// Core normalization so saturated scores do not change ranking. Exceeding the
// scan budget returns no partial ranking. Selection and
// ranking are separate reads, not a cross-request snapshot. DeleteMatching
// completes selection first and deletes each record only at the sequence number
// and primary term it was selected at; a conflict is an error, never a blind
// identity deletion. DeleteIDs expresses unconditional identity intent.
//
// Client bounds every response before buffering and preserves received bytes
// together with read and closure errors. BulkPlan checks the entire encoded
// mutation before any publication, partitions it by bytes and action count, and
// keeps acknowledged, rejected, uncertain and unattempted IDs distinct. A valid
// receipt cannot erase a transport error, and a contradictory summary cannot
// establish per-document success.
//
// Scroll validates complete shard acknowledgments, exact totals, unique document
// identities and cumulative scan budgets. Every observed cursor is eligible for
// cleanup even when its page was truncated. Cleanup errors invalidate the scan;
// cancellation does not relinquish ownership of the server cursor.
package elastic
