// Package ollama exposes Ollama's native chat and embedding adapters.
// [NewChatCompletions] targets Ollama's OpenAI-compatible chat endpoint.
// Native chat maps a natural stop with tool calls to Core tool_calls while
// preserving interrupted outcomes and the native done reason.
package ollama
