// Package workflow provides deterministic orchestration of Framework-managed
// child Processes. A Workflow is an ordered sequence of sealed Stages; it is
// not a general in-process task graph, scheduler, journal, or node registry.
//
// Use this package when each delegated operation needs its own Process
// identity, snapshot, budget, capabilities, cancellation, and tree recovery.
// Ordinary in-process control flow belongs outside the Agent Framework.
//
// Child admission and execution Failures propagate unchanged, preserving their
// kind, code, and complete diagnostic text. Fan-out waits for the active window
// to drain and propagates its first failure in declaration order. The Process
// tree retains the child identity and Stage binding; Workflow-owned decisions
// use workflow-prefixed failure codes.
// Single-child Stages also wait for the child subtree to drain. Restoring any
// handshake boundary resumes the same invocation and its Engine-assigned wait.
//
// Transform, Switch, Fork, and Loop callbacks run inside a discardable Step.
// They must be bounded, deterministic, side-effect-free, and cooperate with
// context cancellation during CPU work. Context carries cancellation, never
// hidden domain input. A canceled candidate cannot advance committed state.
package workflow
