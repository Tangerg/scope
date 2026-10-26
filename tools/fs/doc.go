// Package fs exposes LLM-callable filesystem tools (read, write, edit,
// apply_patch, glob, grep) over minimal per-operation backend ports. Backends
// implement only the capabilities they provide and own all content processing;
// the tools only marshal JSON into port calls and back.
//
// Read and mutation backends handle text files only: they reject binary files
// and Write content containing NUL bytes. Grep preserves matched bytes through
// [GrepLine.Content] so legacy encodings do not corrupt or prevent JSON output.
//
// [LocalExecutor] is the local backend. [NewLocalExecutor] derives its own
// handle from an already-open [os.Root] with an absolute Name, so the host keeps
// that authority for its own inspection instead of resolving the pathname
// again; each side closes the handle it owns.
//
// ApplyPatchTool.MutationPaths exposes prospective patch endpoints without I/O;
// hosts discover it through core/tool.Capability for approval or locking.
// Mutation tools carry ordinary backend errors and acknowledged effects as
// core/tool.CallError evidence for evaluation and reconciliation, not as a
// result for the model. Backends establish definite failure or refusal with
// core/tool.Failure.
//
// Glob and Grep have dedicated ports so a remote backend answers a bulk query
// in one round trip instead of shipping every file to the agent.
package fs
