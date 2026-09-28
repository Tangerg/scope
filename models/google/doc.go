// Package google exposes native Gemini chat, embedding, image, speech,
// transcription, and token-estimation adapters. [NewChatCompletions] targets
// Gemini's OpenAI-compatible chat endpoint.
// Native chat maps a natural stop with function calls to Core tool_calls while
// preserving interrupted and invalid-call outcomes and the native finish reason.
// Native chat request extensions use the SDK's camelCase JSON fields only.
// Sampling, output format, reasoning effort, system messages, function tools,
// and tool choice belong to Core; attempts to set them again in native config
// are rejected even when the Core value is absent. Native safety, grounding,
// modalities, and thinking-budget settings remain available.
// Thought signatures retain their original Part positions across streaming,
// history serialization, and replay. Signature-only Parts carry opaque Core
// reasoning state without visible reasoning text.
// Native part_state metadata requires the original thought flag; histories
// written without that field must be regenerated before replay.
// Native embedding extensions also use SDK camelCase fields and reject
// outputDimensionality; embedding.Options.Dimensions owns that value.
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
// Unary speech and transcription require a STOP finish reason. Truncated,
// blocked, or unterminated generation returns the modality's ErrInvalidResponse
// without presenting partial content as a completed response.
// Gemini 3.1 uses [NewStreamingAudioTTSModel]: Call aggregates Stream, and both
// require successful stream completion. Call discards partial audio on error.
// Native speech_response metadata describes the latest stream event, not a
// fabricated unary response. Request model overrides must stay in that capability.
package google
