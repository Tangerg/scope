// Package mistral wraps Mistral AI's API.
//
// Mistral exposes:
//
//   - /chat/completions — native structured content and thinking chunks, used
//     via [NewChat];
//   - /embeddings — OpenAI-compatible, used via [NewEmbeddingModel]
//     (returns an [openai.EmbeddingModel]);
//   - /moderations — Mistral-native shape that doesn't match OpenAI's
//     moderation response; [NewModerationModel] handles it directly.
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
// The native reason owns completion; Core receives it only on the last delta
// after the stream ends normally, so trailing usage is included and a tail
// failure cannot follow a published terminal delta. Content and Tool calls
// after the native finish_reason are invalid.
//
// Finish reasons. Mistral's own client types the field as stop, length,
// model_length, error or an unrecognized string. error and an unrecognized
// value both map to [chat.FinishReasonOther] — a known terminal state with
// no portable match — and the provider's own word for it rides along on the
// output, so an errored generation stays distinguishable from a provider
// limit.
//
// See https://docs.mistral.ai/ for the full API reference.
// Core Options.ReasoningEffort is the only request control for reasoning
// intensity. Native request extensions reject that field and other unknown
// fields instead of accepting a second configuration source.
//
// Reasoning replay. Each native thinking child has its own Core reasoning Part.
// Part.Text owns visible text; ReasoningState retains native fields, references,
// signatures, and block boundaries without text copies. Replay reconstructs the
// native thinking content from current Core parts. Previously stored full thinking
// chunks are rejected. Preserve signed content and native fields when replaying.
package mistral
