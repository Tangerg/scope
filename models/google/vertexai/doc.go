// Package vertexai exposes Google Gen AI capabilities through Vertex AI's GCP
// backend and Application Default Credentials.
//
// Chat, transcription, speech, and text embedding share the official Google
// Gen AI SDK protocol mappers with package google while supplying
// genai.BackendVertexAI, project, and location. Image generation has its own
// adapter because the current Vertex contract is GenerateContent with Gemini
// image models, whereas the public Gemini Developer API now recommends the
// Interactions API. Treating those two transports as interchangeable would
// leak backend details and preserve deprecated Imagen behavior.
// Native chat request extensions use SDK camelCase fields. Core exclusively
// owns sampling, output format, reasoning effort, system messages, function
// tools, and tool choice; duplicate native fields are rejected. Provider-only
// safety, grounding, modalities, and thinking-budget fields remain available.
// Core ToolCall.ID is replayed unchanged on the call and its matching result,
// including IDs synthesized when the provider omitted one.
// Core Citation owns validation of native citations with a URI. Invalid
// citations fail Call and Stream rather than disappearing from a successful response.
// Citation-only chunks reach Core's accumulator, which owns text attachment.
// Thought signatures, including empty-text Parts, survive Core history
// serialization and replay in their original Part positions.
// Core Part.Kind owns the thought classification of visible text. Native
// part_state retains the independent flag on signature-only, tool, and media
// Parts. Stored visible-text state containing a competing thought flag is rejected.
// Embedding extensions use SDK camelCase fields and reject native
// outputDimensionality; Core embedding.Options.Dimensions owns that value.
//
// API keys are not used. Authenticate locally with Application Default
// Credentials or provide an authenticated HTTP client; production workloads
// normally use a service account or Workload Identity.
//
// Model availability and regions change independently on Vertex AI. Select an
// explicit model id and location from the current Vertex model documentation.
//
// Constructors take a context because construction reaches the network. On the
// Vertex AI backend with application default credentials, building the client
// resolves them and then asks the resolved credential for its quota project,
// which is a metadata-server call the context governs — so a caller can bound
// or cancel its own wiring instead of a background context deciding for it.
//
// See https://cloud.google.com/vertex-ai/generative-ai/docs.
//
// [NewSpeechModel] performs unary-only Gemini 2.5 speech synthesis.
// Unary speech and transcription require a STOP finish reason and return the
// modality's ErrInvalidResponse for truncated, blocked, or unterminated output.
// Gemini 3.1 uses [NewStreamingSpeechModel]: Call aggregates Stream, and both
// require successful stream completion. Call discards partial audio on error.
// Native speech_response metadata describes the latest stream event, not a
// fabricated unary response. Request model overrides must stay in that capability.
package vertexai
