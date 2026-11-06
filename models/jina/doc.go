// Package jina wraps Jina AI's native embedding and reranking APIs. Embedding supports the dense
// float subset of current v5, v4, v3, CLIP, and code embedding models that
// maps losslessly to core/embedding.
//
// Task type, late chunking, embedding_type and normalization use
// [EmbeddingRequestExtensionKey]. Tasks are admitted by the effective model's
// schema: v3 separation, v4 code tasks, and v5 clustering are distinct controls.
// An explicit task requires a recognized model schema; omitting task leaves
// model selection open. Unknown controls and encodings other than float are
// rejected before provider I/O.
//
// Jina's /embeddings dialect partially overlaps OpenAI's but uses
// "dimensions" rather than "output_dimension" and exposes the
// task-conditioning field; this package implements [embedding.Model]
// directly against the native API. Reranking returns indices into the
// caller-owned document batch rather than provider-owned document copies.
//
// Rerank scores. Jina's OpenAPI schema describes relevance_score as a number
// with "Higher is more relevant" and declares no minimum, maximum or
// normalization. Scores are passed unchanged to [rerank.Score], including
// negative scores. Comparisons are meaningful only for the same query and model;
// the adapter does not invent a probability or rescale the reported value.
//
// See https://jina.ai/embeddings/ for the full reference.
package jina
