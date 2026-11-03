// Package tool defines the provider-neutral executable Tool protocol, its
// immutable Contract and execution Binding, authorization boundary, typed
// typed signature, function adapter, and instance-scoped registry. Bind freezes
// the definition once; CallMiddleware decorates execution without becoming a
// second owner of the tool's schema or capabilities.
//
// Invocation carries the driver's Admission explicitly: the idempotency key of
// the logical call and any continuation input. Result completes a call or
// requests external input; neither identity nor suspension travels through
// context or an error. The driving runtime owns call identity, checkpoint
// commitment, input waits, and routing each answer back to the requesting call.
// Func adapts ordinary
// typed functions that always complete; resumable Tools implement Tool directly.
//
// Failure owns a complete unsuccessful invocation outcome. CallError instead
// carries observed evidence through an ordinary error without declaring an
// outcome or retry policy; its evidence is not a model-visible ToolResult.
// Effect declares replay safety independently of failure certainty and
// concurrency. A replayable tool can still require serialized execution.
package tool
