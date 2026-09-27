// Package fs exposes LLM-callable filesystem tools (read, write, edit,
// apply_patch, glob, grep) on top of minimal per-operation ports. Local, sandbox, and
// remote backends implement only the capabilities they provide; the tools
// themselves are thin adapters that marshal LLM JSON into port calls and back.
//
// The local backend accepts one already-open [os.Root] with an absolute Name.
// Hosts retain that authority for additional inspection or policy instead of
// resolving its pathname again. [NewLocalExecutor] derives its own handle from
// the supplied root; the host and executor each close the handle they own.
// Sharing a directory does not synchronize separate host and executor operations.
//
// **Text files only.** Backend implementations MUST reject files
// that look binary (NUL byte in the first 8 KiB is a good default
// heuristic) and reject Write content that contains NUL bytes. Use
// the shell tool if you need to manipulate binary data.
//
// **Tools stay thin.** All content processing — line windowing,
// binary detection, exact replacement, and complete writes — lives
// in the backend, not the tool. The tool's job is JSON in, JSON out.
//
// ApplyPatchTool.MutationPaths exposes prospective patch endpoints without I/O,
// using LocalExecutor's parser and supported-operation rules. Hosts may discover
// this optional method through core/tool.Capability for approval or locking.
// Filesystem authority and hunk applicability are checked during execution;
// ApplyPatchResponse, including a partial response on error, reports actual effects.
// Mutation tools preserve ordinary backend errors and acknowledged observations
// through core/tool.CallError. Such evidence is available for evaluation and
// reconciliation, not a completed result to feed back to the model. Backends
// establish definite failure or permission refusal explicitly with core/tool.Failure.
//
// Why Glob and Grep have dedicated ports (instead of "walk + match" in the
// tool layer): a remote backend cannot afford to ship every file
// across the wire to pattern-match on the agent side. Pushing bulk
// queries into their ports keeps remote implementations one round-trip per call.
package fs
