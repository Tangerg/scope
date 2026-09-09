// Package anthropic exposes Anthropic adapters. [NewChat] targets the Messages
// API, [NewChatCompletions] targets the OpenAI-compatible endpoint, and
// [NewTextEstimator] serves isolated text-token estimation.
//
// The OpenAI-compatible endpoint is narrower than the native one, and Anthropic
// publishes exactly how. Its support table marks reasoning_effort,
// presence_penalty and frequency_penalty "Ignored", so [NewChatCompletions]
// refuses those Core options rather than send them to be discarded — a caller
// cannot tell that apart from the adapter never having mapped them. It caps
// temperature at 1 and "values greater than 1 are capped at 1", so a larger
// value is refused rather than silently altered. response_format is ignored
// too, so a JSON output format is met with the shared prompt fallback.
//
// Anthropic calls the layer "primarily intended to test and compare model
// capabilities, and not considered a long-term or production-ready solution".
// [NewChat] is the native Messages surface and carries all of the above.
package anthropic
