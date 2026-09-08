// Package voyage implements Core embedding and reranking with Voyage AI.
//
// Voyage publishes retrieval-tuned text and multimodal embedding
// models that consistently lead public retrieval benchmarks; the
// current voyage-4-large / voyage-4 / voyage-4-lite models support
// matryoshka-style output truncation via the output_dimension
// parameter.
//
// Voyage's /embeddings shape is bespoke (input_type, truncation,
// quantization knobs) and doesn't speak the OpenAI dialect — this
// package implements [embedding.Model] directly against the native
// API. Reranking keeps truncation provider-specific while returning Core
// document indices.
//
// Rerank scores. Voyage documents relevance_score only as "the relevance
// score of the document with respect to the query" and states no range, so
// this package does not claim one either: the value is passed to
// [rerank.Score], whose contract is [0, 1], and a score outside that range
// fails the call by name rather than being rescaled onto the bound. There is
// no published scale to map from, and inventing one would put a plausible
// number where a reported mismatch belongs.
//
// See https://docs.voyageai.com/ for the full reference.
package voyage
