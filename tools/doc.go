// Package tools is the module overview for Scope's ready-to-assemble tools. It
// declares no API of its own; every capability lives in a subpackage.
//
// This module implements executable capability only. The tool protocol, typed
// functions, and the registry belong to core/tool; schemas are derived by
// core/jsonschema. Model-facing capabilities implement core/tool.Tool, so a
// chat client, an Agent, or an MCP server consumes them through one contract.
// Shared backend guarantees also support consumers that do not expose a tool.
//
// # Capabilities
//
//   - shell: run a command and capture its output.
//   - process: own a local command's lifetime and Unix process-group cleanup.
//   - content: preserve arbitrary response bytes as UTF-8 or explicit base64.
//   - fs: read, write, edit, glob, and grep inside an explicitly supplied directory authority.
//   - textread: read text with line numbering and limits.
//   - httpreq: issue an HTTP request against an explicit allowlist.
//   - web: the neutral Searcher and Fetcher SPIs plus the search and fetch
//     tools, with one provider package per vendor.
//   - skills: expose an Agent Skills repository as a callable tool.
//
// # Two tiers
//
// Model-facing capabilities have two tiers. The Tool tier faces the model: JSON in,
// JSON out, schema validation. The Backend Port tier does the work and holds
// all the domain logic — line numbering, binary detection, write locks, path
// authority. That split is what lets a remote or sandboxed backend answer a
// glob or grep in one round trip instead of listing and reading through the
// Tool tier.
//
// # Assembly
//
// There is no global registry: a caller registers exactly the tools it wants.
// Every tool receives its executor or client explicitly. Filesystem roots,
// shell access, network clients, and other authorities are never inferred from
// nil or ambient process state.
//
// # Concurrency and limits
//
// A tool may declare, per call, whether it can overlap others and what it
// conflicts on. Reads declare no conflict; an edit or write keys on its target
// path, so the loop parallelizes different files and serializes the same file.
// Output over a limit is truncated and marked, never turned into an error, so
// the model can decide what to do next.
//
// # Execution evidence
//
// A backend error does not prove that a command or mutation failed without
// effects. Shell, filesystem mutation, and HTTP tools retain available observations in
// core/tool.CallError for diagnostics and evaluation. Runtimes reconcile an
// uncertain outcome before deciding whether to repeat an operation. Definite
// failure and permission refusal use core/tool.Failure; filesystem backends can
// identify rejection before any mutation with fs.ErrMutationRejected.
package tools
