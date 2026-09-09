// Package trajectory records and evaluates deterministic Agent execution facts.
// It keeps Agent vocabulary outside the subject-agnostic eval kernel and owns no
// experiment persistence, replay scheduler, or product workflow.
// Its JSON record rejects unknown members and encodes duration in nanoseconds.
package trajectory
