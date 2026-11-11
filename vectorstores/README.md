# Vector-store adapters

Scope's vector-store adapters connect storage services to Core's
provider-neutral vector-store contracts. This directory is a namespace, not a
module: every adapter below it is an independently versioned leaf with its own
`doc.go`.

Native stores preserve typed source identity and location separately from the
searchable business metadata. Adapters that share one physical JSON field use
the [document storage codec](../core/document/stored_metadata.go); only business
metadata may feed search indexes, predicates and the returned metadata view.

## Provider catalog

This table is generated from Go method signatures in the provider modules.
Native vector stores accept prepared records and coordinates. Managed retrievers
expose the hosted service's actual operation. Follow the contract owner for
space binding, supported predicates, confirmation granularity and consistency:
a method name alone cannot establish those guarantees.

Live-service evidence is **not established for any row by this catalog**.
Checked examples, offline HTTP/gRPC/SQL peers, race tests, and a successful
integration-tag compilation do not prove a live backend. A PostgreSQL run
store test does not exercise the pgvector extension. Record service versions
and actual test reports separately for any production release.

Regenerate with `go run scripts/vectorstore-matrix.go -write` from the workspace
root. Use `-check` to detect drift or omit flags to print the current matrix.

<!-- vectorstore-catalog:begin -->

| Module | Public boundary | Index, query, or explicit scan operations | Contract owner |
|---|---|---|---|
| `retrievers/bedrockkb` | Managed retrieval | `Retrieve` | [bedrockkb](../retrievers/bedrockkb/doc.go) |
| `retrievers/vectara` | Managed retrieval | `CreateDocument`, `Retrieve` | [vectara](../retrievers/vectara/doc.go) |
| `vectorstores/azureaisearch` | Native vectors | `Query`, `Scan`, `Upsert` | [azureaisearch](azureaisearch/doc.go) |
| `vectorstores/azurecosmos` | Native vectors | `DeleteMatching`, `Query`, `Scan`, `Upsert` | [azurecosmos](azurecosmos/doc.go) |
| `vectorstores/cassandra` | Native vectors (ranked scan only) | `DeleteMatching`, `QueryByScan`, `Upsert` | [cassandra](cassandra/doc.go) |
| `vectorstores/chroma` | Native vectors | `Query`, `Scan`, `Upsert` | [chroma](chroma/doc.go) |
| `vectorstores/clickhouse` | Native vectors | `DeleteMatching`, `Query`, `Scan`, `Upsert` | [clickhouse](clickhouse/doc.go) |
| `vectorstores/couchbase` | Native vectors (ranked scan only) | `DeleteMatching`, `QueryByScan`, `Upsert` | [couchbase](couchbase/doc.go) |
| `vectorstores/elasticsearch` | Native vectors | `DeleteMatching`, `QueryByScan`, `Query`, `Upsert` | [elastic](protocol/elastic/doc.go), [elasticsearch](elasticsearch/doc.go) |
| `vectorstores/mariadb` | Native vectors | `DeleteMatching`, `QueryByScan`, `Query`, `Upsert` | [mariadb](mariadb/doc.go) |
| `vectorstores/milvus` | Native vectors | `QueryByScan`, `Query`, `Upsert` | [milvus](milvus/doc.go) |
| `vectorstores/mongodb` | Native vectors | `DeleteMatching`, `QueryByScan`, `Query`, `Upsert` | [mongodb](mongodb/doc.go) |
| `vectorstores/neo4j` | Native vectors (ranked scan only) | `DeleteMatching`, `QueryByScan`, `Upsert` | [neo4j](neo4j/doc.go) |
| `vectorstores/opensearch` | Native vectors | `DeleteMatching`, `QueryByScan`, `Query`, `Upsert` | [elastic](protocol/elastic/doc.go), [opensearch](opensearch/doc.go) |
| `vectorstores/oracle` | Native vectors | `DeleteMatching`, `QueryByScan`, `Query`, `Upsert` | [oracle](oracle/doc.go) |
| `vectorstores/pinecone` | Native vectors | `Query`, `Upsert` | [pinecone](pinecone/doc.go) |
| `vectorstores/postgres` | Native vectors | `Query`, `Scan`, `Upsert` | [cockroachdb](postgres/cockroachdb/doc.go), [pgvector](postgres/pgvector/doc.go) |
| `vectorstores/qdrant` | Native vectors | `Query`, `Upsert` | [qdrant](qdrant/doc.go) |
| `vectorstores/redis` | Native vectors | `DeleteMatching`, `QueryByScan`, `Query`, `Upsert` | [redis](redis/doc.go) |
| `vectorstores/s3vectors` | Native vectors | `Query`, `Scan`, `Upsert` | [s3vectors](s3vectors/doc.go) |
| `vectorstores/tidb` | Native vectors | `DeleteMatching`, `QueryByScan`, `Query`, `Upsert` | [tidb](tidb/doc.go) |
| `vectorstores/typesense` | Native vectors | `QueryByScan`, `Query`, `Upsert` | [typesense](typesense/doc.go) |
| `vectorstores/vespa` | Native vectors (ranked scan only) | `DeleteMatching`, `QueryByScan`, `Upsert` | [vespa](vespa/doc.go) |
| `vectorstores/weaviate` | Native vectors | `Query`, `Upsert` | [weaviate](weaviate/doc.go) |

<!-- vectorstore-catalog:end -->

## Ownership

- Each leaf owns only its external boundary, configuration, and translation.
- Core owns portable semantics and the contract test suites.
- The family owns backend translation, not retrieval semantics, embedding
  policy, or product search.

## Dependencies

- A leaf may depend on Core and the external service it adapts.
- Leaves do not import sibling adapters or higher-level capability modules.

## Invariants

- External details do not alter or leak through shared contracts.
- Construction, authority, and unsupported capabilities are explicit.
- Runtime policy and product workflows remain outside adapter modules.

## A successful call is not a completed operation

Storage services routinely report an incomplete operation inside a successful
response, so an adapter states what its evidence actually proves. Three
questions decide it, and each adapter answers them in its own `doc.go`:

- **Did every item apply?** A batch endpoint that answers 200 with per-item
  results has not acknowledged anything until those results are read. Require
  one successful result per item sent, and treat a missing, extra, or repeated
  result as an unaccounted item rather than a success.
- **Is this result complete?** Timeouts, lost shards, and degraded coverage
  return partial data with a successful status. A page shorter than the
  requested limit, or an empty page, proves exhaustion only when the service
  says so — read the continuation token or the reported totals instead of
  inferring the end from a page's length.
- **Does the primitive answer the question asked?** An approximate
  nearest-neighbor query returns up to *k* candidates, never every match, so it
  cannot enumerate a filter. A native adapter must reject a predicate it cannot lower exactly.
  If the Host explicitly authorizes a full source scan, it supplies finite
  record, byte, and time limits; [`filter.Match`](../core/vectorstore/filter/match.go)
  defines membership. Exhausting a budget yields no partial ranking.

Where a service genuinely cannot offer atomicity, say what state a failure
leaves behind instead of implying a rollback. Adapters keep their provider
dependency narrow — an interface naming the operations they call — so this
accounting is checkable without a live backend.

Each adapter's own boundary, public API, and executable usage live in its
`doc.go`, GoDoc, and checked examples.
