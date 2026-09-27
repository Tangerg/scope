// Package shell exposes a single LLM-callable shell tool plus a small
// [Executor] SPI. The package itself does not pick where commands run
// — local, sandboxed, or remote backends each implement [Executor] and
// plug in via [NewTool].
//
// The local executor ([NewLocalExecutor]) is the reference impl and
// covers the common case (run on the same host as the agent).
//
// A non-zero exit status is a completed execution result. Transport, collection,
// and cleanup errors can leave the outcome unknown; Tool retains the observed
// output as core/tool.CallError evidence without inventing a definite failure.
// Response streams use content.Content so binary data and a UTF-8 prefix split
// by a byte limit survive serialization without losing the other observations.
package shell
