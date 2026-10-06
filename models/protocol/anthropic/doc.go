// Package anthropic implements the Anthropic Messages wire protocol reused by
// native and compatible provider endpoints inside the models module.
//
// A nonempty Config BaseURL overrides ANTHROPIC_BASE_URL; an absent environment
// value uses the SDK production endpoint. Both supplied URL sources are checked
// during construction because the SDK applies its environment options before
// explicit options: malformed environment options can otherwise fail only
// when a request starts.
//
// Constructors:
//
//   - [NewMessages] — native /v1/messages. Full Claude surface:
//     extended thinking blocks, tool_use with signature continuity,
//     citations, fine-grained tool-result content blocks,
//     cache_control.
//
// A successful stream begins with one message_start, opens each content block
// before its deltas, and ends with message_stop after all blocks close and a
// message_delta supplies the finish reason. Invalid event order or mismatched
// delta types fail with [chat.ErrInvalidResponse]. Native server-tool and future
// blocks remain in exact native event metadata without becoming local Tool
// calls. Thinking and redacted blocks retain their identities through
// aggregation and history replay.
//
// Provider packages exposing an Anthropic-compatible endpoint reuse the
// Messages protocol through [NewCompatibleMessages] and select one typed
// [Dialect]. Application code continues to use the provider's own chat type.
//
// [Messages.CountInputTokens] exposes the provider-specific complete-request
// token endpoint. [NewTextCounter] serves isolated text workflows.
//
// [Messages.ListModels] follows the SDK's native model pagination using this
// binding. Count and total response-byte budgets are explicit per operation;
// the context owns the deadline. A partial scan never returns successful IDs.
// Catalog enrichment and model selection remain consumer policy.
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
