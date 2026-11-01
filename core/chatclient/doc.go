// Package chatclient provides direct, optional conveniences around the minimal
// chat protocols and model capabilities defined by Core. Client.Output applies
// a provider-neutral OutputFormat and strictly decodes naturally completed
// text; a caller that already holds a response, such as a managed Agent, reuses
// that one decode rule through Format.Decode without another model call.
// Other completion reasons remain identifiable as OutputCompletionError.
// Typed output has only a terminal value; callers that need transport deltas
// use StreamClient.Stream with a required streaming dependency. Scope-driven tool
// batches and continuations use the Agent runtime; direct model calls never dispatch local tools.
package chatclient
