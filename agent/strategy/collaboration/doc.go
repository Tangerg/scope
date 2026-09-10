// Package collaboration provides bounded, decision-driven multi-agent work.
// A coordinator consumes Turn and returns Decision; configured workers execute
// ordinary inputs and outputs. Both are exact Deployments of any Strategy.
// Model-backed coordinators compose an interaction Strategy with a workflow
// adapter that renders Turn and decodes Decision. This package owns no model
// conversion, dispatcher, scheduler, mailbox, persistence, or product session.
// Construction freezes only child references, descriptors, budgets, and
// capabilities; the Engine resolves the executable Deployments. The root
// Descriptor owns the carried-state and final-output schemas.
//
// Continue runs another coordinator turn while workers remain active. Wait
// observes at least one drained task before running the next turn. Every wait
// also observes workers that finish while the current coordinator is running;
// those facts appear in its successor's Turn without preempting a decision.
// Start failures, task failures, and control rejections remain explicit facts.
// A failed coordinator or exhausted turn bound fails the collaboration.
// Coordinator admission and execution failures preserve the original Failure
// kind, code, and diagnostic; the failed turn remains restorable evidence.
// A rejected worker start counts toward MaxTasks. Admitted tasks count toward
// MaxConcurrentTasks until their drained outcomes are observed. MaxTurns and
// MaxControlsPerTurn bound coordinator decisions and each control batch.
//
// Controls compile to agent.SignalChild and agent.CancelChild. Signals obey the
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
package collaboration
