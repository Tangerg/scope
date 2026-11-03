// Package invocation preserves execution evidence at a complete model-call boundary.
// A failed invocation may have consumed resources or produced external work;
// only FailureNotExecuted proves absence of work. No classification authorizes
// retry. Compositions own their complete outcome independently of nested causes.
package invocation
