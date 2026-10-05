// Package vespa exposes Yahoo Vespa's vector search
// through the Core vector-store capability interfaces. Documents are regular Vespa documents in a
// schema with content and embedding (tensor) fields plus any
// metadata attributes and the Scope storage fields described below, reached over
// the HTTP Document and Search REST APIs.
// Documents containing media are rejected before indexing I/O because this
// adapter persists document text and metadata only.
//
// Requirements: a Vespa application (Vespa Cloud or self-hosted) with
// a schema (.sd file) declaring the embedding tensor field and any
// metadata attributes the filter visitor will address. scope_namespace must use
// attribute and summary indexing with exact, cased matching. Index writes the configured
// namespace, and Search and DeleteWhere restrict selection to it. Returned native
// document IDs must belong to the same namespace and schema. The store
// does NOT create the schema — Vespa schemas are part of the
// application package, not a runtime API.
//
// Identity is encoded only in Vespa's native document ID. The store writes no
// separate ID attribute. Search and filtered deletion explicitly request the
// default document summary, which must include documentid, content, and
// scope_metadata without renaming or dynamic text transformation.
// Native documentid, sddocname, summaryfeatures, and matchfeatures are system
// projections and never become user metadata. Those names, the Scope fields, and
// the configured content and embedding fields are reserved in both indexed
// metadata and filter selectors. ContentField and EmbeddingField must differ.
//
// Metadata ownership. scope_metadata is a string containing the complete Core
// metadata JSON object, and is required in the default document summary.
// Native attributes are derived filtering projections; they never reconstruct
// returned metadata, because unset native bool and string attributes have defaults
// that lose Core's missing/null distinction. scope_metadata_paths is an
// array<string> attribute with exact, cased matching and no text index. It holds
// JSON-encoded key paths for non-null values, including empty arrays and objects.
// Index writes the authoritative JSON, attributes, and paths in one document put.
// Updates replace them together; no independent projection mutation is exposed.
// Search and deletion refuse hits without the required metadata object.
//
// The application schema must declare these storage fields in its document:
//
//	field scope_metadata type string {
//	    indexing: summary
//	}
//	field scope_metadata_paths type array<string> {
//	    indexing: attribute
//	    match { exact cased }
//	}
//	field scope_namespace type string {
//	    indexing: attribute | summary
//	    match { exact cased }
//	}
//
// Authentication. Talk to Vespa over HTTPS with mTLS (Vespa Cloud)
// or plain HTTP (self-hosted). Inject credentials by passing a
// configured [http.Client] via [StoreConfig.HTTPClient].
//
// Schema agreement is the caller's. [StoreConfig.RankingProfile] must rank by
// closeness, and the schema must declare the configured fields; both live in an
// application package this store cannot read, and Vespa's documentation does
// not establish that a status path answers on the container endpoint this store
// is configured with. So unlike its siblings, [NewStore] confirms nothing — a
// profile ranking by something else reports scores on a scale this store reads
// as closeness, and only the deployment can prevent that.
//
// Search shape. The store issues a `nearestNeighbor` YQL search:
//
//	POST /search/
//	{
//	  "yql": "select * from <schema> where {targetHits:K}nearestNeighbor(<vec_field>, q) and scope_namespace contains \"<namespace>\" and <filter>",
//	  "hits": K,
//	  "input.query(q)": {"values": [...]},
//	  "ranking": "<closeness_profile>",
//	  "presentation.summary": "default"
//	}
//
// The result's relevance must be a [0, 1] closeness score from the required rank
// profile. No nativeRank or distance-to-similarity conversion is inferred.
//
// Result ceiling. Vespa documents that "hits is capped at maxHits, default
// 400", and applies the cap by trimming the result rather than answering an
// error — a search for more would come back short with nothing to distinguish
// a capped result from an exhausted one. Search refuses a TopK above
// [StoreConfig.MaxHits], which defaults to Vespa's own [DefaultMaxHits]; a
// deployment whose query profile raised the value says so there, the same way
// it declares its schema and rank profile. Filtered deletion pages within the
// same ceiling.
//
// Query completeness. Vespa enables soft timeout by default and answers a
// partially evaluated query with 200 plus a degraded `root.coverage` report.
// Both search and filtered deletion require full coverage and no reported
// `root.errors`, because a shortened hit list is indistinguishable from a
// smaller result and would silently skip documents during deletion.
//
// Filter visitor produces YQL where-clause fragments — `author
// contains "Alice"` (equality on string fields uses `contains`),
// `year >= 2020`, `!(...)`, ` and ` / ` or `.
// Every filterable metadata key must exist as a top-level attribute
// in the schema, with a type and numeric range matching the Core operator: scalar attributes for
// comparisons, IN, and LIKE, and collection attributes for HAS. String attributes
// require exact, cased matching and no text index; the schema owns those settings.
// IN composes scalar equality with OR, preserving boolean and fractional values
// that native IN cannot represent. Inequality negates equality rather than
// emitting the unsupported native != operator. Integer literals outside the
// native signed 64-bit range are refused before I/O.
//
// LIKE compiles to an anchored regular expression with newline-inclusive
// wildcards. It matches the whole string and counts Unicode characters, as Core
// does. String literals use JSON escapes accepted by YQL; backslashes, quotes,
// and query-looking text remain data. Unsupported filters and result limits are
// checked before query embedding or native search/deletion I/O.
//
// Delete. Vespa selection expressions live under their own mini
// language; rather than translate the AST a second way, the store
// enumerates ids via a YQL search and then issues per-id deletes
// against the Document API (`DELETE /document/v1/<ns>/<schema>/docid/<id>`).
//
// Every atomic value condition also requires its non-null presence path before
// composition or negation. IS NULL negates that same presence projection, so
// absent and explicit null values agree with Core while false, empty strings,
// arrays, and objects remain present. Native default values never originate a
// competing filter truth value.
//
// Filterable keys. A metadata key is written into the query language as
// text, and that language cannot quote a field name, so a filter can only
// name a key that is a plain identifier. An indexed key is a string literal
// in the filter DSL, so without that limit a caller's key was read as
// syntax. Metadata attribute names and types must be declared in the application
// schema; the complete JSON object remains the returned metadata authority.
//
// See https://docs.vespa.ai/en/nearest-neighbor-search.html.
package vespa
