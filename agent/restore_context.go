package agent

import "context"

// Restore is a pure reduction. Host values cannot become unrecorded inputs,
// while cancellation and deadlines still belong to the calling operation.
type restoreContext struct{ context.Context }

func (r restoreContext) Value(any) any { return nil }
