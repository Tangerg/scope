// Package tool defines the provider-neutral executable Tool protocol, its
// immutable Contract and execution Binding, authorization boundary, typed
// function adapter, and instance-scoped registry.
//
// Failure owns a complete unsuccessful invocation outcome. CallError instead
// carries observed evidence through an ordinary error without declaring an
// outcome or retry policy; its evidence is not a model-visible ToolResult.
package tool
