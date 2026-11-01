// Package anthropic binds the official Anthropic endpoints. [NewMessages]
// targets the Messages API, [NewChatCompletions] targets the
// OpenAI-compatible endpoint, and the Messages text-counter binding serves isolated text-token
// estimation.
//
// Constructors return the shared protocol models from
// [github.com/Tangerg/scope/models/protocol/anthropic] and
// [github.com/Tangerg/scope/models/protocol/openai], which own the wire
// protocols. This package owns the Anthropic binding: its identity, endpoints,
// and the compatibility limits below.
//
// The OpenAI-compatible endpoint is narrower than the native one, and Anthropic
// publishes exactly how. Its support table marks reasoning_effort,
// presence_penalty and frequency_penalty "Ignored", so [NewChatCompletions]
// refuses those Core options rather than send them to be discarded — a caller
// cannot tell that apart from the adapter never having mapped them. It caps
// temperature at 1 and "values greater than 1 are capped at 1", so a larger
// value is refused rather than silently altered. response_format is ignored
// too, so unsupported JSON output formats return ErrUnsupportedOutputFormat.
//
// Anthropic calls the layer "primarily intended to test and compare model
// capabilities, and not considered a long-term or production-ready solution".
// [NewMessages] implements the native 2026-10-21 Messages contract. It rejects
// temperature, top_p and top_k (even at their former defaults) before sending
// a request. Its API key is sent as an Authorization bearer credential.
package anthropic
