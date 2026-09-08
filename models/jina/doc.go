// Package jina wraps Jina AI's native embedding and reranking APIs. Embedding supports the dense
// float subset of current v5, v4, v3, CLIP, and code embedding models that
// maps losslessly to core/embedding.
//
// Jina-specific knobs that don't fit the generic surface — task type
// ("retrieval.query" / "retrieval.passage" / "text-matching" /
// "classification" / "separation"), late chunking, embedding_type
// (float / int8 / uint8 / binary / ubinary quantization),
// normalization — are reached through [EmbeddingRequestExtensionKey] and [EmbeddingRequest].
//
// Jina's /embeddings dialect partially overlaps OpenAI's but uses
// "dimensions" rather than "output_dimension" and exposes the
// task-conditioning field; this package implements [embedding.Model]
// directly against the native API. Reranking returns indices into the
// caller-owned document batch rather than provider-owned document copies.
//
// Rerank scores. Jina's OpenAPI schema describes relevance_score as a number
// with "Higher is more relevant" and declares no minimum, maximum or
// normalization, so this package does not claim one: the value is passed to
// [rerank.Score], whose contract is [0, 1], and a score outside that range
// fails the call by name rather than being rescaled onto the bound.
//
// See https://jina.ai/embeddings/ for the full reference.
package jina
