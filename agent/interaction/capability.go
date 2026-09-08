package interaction

import "github.com/Tangerg/scope/core/tool"

// DirectResultTool is an optional Tool capability declaring that a successful
// model-requested batch containing only such tools returns its ordered results
// directly instead of making another model call. The declaration is frozen by
// NewToolSet; a panic or capability-resolution error rejects construction.
type DirectResultTool interface {
	// ReturnsDirectResult declares whether a successful invocation can terminate
	// Interaction with the ToolResult itself. The answer is read and frozen at
	// ToolSet construction and therefore must not depend on mutable state or
	// perform I/O.
	ReturnsDirectResult() bool
}

// ConcurrentTool is an optional Tool capability declaring which calls are safe
// to overlap within one model-requested batch. Tools without this capability,
// or calls returning concurrent=false, execute alone. A non-empty key names a
// mutually exclusive resource: calls with the same key in that batch never
// overlap. Cross-Process resource coordination remains the Tool owner's job.
//
// Each call keeps its own Effect and may wait for external input while siblings
// continue. The limit counts a waiting child as active until its Tool result is
// complete. Pure scheduling declarations may be reevaluated during recovery.
type ConcurrentTool interface {
	// ConcurrencyKey classifies one exact JSON argument document before any Tool
	// in the batch executes. concurrent=false requires exclusive execution;
	// concurrent=true with the same non-empty key serializes calls to that
	// resource. The method must be deterministic, bounded, side-effect-free, and
	// must not retain arguments.
	ConcurrencyKey(invocation tool.Invocation) (key string, concurrent bool)
}
