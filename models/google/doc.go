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
//
// [NewAudioTTSModel] performs unary-only Gemini 2.5 speech synthesis.
// Gemini 3.1 uses [NewStreamingAudioTTSModel]: Call aggregates Stream, and both
// require successful stream completion. Call discards partial audio on error.
// Native speech_response metadata describes the latest stream event, not a
// fabricated unary response. Request model overrides must stay in that capability.
package google
