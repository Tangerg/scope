// Package chatclient provides direct, optional conveniences around the minimal
// chat protocols and model capabilities defined by Core. Client.Output applies
// a provider-neutral OutputFormat and strictly decodes naturally completed
// text. Other completion reasons remain identifiable as OutputCompletionError.
// Typed output has only a terminal value; callers that need transport deltas
// use StreamClient.Stream with a required streaming dependency.
//
// [NewSingleBatchToolMiddleware] covers the deliberately small direct-use path: it
// advertises a frozen executable Tool set, validates one returned call batch,
// executes it serially, and performs one follow-up model call. Further tool
// rounds, retries, concurrency, approval, and durable execution belong to Agent.
// This middleware exclusively owns Tools and ToolChoice and cannot be stacked.
// Tool execution failures retain the input request, full assistant proposal, and
// successful prefix in [ToolBatchError]. An error proves no rollback.
// Place history.Middleware.Call outside tool orchestration for user/answer
// history, or inside for the full tool exchange.
// A failed follow-up model call retains all completed effects and the exact
// continuation request in [ToolContinuationError].
package chatclient
