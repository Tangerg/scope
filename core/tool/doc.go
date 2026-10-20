// Package tool defines the provider-neutral executable Tool protocol, its
// immutable Contract and execution Binding, authorization boundary, typed
// function adapter, and instance-scoped registry.
//
// Invocation carries explicitly supplied continuation input. Result completes a
// call or requests external input; suspension never travels through context or
// an error. The driving runtime owns call identity, checkpoint commitment, input
// waits, and routing each answer back to the requesting call. Func adapts ordinary
// typed functions that always complete; resumable Tools implement Tool directly.
//
// Failure owns a complete unsuccessful invocation outcome. CallError instead
// carries observed evidence through an ordinary error without declaring an
// outcome or retry policy; its evidence is not a model-visible ToolResult.
package tool
