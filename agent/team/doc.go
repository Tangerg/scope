// Package team composes decision-driven collaboration from exact Programs.
// A coordinator consumes [Turn] and returns [Decision]; configured workers use
// ordinary inputs and outputs. [Compile] freezes those bindings and schemas with
// the Runtime's Compiler. Model-backed coordinators compose interaction with
// workflow transforms that render Turn and decode Decision.
//
// ModeContinue starts another coordinator turn while workers remain active.
// ModeWait observes at least one task outcome before the next turn, including
// an outcome that arrived while the current coordinator was running. Start
// failures, task failures, and control rejections remain explicit facts.
// A coordinator failure or exhausted turn bound terminates the collaboration.
//
// Decisions transfer their next working state and work declarations into the
// execution; a retained Decision never competes with those facts. Each task
// advances from Spawn Completion to child Exit. Only Exit proves subtree drain.
// Coordinator inputs are projections of current working state, tasks, controls,
// and configured workers, never an independently writable copy.
//
// Controls compile to child Signal or Cancel Effects. A cancellation receipt
// establishes intent; the task's Exit establishes release. Signals follow the
// recipient's contract, including addressed InputGate waits or interaction
// steering. Task keys name finite executions; follow-up work uses a new key and
// explicit input carrying prior results or application-owned context.
//
// Completion asks the Runtime to cancel and drain remaining descendants before
// publishing this process's Exit. No task outcome bypasses unresolved external
// effects. The Runtime's Log and inspection are the authoritative history and
// live projection; team owns no scheduler, mailbox, persistence, or session.
package team
