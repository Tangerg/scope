// Package google exposes native Gemini chat, embedding, image, speech,
// transcription, and token-estimation adapters. [NewChatCompletions] targets
// Gemini's OpenAI-compatible chat endpoint.
// Native chat maps a natural stop with function calls to Core tool_calls while
// preserving interrupted and invalid-call outcomes and the native finish reason.
package google
