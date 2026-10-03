// Package collaboration provides decision-driven multi-agent work.
// A coordinator consumes Turn and returns Decision; configured workers execute
// ordinary inputs and outputs. Both are exact Deployments of any Strategy.
// Model-backed coordinators compose an interaction Strategy with a workflow
// adapter that renders Turn and decodes Decision. This package owns no model
// conversion, dispatcher, scheduler, mailbox, persistence, or product session.
// Construction binds the coordinator and worker Deployments with their budgets
// and capabilities, and the Engine starts children from those bindings. The
// root Descriptor owns the carried-state and final-output schemas.
//
// ModeContinue runs another coordinator turn while workers remain active.
// ModeWait observes at least one drained task before running the next turn. Every wait
// also observes workers that finish while the current coordinator is running;
// those facts appear in its successor's Turn without preempting a decision.
// Start failures, task failures, and control rejections remain explicit facts.
// A failed coordinator or exhausted turn bound fails the collaboration.
// Turn exhaustion uses execution / collaboration.limit.turns; counter overflow
// uses execution / engine.counter.exhausted. Invalid coordinator
// decisions and protocol frames use contract / collaboration.decision.invalid
// and collaboration.protocol.invalid. These failures discard the candidate.
// A coordinator subtree with unresolved Effects cannot authorize a Decision.
// Worker outcomes retain their complete subtree evidence for coordinator policy.
// Coordinator admission and execution failures preserve the original Failure
// kind, code, and diagnostic; the failed turn remains restorable evidence.
// The current Turn owns its number, input state, and the task outcomes that
// arrived after it opened; the coordinator's Turn input is assembled from these,
// the current tasks and controls, and the configured workers, never retained as
// a copy. A resolved coordinator
// outcome owns the Decision, including the next working state and mode;
// recovery derives them directly without retaining writable copies. Initial
// state is retained only until the first Turn takes ownership of it.
//
// Controls compile to agent.NewChildSignalEffect and agent.NewChildCancelEffect. Signals obey the
// recipient Strategy's protocol, including interaction steering at its safe
// boundary. They do not preempt a model request or release an unrelated wait.
// For input that must wake a waiting collaboration, configure a coordination
// InputGate as a worker. Its addressed answer completes the gate, wakes the
// coordinator, and retains the original Signal identity. Re-arm a new gate when
// another answer is required; never redirect an ambiguous delivery to it.
//
// Task keys identify finite executions. Follow-up work uses a new key and an
// explicit Input carrying prior results or application-owned context. Group
// discussion, round-robin selection, and peer-message routing are coordinator
// policies over the same task and control contracts. They do not resurrect
// terminal Processes or introduce a second long-lived session owner.
//
// Completing the collaboration cancels remaining descendants. A cancellation
// receipt confirms intent; only a drained outcome or Process.Join establishes
// resource release. Bounds complement the Engine's monotonic tree budgets;
// child allocations are never refunded. Inspect execution through the Engine's
// canonical tree inspection and snapshot APIs. Restore validates the strict
// current state against its exact frozen configuration and child contracts.
//
// Definitions reject unaddressed Host Signals through Descriptor.SignalSchema.
// Engine-owned settlements are consumed only at their matching protocol phase;
// Child-wait openings may share a window with the following completion. InputGate
// replies, when used as children, must address the child's current WaitID.
package collaboration
