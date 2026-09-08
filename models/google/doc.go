// Package google exposes native Gemini chat, embedding, image, speech,
// transcription, and token-estimation adapters. [NewChatCompletions] targets
// Gemini's OpenAI-compatible chat endpoint.
// Native chat maps a natural stop with function calls to Core tool_calls while
// preserving interrupted and invalid-call outcomes and the native finish reason.
//
// Constructors take a context because construction reaches the network. On the
// Vertex AI backend with application default credentials, building the client
// resolves them and then asks the resolved credential for its quota project,
// which is a metadata-server call the context governs — so a caller can bound
// or cancel its own wiring instead of a background context deciding for it.
// [NewChatCompletions] is the exception: it builds an OpenAI-compatible client
// and performs no I/O.
package google
