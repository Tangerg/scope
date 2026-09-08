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
// Finish reasons. Mistral's own client types the field as stop, length,
// model_length, error or an unrecognized string. error and an unrecognized
// value both map to [chat.FinishReasonOther] — a known terminal state with
// no portable match — and the provider's own word for it rides along on the
// output, so an errored generation stays distinguishable from a provider
// limit. error used to reach Other through a default branch, which files a
// documented value under "not classified", and the synchronous path dropped
// the native value the streaming path already kept.
//
// See https://docs.mistral.ai/ for the full API reference.
package mistral
