// Package anthropic implements the Anthropic Messages wire protocol reused by
// native and compatible provider endpoints inside the models module.
//
// Constructors:
//
//   - [NewMessages] — native /v1/messages. Full Claude surface:
//     extended thinking blocks, tool_use with signature continuity,
//     citations, fine-grained tool-result content blocks,
//     cache_control.
//
// A successful stream requires the final message_stop after all content blocks
// close and a message_delta supplies the finish reason. Premature EOF fails
// with [chat.ErrInvalidResponse]. Thinking and redacted blocks retain their
// individual identities through aggregation and history replay.
//
// Provider packages exposing an Anthropic-compatible endpoint reuse the
// Messages protocol through [NewCompatibleMessages] and select one typed
// [Dialect]. Application code continues to use the provider's own chat type.
//
// [Messages.CountInputTokens] exposes the provider-specific complete-request
// token endpoint. [NewTextCounter] serves isolated text workflows.
//
// Anthropic's Message Batches API (~50% pricing, up to 24h
// asynchronous) doesn't fit core/chat's synchronous request/response shape and
// is not exposed.
//
// Model id constants aren't exported — anthropic-sdk-go owns them
// ([anthropicsdk.ModelClaudeOpus5], [anthropicsdk.ModelClaudeSonnet5],
// [anthropicsdk.ModelClaudeFable5], etc.). Import the SDK directly
// when you need them.
package anthropic
