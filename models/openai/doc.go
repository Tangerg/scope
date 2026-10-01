// Package openai binds the official OpenAI endpoints. [NewChatCompletions]
// uses Chat Completions; [NewResponses] uses the Responses API.
//
// Each constructor returns the shared protocol model from
// [github.com/Tangerg/scope/models/protocol/openai], which owns the wire
// protocol and serves every OpenAI-compatible provider. This package owns the
// OpenAI binding: its provider identity, model identifiers, and extension keys.
package openai
