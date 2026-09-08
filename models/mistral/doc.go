// Package mistral wraps Mistral AI's API.
//
// Mistral exposes:
//
//   - /chat/completions — native structured content and thinking chunks, used
//     via [NewChat];
//   - /embeddings — OpenAI-compatible, used via [NewEmbeddingModel]
//     (returns an [openai.EmbeddingModel]);
//   - /moderations — Mistral-native shape that doesn't match OpenAI's
//     moderation response; [NewModerationModel] handles it directly
//     against [API] from this package.
//
// Additional Mistral surfaces not exposed here:
//   - /agents (stateful agent runs);
//   - /fim (code completion endpoint; it is not modeled by the chat facade).
//
// Stream termination. A chunk carrying finish_reason is the only claim that
// the answer is whole; the closing [DONE] marker is not, and neither is a body
// that simply ends. A stream that stops without one fails with
// [chat.ErrInvalidResponse] rather than handing back the fragment it did
// deliver. finish_reason model_length is the model's own context filling
// rather than the caller's budget, and both report as a truncation.
//
// See https://docs.mistral.ai/ for the full API reference.
package mistral
