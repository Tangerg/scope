// Package google exposes native Gemini chat, embedding, image, speech,
// transcription, and token-estimation adapters. [NewChatCompletions] targets
// Gemini's OpenAI-compatible chat endpoint.
// Native chat maps a natural stop with function calls to Core tool_calls while
// preserving interrupted and invalid-call outcomes and the native finish reason.
// Core ToolCall.ID is replayed unchanged on the call and its matching result,
// including IDs synthesized when the provider omitted one.
// Native chat request extensions use the SDK's camelCase JSON fields only.
// Chat, speech, and transcription each produce a single candidate. An explicit
// count of one is supported; larger or negative counts fail before provider I/O.
// GenerateContent has one native decoder shared by unary and streaming calls.
// The official SDK owns full backend request admission and projection. Local
// request preparation performs no provider I/O; the adapter sends the prepared
// request once and keeps strict unary/SSE decoding outside the SDK's response
// converters. Opaque JSON retains its original number representation.
// Automatic continuation is rejected. HTTP extensions allow headers and timeout
// only; timeout uses the SDK Go JSON representation, integer nanoseconds. Omission
// or zero adds no per-request timeout, while the host HTTP policy still applies.
// Local admission failures prove NotExecuted; errors after external execution
// are Unknown and retain their cause without inheriting nested call evidence.
// Only ordinary content FileData may omit MIME because Gemini can resolve its
// type remotely. Inline and function-response media require explicit MIME.
// Core Citation owns validation of native citations with a URI. Invalid
// citations fail Call and Stream rather than disappearing from a successful response.
// Citation-only chunks reach Core's accumulator, which owns text attachment.
// Sampling, output format, reasoning effort, system messages, function tools,
// and tool choice belong to Core; attempts to set them again in native config
// are rejected even when the Core value is absent. Native safety, grounding,
// modalities, and thinking-budget settings remain available. Thinking budget
// and Core reasoning effort are mutually exclusive after resolving defaults.
// Code execution is rejected before I/O because Core cannot represent its
// output and replay losslessly. Execution parts in responses fail validation.
// Each stream binds the first reported response ID and model version; later
// omissions preserve them, while conflicting values fail validation.
// Thought signatures retain their original Part positions across streaming,
// history serialization, and replay. Signature-only Parts carry opaque Core
// reasoning state without visible reasoning text.
// Core Part.Kind owns the thought classification of visible text. Native
// part_state retains the independent flag on signature-only, tool, and media
// Parts. Stored visible-text state containing a competing thought flag is rejected.
// Google-managed ToolCall/ToolResponse Parts are native replay state, never host
// tool calls. Runs attach before the next Core part or at the message's end, so
// history replay uses current Core content without losing native tool ordering.
// A server-tool-only turn uses an assistant message with native metadata and no
// portable parts. Native Part data payloads are exclusive; conflicts are errors.
// Embedding extensions admit only taskType, title, mimeType, autoTruncate,
// documentOcr, and audioTrackExtraction. HTTP envelopes are excluded because
// SDK extra-body merging could overwrite Core's texts, model, or dimensions.
// autoTruncate requires Vertex AI; Gemini rejects either explicit boolean value.
// Positive dimensions must fit the SDK's int32 representation; the service
// determines the selected model's supported dimensions.
// Embedding responses retain the exact native JSON. Optional token, truncation,
// and billing metadata is projected only when reported, including zero and false.
//
// Constructors take a context because construction reaches the network. On the
// Vertex AI backend with application default credentials, building the client
// resolves them and then asks the resolved credential for its quota project,
// which is a metadata-server call the context governs — so a caller can bound
// or cancel its own wiring instead of a background context deciding for it.
// [NewChatCompletions] and [NewImageModel] borrow HTTP policy directly rather
// than constructing a Gen AI client.
//
// [NewImageModel] owns the Gemini Interactions image capability. Native controls
// are [ImageGenerationOptions] under [ImageRequestExtensionKey]; their typed
// vocabulary and admission live with those public values.
// Input image MIME declarations remain required; URLs do not establish their
// content type. Only completed interactions produce Core image responses.
// Interactions uses an independent HTTP transport because GenerateContent and
// Vertex AI use different protocols and authentication contracts.
//
// Interactions targets the current steps schema. Hosts that pin API revisions
// through their HTTPClient must choose one with that schema; legacy outputs
// responses cannot establish a completed image result.
//
// [NewSpeechModel] performs unary-only Gemini 2.5 speech synthesis.
// Unary speech and transcription require a STOP finish reason. Truncated,
// blocked, or unterminated generation returns the modality's ErrInvalidResponse
// without presenting partial content as a completed response.
// Gemini 3.1 uses [NewStreamingSpeechModel]: Call aggregates Stream, and both
// require successful stream completion. Call discards partial audio on error.
// Native speech_response metadata describes the latest stream event, not a
// fabricated unary response. Request model overrides must stay in that capability.
package google
