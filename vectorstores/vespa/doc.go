// Package vespa exposes Yahoo Vespa's vector search
// through the Core vector-store capability interfaces. Documents are regular Vespa documents in a
// schema with id / content / embedding (tensor) fields plus any
// metadata attributes — reached over the HTTP Document / Search REST
// APIs.
//
// Requirements: a Vespa application (Vespa Cloud or self-hosted) with
// a schema (.sd file) declaring the embedding tensor field and any
// metadata attributes the filter visitor will address. The store
// does NOT create the schema — Vespa schemas are part of the
// application package, not a runtime API.
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
//	  "yql": "select * from <schema> where {targetHits:K}nearestNeighbor(<vec_field>, q) and <filter>",
//	  "hits": K,
//	  "input.query(q)": {"values": [...]},
//	  "ranking": "default"
//	}
//
// The result's `relevance` is taken as-is — for the default cosine
// configuration this is already a [0, 1] similarity score.
//
// Query completeness. Vespa enables soft timeout by default and answers a
// partially evaluated query with 200 plus a degraded `root.coverage` report.
// Both search and filtered deletion require full coverage and no reported
// `root.errors`, because a shortened hit list is indistinguishable from a
// smaller result and would silently skip documents during deletion.
//
// Filter visitor produces YQL where-clause fragments — `author
// contains "Alice"` (equality on string fields uses `contains`),
// `year >= 2020`, `tag in ("a", "b")`, `!(...)`, ` and ` / ` or `.
// Every filterable metadata key must exist as a top-level attribute
// in the schema.
//
// Delete. Vespa selection expressions live under their own mini
// language; rather than translate the AST a second way, the store
// enumerates ids via a YQL search and then issues per-id deletes
// against the Document API (`DELETE /document/v1/<ns>/<schema>/docid/<id>`).
//
// Null tests are refused. The query language reference states that "there is
// no way to query for a field that is not set / equals null or NaN" and
// suggests a magic sentinel value as a workaround, which this store will not
// invent on a caller's behalf.
//
// Filterable keys. A metadata key is written into the query language as
// text, and that language cannot quote a field name, so a filter can only
// name a key that is a plain identifier. An indexed key is a string literal
// in the filter DSL, so without that limit a caller's key was read as
// syntax. A document whose metadata key is anything at all still stores and
// reads back fine; this is only about which keys a filter can name.
//
// See https://docs.vespa.ai/en/nearest-neighbor-search.html.
package vespa
