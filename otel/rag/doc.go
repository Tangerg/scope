// Package rag adapts Scope retrieval capabilities to the official OpenTelemetry
// tracing and metrics APIs. The rag module does not import OpenTelemetry; this
// module decorates it from outside so a core-only consumer never inherits the
// telemetry dependency.
//
// Decorate a [github.com/Tangerg/scope/rag.Retriever] with Middleware.Retrieve to
// record a rag.retrieve span and a rag.operation.duration measurement per
// retrieval. Query text and document content are never recorded; only the
// operation name and returned document count become attributes. A panicking
// retriever ends the observation and re-panics the original value unchanged.
//
// Vector-store operation decorators expose only the Core operation they wrap.
// A RAG source or destination also needs provider admission before embedding.
// Compose the original store's admission methods with its decorated operations,
// as the vectorStoreComposition example shows; the store retains all validation.
package rag
