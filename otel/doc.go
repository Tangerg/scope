// Package otel is the module overview for Scope's OpenTelemetry integration. It
// declares no API of its own; every adapter lives in a subpackage listed below.
//
// The module decorates Core capabilities from the outside with traces, metrics,
// and model exception events. Core never imports OpenTelemetry, and this module
// invents no tracer, meter, registry, or observation abstraction beyond the
// official vendor-neutral API.
//
// # Adapters
//
// Adapters are split by the contract they wrap, so one config never mixes
// different lifecycles:
//
//   - chat: chat call and lazy stream.
//   - embedding, image, moderation, rerank, speech, transcription: one modality each.
//   - eval: an Assessment with identity bound before its Evaluator runs.
//   - rag: retrieval.
//   - tool: tool invocation.
//   - history: history store and conversation listing.
//   - vectorstore: the vector-store capabilities.
//   - agent: managed Process activation, Step, and Effect, through an Observer
//     bound as an agent.EventListener. WrapDispatcher propagates Effect spans
//     into downstream calls. Agent and tool durations use gen_ai.invoke_agent.duration
//     and gen_ai.execute_tool.duration, matching their invocation spans.
//   - slog: development exporters that write all three signals to log/slog.
//
// Chat and speech streaming record gen_ai.client.operation.time_to_first_chunk
// and gen_ai.client.operation.time_per_output_chunk at chunk arrival. Chat
// includes metadata-only deltas, so first-chunk latency is not first-token latency.
// Provider finish reasons and response model identity are recorded only when
// observed. Token metrics retain known usage even when generation fails. Cache
// and reasoning token counts are subsets of the totals.
//
// The a2a and mcp modules use the official OTel API directly at their own call
// boundary, so this module has no adapter for them.
//
// # What never enters telemetry
//
// Prompts, queries, documents, media, audio, transcripts, and evaluation
// subjects stay out. An adapter records identity, counts, latency, outcome, and
// a stable low-cardinality error classification, because error.type is a metric
// dimension and a provider message would create one time series per string.
// Model decorators emit gen_ai.client.operation.exception at WARN severity
// through an optional LoggerProvider; the official log API correlates these
// events with the failed model span.
//
// Cancellation and deadlines use context.canceled and
// context.deadline_exceeded. Model adapters classify malformed requests and
// responses with their capability's stable error name. Other errors follow
// the official semconv.ErrorType convention, including a supplied ErrorType
// method or the concrete Go error type. Panics end the call observation with
// error.type=panic and propagate the original value unchanged; panic values
// never enter telemetry.
package otel
