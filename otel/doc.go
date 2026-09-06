// Package otel is the module overview for Scope's OpenTelemetry integration. It
// declares no API of its own; every adapter lives in a subpackage listed below.
//
// The module adds traces, metrics, and model exception events to Core's
// capabilities from the outside. Development exporters write all three OTel
// signals to log/slog. Core itself never imports OpenTelemetry: a wrapper here
// is an ordinary decorator, and this module invents no tracer, meter, registry, or
// observation abstraction. The official API is the vendor-neutral layer.
//
// # Adapters
//
// Adapters are split by the contract they wrap, so one generic config never
// mixes different lifecycles:
//
//   - chat: chat call and lazy stream.
//   - embedding, image, moderation, rerank, speech, transcription: one modality each.
//   - eval: a generic Evaluator result.
//   - rag: retrieval.
//   - tool: tool invocation.
//   - history: history store and conversation listing.
//   - vectorstore: the vector-store capabilities.
//   - agent: managed Process activation, Step, and Effect, through an Observer
//     bound as an agent.EventListener. WrapDispatcher propagates Effect spans
//     into downstream calls. Agent and tool durations use gen_ai.invoke_agent.duration
//     and gen_ai.execute_tool.duration, matching their invocation spans.
//
// Chat and speech streaming record gen_ai.client.operation.time_to_first_chunk
// and gen_ai.client.operation.time_per_output_chunk at chunk arrival. Chat
// includes metadata-only deltas. Token metrics retain known usage even when
// generation fails. Response model identity is recorded only when reported by
// the provider. Cache and reasoning token counts are subsets of the totals.
//
// The a2a and mcp modules are protocol integrations and use the official OTel
// API directly at their own call boundary, so this module has no adapter for
// them.
//
// # What never enters telemetry
//
// Prompts, queries, documents, media, audio, transcripts, and evaluation
// subjects stay out. An adapter records identity, counts, latency, outcome, and
// a stable low-cardinality error classification, because error.type is a metric
// dimension and a provider message would create one time series per string.
// Adapters project provider errors as classified types rather than raw messages.
// Model decorators emit gen_ai.client.operation.exception
// through an optional LoggerProvider at WARN severity. The official log API
// correlates these events with the failed model span; no separate logging
// facade or exporter configuration is introduced by the decorators.
//
// Cancellation and deadlines use context.canceled and
// context.deadline_exceeded. Model adapters classify malformed requests and
// responses with their capability's stable error name. Other errors follow
// the official semconv.ErrorType convention, including a supplied ErrorType
// method or the concrete Go error type.
//
// # Development sinks
//
// The slog subpackage implements the three official SDK exporter interfaces,
// writing one slog record per span, metric batch, and log record. Exporter
// errors belong to the SDK's error handler, never the business return value.
// Flushing is synchronous rather than batched, and an error span is promoted
// to error level.
// In production the composition root swaps these for the official OTLP
// exporters; the wrappers and the Core protocol code do not change.
package otel
