// Package chatclient provides direct, optional conveniences around the minimal
// chat protocols and model capabilities defined by Core. Client.Output applies
// a provider-neutral OutputFormat and strictly decodes naturally completed
// text. Other completion reasons remain identifiable as OutputCompletionError.
// Typed output has only a terminal value; callers that need transport deltas
// use StreamClient.Stream with a required streaming dependency.
//
// [NewToolMiddleware] covers the deliberately small direct-use path: it
// advertises a frozen executable Tool set, validates one returned call batch,
// executes it serially, and performs one follow-up model call. Further tool
// rounds, retries, concurrency, approval, and durable execution belong to Agent.
package chatclient
