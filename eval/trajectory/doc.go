// Package trajectory records and evaluates deterministic Agent execution facts.
// It keeps Agent vocabulary outside the subject-agnostic eval kernel and owns no
// experiment persistence, replay scheduler, or product workflow.
// Its strict JSON record preserves activation-local order and explicit semantic
// coverage. Take joins a root subtree before consuming its bounded recording.
// RootUsage and TreeUsage have distinct scopes. Unknown token accounting or
// elapsed time cannot satisfy an upper bound. Cross-restart history remains a
// fragment for historical comparisons; raw wall-clock order is not causal.
package trajectory
