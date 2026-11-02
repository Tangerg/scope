package interaction

import "github.com/Tangerg/scope/core/tool"

// DirectResultTool is an optional Tool capability declaring that a successful
// model-requested batch containing only such tools returns its ordered results
// directly instead of making another model call. The declaration is frozen by
// NewToolSet; a panic or capability-resolution error rejects construction.
type DirectResultTool interface {
	// ReturnsDirectResult is read once by NewToolSet; it must not depend on
	// mutable state or perform I/O.
	ReturnsDirectResult() bool
}

// ConcurrentTool supplies an independent scheduling policy for calls within one
// model-requested batch. Tools without a policy, or calls returning
// concurrent=false, execute alone. A non-empty key names a
// mutually exclusive resource: calls with the same key in that batch never
// overlap. Cross-Process resource coordination remains the Tool owner's job.
//
// Each call keeps its own Effect and may wait for external input while siblings
// continue. The limit counts a waiting call as active until its Tool result is
// complete. Pure scheduling declarations may be reevaluated during recovery.
type ConcurrentTool interface {
	// ConcurrencyPolicy returns an immutable classifier, frozen at ToolSet
	// construction. It must not retain the Tool or any execution backend,
	// including through a bound method or closure. Nil requires exclusive calls.
	// The classifier receives an admitted invocation and must be deterministic,
	// bounded, side-effect-free, concurrent-safe, and not retain its input.
	// Its identity and captured configuration belong in the ToolSet Revision.
	ConcurrencyPolicy() func(tool.Invocation) (key string, concurrent bool)
}

// IdempotentTool declares that repeating a call under its original
// tool.Admission.IdempotencyKey cannot duplicate an external effect. Recovery
// then redispatches an interrupted call instead of settling it Unknown, so the
// declaration is a durability promise, not a performance hint: a Tool may make
// it only when its backend deduplicates on the key or the call has no effect.
type IdempotentTool interface {
	// Idempotent is read once by NewToolSet; it must not depend on mutable
	// state or perform I/O.
	Idempotent() bool
}
