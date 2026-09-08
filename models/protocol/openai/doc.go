// Package openai implements reusable OpenAI wire adapters for native and
// compatible provider endpoints.
//
// Modalities exposed:
//
//   - chat (Chat Completions) via [NewChatCompletions] — tool calling, streaming,
//     native request extensions, vision input, audio input/output;
//   - chat (Responses) via [NewResponses] — ordered reasoning and tool
//     replay, streaming, multimodal input, and complete-request input-token
//     counting through the native Responses endpoint;
//   - embedding via [NewEmbeddingModel] — text-embedding-3-small/large
//     with dimension truncation;
//   - image via [NewImageModel] — DALL·E 3 and gpt-image-1;
//   - moderation via [NewModerationModel] — omni-moderation-latest;
//   - audio tts via [NewAudioTTSModel] — tts-1, tts-1-hd, gpt-4o-mini-tts;
//   - audio transcription via [NewAudioTranscriptionModel] —
//     whisper-1, gpt-4o-transcribe, gpt-4o-mini-transcribe;
//   - audio translation via [NewAudioTranslationModel] — whisper-1
//     translating any source language to English (implements
//     transcription.Model).
//
// Model id constants aren't exported here — they're maintained by
// openai-go ([openai.ChatModelGPT5_6Sol], [openai.EmbeddingModelTextEmbedding3Large],
// etc.). Import openai-go directly when you need them.
//
// Provider-specific fields not modeled by Core reach the wire through request
// extensions scoped to the endpoint provider. Raw response details use that
// same namespace, so compatible endpoints never leak OpenAI provider metadata.
//
// Responses Call and Stream share terminal-state mapping: incomplete generation
// preserves its stop reason, failed generation returns an error, and a stream
// ending before a terminal response returns chat.ErrInvalidResponse.
//
// Provider packages with an OpenAI-compatible endpoint reuse the protocol
// through [NewCompatibleChatCompletions] and select one typed [Dialect]. This
// keeps provider-only fields such as reasoning_content out of OpenAI's native
// behavior while sharing the standard wire mapping.
// Moderation categories are named one field at a time, because the SDK types
// them as a struct rather than a map. A category OpenAI adds would arrive as
// a field nobody reads and a flagged input would come back unflagged for it,
// so a test reads the JSON names off ModerationCategories by reflection and
// requires the response to report exactly that set — silence is the failure
// mode here, and it is the one place silence is the whole problem.
package openai
