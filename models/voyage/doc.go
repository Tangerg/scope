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
// Embedding limits. "The maximum length of the list is 1,000", which
// [MaxTextsPerEmbedRequest] names and the model refuses above rather than
// sending a request certain to be rejected. Voyage caps total tokens per
// request as well, by model rather than uniformly, which a text count cannot
// predict; that one surfaces as a provider error.
//
// Over-long input is truncated by default: truncation defaults to true, so "an
// over-length input texts will be truncated to fit within the context length,
// before vectorized by the embedding model" and nothing reports it. Set
// truncation to false through the request extension and "an error will be
// raised if any given text exceeds the context length".
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
