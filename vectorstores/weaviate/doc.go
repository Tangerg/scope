// Package weaviate stores prepared vectors in a Weaviate collection through the
// official gRPC protocol. Server metadata, schema, and deletion use one HTTP
// origin that rejects redirects. Search and batch writes address the configured
// gRPC server directly. HTTPS enables certificate verification on both transports.
// The Host closes the
// client. Provision exactly one self-provided dense vector named "embedding":
// its collection description must equal the bound SpaceID.StorageKey(). Use
// HNSW, flat or dynamic with an explicit distance. The REST schema supplies the native
// flags and distance. Multi-tenancy, reference
// properties and multivectors are not supported by this binding.
//
// The same object owns content (searchable word text), metadata (field text)
// and metadataTerms (filterable field text[]). The metadata cell preserves
// exact business JSON and source provenance. Only business fields contribute
// to the native terms. Filters outside the equivalent subset fail before search.
//
// A gRPC batch reply identifies rejections by position. Omitted indices are
// acknowledged only after a completed reply; a transport failure leaves the
// submitted group unconfirmed. Acknowledgment is distinct from search visibility.
// NearVector and Hybrid each issue one native search. Hybrid uses the supplied
// lexical text and coordinates in a single relative-score fusion; an absent
// alpha leaves the server default intact. DeleteIDs removes named UUIDs only.
// Missing native identities, vectors, scores or source evidence invalidate the
// entire result. No operation falls back to enumeration.
// Unexpected REST statuses retain HTTP status and safe request identity through
// core/httpclient.ResponseError; DELETE accepts both 204 and 404 as success.
//
// The integration suite requires an isolated server and provisions disposable
// collections. A local protocol peer exercises both transports in ordinary tests.
package weaviate
