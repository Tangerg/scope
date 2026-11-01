// Package chat defines the serializable provider-neutral chat protocol and its
// minimal synchronous [Model] and optional [Streamer] capabilities.
//
// Construct messages and requests with NewSystemMessage, NewUserMessage, and
// NewRequest. Leaf constructors establish protocol shape; aggregate roots and
// model boundaries validate the complete value. Call Validate again after
// mutating exported fields. System messages form a leading prefix so providers
// with a distinct system channel can translate a request without reordering it.
// Options express only per-call overrides; ToolChoice describes portable tool
// selection, ReasoningEffort carries model-advertised intensity without imposing
// one provider's closed enum, and OutputFormat describes the requested
// representation without adopting provider wire naming. Namespaced Extensions
// preserve provider data without expanding the shared protocol for every
// provider feature.
//
// Response is a complete, stable output. Streamer yields ResponseDelta transport
// increments instead of partial Responses; ResponseAccumulator is the single
// promotion path between them and requires a terminal finish reason. Citations,
// refusals, reasoning replay state, and tool calls remain typed protocol values
// while exact provider-only data stays in namespaced metadata.
//
// Requests project tool.Definition; executable tools, registries,
// history, retries, middleware policy, and tool loops belong to higher-level
// modules. Protocol values therefore never retain callbacks, provider clients,
// or other runtime objects.
package chat
