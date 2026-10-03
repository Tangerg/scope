// Package ollama exposes Ollama's native chat and embedding adapters.
// [NewChatCompletions] targets Ollama's OpenAI-compatible chat endpoint.
// Native chat maps a natural stop with tool calls to Core tool_calls while
// preserving interrupted outcomes and the native done reason.
//
// Stream termination. A chunk with done true is the only claim that the
// generation finished, and the non-streaming path already refuses a response
// without it. A stream whose body ends before that chunk fails with
// [chat.ErrInvalidResponse], because to a delta consumer a truncated body looks
// exactly like a completed answer.
// Reasoning. Native chat's think field is configured exclusively through
// RequestExtensionKey. It accepts booleans and thinking levels (low, medium,
// high, max). Core ReasoningEffort is rejected by native chat so two request
// representations cannot compete for the same field.
//
// Embedding input is truncated by default. /api/embed's truncate parameter
// "defaults to true", so a text past the model's context window is embedded
// from a prefix and nothing reports it; setting it false makes Ollama return an
// error instead. The knob reaches the wire through the native request
// extension.
//
// See https://github.com/ollama/ollama/blob/main/docs/api.md for the API
// reference.
package ollama
