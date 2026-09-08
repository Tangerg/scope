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
package ollama
