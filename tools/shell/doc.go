// Package shell exposes a model-callable shell tool over the [Executor] SPI.
// Local, sandboxed, or remote backends implement [Executor];
// [NewLocalExecutor] runs commands on the host.
//
// A non-zero exit status is a completed execution result. Transport, collection,
// and cleanup errors can leave the outcome unknown; Tool retains the observed
// output as core/tool.CallError evidence without inventing a definite failure.
// Response streams use content.Content so binary data and a UTF-8 prefix split
// by a byte limit survive serialization without losing the other observations.
package shell
