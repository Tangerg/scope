// Package childcall owns Framework child response correlation and recoverable
// single-child handshake progress. Strategies own invocation keys, signal
// consumption, result interpretation, and failure policy.
package childcall

import (
	"slices"

	"github.com/Tangerg/scope/agent"
)

// StartMatches binds a parsed start result to its exact declared child.
func StartMatches(result agent.ChildStartResult, key agent.ChildKey, deployment agent.DeploymentRef) bool {
	return result.Key() == key && result.DeploymentRef() == deployment
}

// OpeningMatches binds an acknowledgement to the complete declared wait,
// including child order and the boundary that releases the parent.
func OpeningMatches(opened agent.ChildWaitOpened, want agent.ChildWaitSpec) bool {
	got := opened.Spec()
	return got.Key == want.Key && got.Boundary == want.Boundary &&
		got.Condition == want.Condition && slices.Equal(got.Children, want.Children)
}

// CompletionMatches binds a parsed satisfaction to the active logical and
// Engine-assigned wait identities. The Strategy checks the result set against
// its own remaining children before changing progress.
func CompletionMatches(completed agent.ChildWaitSatisfied, id agent.WaitID, key agent.WaitKey, boundary agent.ChildWaitBoundary) bool {
	return completed.WaitID() == id && completed.Key() == key && completed.Boundary() == boundary
}

// OutcomeMatches binds a parsed outcome to the declared child and its created
// Process. A matching name alone never identifies a completed invocation.
func OutcomeMatches(outcome agent.ChildOutcome, key agent.ChildKey, process agent.ProcessID) bool {
	return outcome.Key() == key && outcome.Result().ProcessID() == process
}
