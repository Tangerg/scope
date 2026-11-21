// Package agent is the module overview for durable agent execution. It declares
// no API of its own; every capability lives in a sibling package listed below.
//
// # Packages
//
//   - run: the kernel. Deterministic Programs run as processes over an
//     append-only tree log; Steps are replayed on recovery, and Effects are
//     external work executed once and recorded.
//   - interaction: the model and tool loop, compiled into a Program.
//   - workflow, planning, coordination, team: the other strategies, each a
//     sibling policy package over the same kernel.
//   - messaging: delivery across trees.
//   - run/runtest: conformance suites for Stores, Definitions and resolvers.
//     The PostgreSQL Store is the separately versioned run/postgres module.
//
// # Where to extend
//
// Every extension seam fixes two properties. Retention: whether a change
// affects one call or is written back to the Program's state. Placement: a
// Step runs inside the deterministic Execution, is replayed on recovery, and
// must be bounded and free of I/O; an Effect is external work, executed once
// and recorded.
//
//   - To change only one model request, such as temporary instructions,
//     redaction, routing or caching, decorate the model with chat.Wrap. It runs
//     inside the model Effect: request edits are transient, and the response it
//     returns is recorded.
//   - For approval, audit, offloading large results or per-tool policy,
//     decorate the Tool with tool.Wrap. It runs inside the tool Effect, and the
//     result it returns is recorded.
//   - To assign a scheduling declaration to a Tool you do not own, wrap it in a
//     Tool that implements the declaration and Unwrap() tool.Tool. NewToolSet
//     reads every declaration once, through tool.As.
//   - For deterministic history trimming, set interaction.ContextPolicy. It runs
//     in the Step, and its result is written back.
//   - For compaction that needs I/O, such as a model summary or a memory write,
//     set interaction.ContextPreparer. Its trigger runs in the Step; its work
//     is an Effect of its own, and its result is written back.
//   - To accept or reject a final answer, set interaction.CompletionValidator.
//     It runs in the Step.
//   - For tracing, metrics, call identity or rate limiting across every
//     Effect, register run.Config.DispatchMiddlewares. They run around every
//     dispatch of every Program, built-in strategies included.
//   - To decorate persistence, wrap the run.Store given to run.New.
//
// # Rules
//
// Four rules keep the seams consistent across Scope:
//
//   - A context parameter means the call may block or run unbounded: I/O inside
//     Effects, and unbounded search such as planning.Planner and
//     run.Execution.Step. Bounded Step callbacks take no context, which also
//     keeps ambient context values out of replayed logic.
//   - The Step side never holds an executable, only frozen values taken from
//     one, such as a tool.Contract or the function InputValidator returns.
//   - Values are decorated before they reach Scope, in one shape: func(next X)
//     X, composed outermost-first with nil entries skipped. A configured
//     decorator is constructed with NewMiddleware and exposes methods named
//     after the operation it decorates; an unconstructed one decorates into
//     values whose every call fails.
//   - One fact, one owner. A decorator that implements a declaration overrides
//     the inner one, as an embedding type overrides a promoted method, so the
//     composed value has one owner; likewise the Step that declares an Effect
//     owns its replay policy, and nothing at dispatch time can change it.
//
// A Program's Revision identifies only the Step-side callbacks it runs and
// replays. Declarative configuration is digested automatically, and Effect-side
// collaborators, such as models, Tool execution, middleware and authorization,
// do not affect replay, so changing them never strands a recoverable run.
package agent
