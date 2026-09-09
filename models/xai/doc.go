// Package xai wraps xAI's (Grok) OpenAI-compatible API.
//
// [NewChat] returns xAI's provider-local [Chat], backed by the
// shared OpenAI Chat Completions protocol.
//
// Current Grok models support text, image input, structured outputs, reasoning
// effort, and custom function calling through the Chat Completions surface.
// xAI's server-side Web Search, X Search, code execution, and collections tools
// belong to its Responses API surface and are not represented as Core custom
// functions by this adapter.
//
// Core's MaxOutputTokens travels as max_completion_tokens. xAI's OpenAPI
// document describes max_tokens as "[DEPRECATED] ... Deprecated in favor of
// max_completion_tokens", so the guide examples that still pass the old field
// are not the contract. Note xAI scopes the replacement to "visible output
// tokens (i.e. does not apply to tokens used for reasoning or function calls)",
// unlike OpenAI's, which bounds reasoning too. max_output_tokens belongs to
// xAI's separate Responses API surface, which this adapter does not target.
//
// See https://docs.x.ai/ for the full API reference.
package xai
