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
// Core's MaxOutputTokens travels as max_tokens. xAI's own examples pass
// max_tokens against a current Grok model on this endpoint and its
// documentation nowhere marks the field deprecated or names
// max_completion_tokens for Chat Completions; max_output_tokens belongs to its
// separate Responses API surface, which this adapter does not target.
//
// See https://docs.x.ai/ for the full API reference.
package xai
