// Package cohere implements Core embedding and reranking with Cohere's v2 API.
//
// Embedding callers select the official input_type explicitly because query,
// document, classification, and clustering embeddings have different task
// semantics. Reranking returns indices into the caller-owned document batch.
//
// Rerank scores reach [rerank.Score] unchanged because Cohere documents the
// range they arrive in: "Relevance scores are normalized to be in the range
// [0, 1]". Cohere also warns against reading them as ratios — a 0.9 is not
// twice as relevant as a 0.45 — which is why Core calls a score
// query-relative and not comparable across providers.
//
// See https://docs.cohere.com/ for the full API reference.
package cohere
