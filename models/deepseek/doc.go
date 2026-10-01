// Package deepseek wraps DeepSeek's OpenAI-compatible API.
//
// DeepSeek derives from the OpenAI Chat Completions protocol while retaining
// its provider-specific reasoning semantics behind [NewChatCompletions].
//
// Provider-specific behavior handled transparently:
//
//   - reasoning_content is decoded into a [chat.PartReasoning];
//   - provider-issued reasoning from every prior assistant turn is replayed
//     as reasoning_content, as required when the next request includes tools.
//
// Core Options.ReasoningEffort owns both the thinking mode and its intensity:
// "none" disables thinking; "low", "high", and "max" enable it. The provider
// defaults to thinking with high effort. In thinking mode temperature is
// unsupported and top_p must be in [0.95, 1]; in non-thinking mode top_p is
// unsupported. Required and named tool choices require non-thinking mode.
//
// Provider-specific request controls use the typed [RequestOptions] extension;
// the OpenAI SDK request shape is intentionally not exposed. Prefix completion
// remains a separate beta protocol and is not accepted by [NewChatCompletions].
//
// See https://api-docs.deepseek.com/ for the full API reference.
package deepseek
