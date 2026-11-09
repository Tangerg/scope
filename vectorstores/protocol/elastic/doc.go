// Package elastic owns the shared Elasticsearch and OpenSearch bulk and scroll
// protocol. Native adapters keep their SDK transport, mapping, vector constraints,
// query syntax and record decoder; this module imports neither native SDK.
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
